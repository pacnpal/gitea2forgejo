// Package restore performs Phase 2 (ingest) on the target Forgejo host.
//
// It extracts the gitea-dump tarball locally, rsyncs the data/repos/custom
// subtrees to the target host over SSH, imports the native DB dump, applies
// the forgejo#7638 schema-version trick, wipes stale Bleve indexers, writes
// a translated app.ini, restarts Forgejo, and runs doctor + regenerate-hooks.
package restore

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// ExtractDump extracts work_dir/gitea-dump.<ext> into work_dir/extracted/,
// using system `tar` (with --zstd or --gzip auto-detected by extension).
// Returns the extracted root.
func ExtractDump(cfg *config.Config, log *slog.Logger) (string, error) {
	ext := cfg.Options.DumpFormat
	tarPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	if _, err := os.Stat(tarPath); err != nil {
		return "", fmt.Errorf("dump tarball not found at %s: %w", tarPath, err)
	}
	outDir := filepath.Join(cfg.WorkDir, "extracted")
	if err := os.RemoveAll(outDir); err != nil {
		return "", fmt.Errorf("clean %s: %w", outDir, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}

	args := []string{"-x", "-f", tarPath, "-C", outDir}
	switch ext {
	case "tar.zst":
		args = append([]string{"--zstd"}, args...)
	case "tar.gz":
		args = append([]string{"-z"}, args...)
	case "tar":
		// no extra flag
	case "zip":
		cmd := exec.Command("unzip", "-q", tarPath, "-d", outDir)
		return outDir, runCmd(cmd, log, "unzip")
	default:
		return "", fmt.Errorf("unsupported dump_format %q", ext)
	}
	cmd := exec.Command("tar", args...)
	log.Info("extract: starting", "tar", tarPath, "to", outDir, "args", args)
	return outDir, runCmd(cmd, log, "tar")
}

// RsyncToTarget copies a local directory tree to the target path. When
// the target directory is reachable on the mig-host's local filesystem
// (sameHost check: we can stat the target directory directly), the
// rsync invocation drops the SSH transport entirely and runs locally
// — significantly faster for loopback / Unraid / homelab use.
//
// remoteDst is the absolute path on the target, which for same-host
// runs is ALSO a path on our local filesystem.
func RsyncToTarget(cfg *config.Config, localSrc, remoteDst string, log *slog.Logger) error {
	if _, err := os.Stat(localSrc); err != nil {
		log.Info("rsync: source missing, skipping", "src", localSrc)
		return nil
	}

	local := isLocalPath(remoteDst)
	var args []string
	var dstDesc string
	if local {
		args = []string{
			"-aHAX",
			"--delete-after",
			"--numeric-ids",
			"--info=progress2",
			ensureTrailingSlash(localSrc),
			ensureTrailingSlash(remoteDst),
		}
		dstDesc = remoteDst
	} else {
		if cfg.Target.SSH == nil {
			return fmt.Errorf("rsync requires target.ssh block for remote targets")
		}
		rspec := fmt.Sprintf("%s@%s:%s", cfg.Target.SSH.User, cfg.Target.SSH.Host, remoteDst)
		sshCmd := fmt.Sprintf("ssh -p %d -i %s -o StrictHostKeyChecking=no",
			cfg.Target.SSH.Port, cfg.Target.SSH.Key)
		args = []string{
			"-aHAX",
			"--delete-after",
			"--numeric-ids",
			"--info=progress2",
			"-e", sshCmd,
			ensureTrailingSlash(localSrc),
			rspec,
		}
		dstDesc = rspec
	}

	cmd := exec.Command("rsync", args...)
	log.Info("rsync: starting", "src", localSrc, "dst", dstDesc, "local_fast_path", local)
	start := time.Now()
	if err := runCmd(cmd, log, "rsync"); err != nil {
		return fmt.Errorf("rsync %s -> %s: %w", localSrc, dstDesc, err)
	}
	log.Info("rsync: done", "elapsed", time.Since(start).Round(time.Second))
	return nil
}

// isLocalPath returns true when the given absolute path (or its parent
// dir) is reachable via the local filesystem. Used to detect the
// loopback case where we can skip SSH entirely.
func isLocalPath(p string) bool {
	if p == "" {
		return false
	}
	if _, err := os.Stat(p); err == nil {
		return true
	}
	// Path may not exist yet (fresh target dir). Walk up to the first
	// extant ancestor and test there.
	for cur := filepath.Dir(p); cur != "" && cur != "."; cur = filepath.Dir(cur) {
		if _, err := os.Stat(cur); err == nil {
			return true
		}
		if cur == "/" {
			break
		}
	}
	return false
}

// StageFiles extracts the dump and gets its data/repos/custom subtrees
// to the target host. Two code paths:
//
//   LOCAL fast path (target filesystem accessible from this process):
//     Extract directly onto the target filesystem in a staging dir,
//     then rename the subdirs into place. rename within one fs is
//     O(1) — zero bytes copied after the extract itself. Falls back
//     to rsync for any subdir whose rename fails (cross-fs, existing
//     non-empty dest, etc.).
//
//   REMOTE path (target behind SSH):
//     Extract locally to work_dir/extracted/ then rsync each subtree
//     over SSH. No way around the byte transfer in this case.
func StageFiles(cfg *config.Config, log *slog.Logger) error {
	if isLocalPath(cfg.Target.DataDir) {
		return stageFilesLocal(cfg, log)
	}
	return stageFilesRemote(cfg, log)
}

func stageFilesRemote(cfg *config.Config, log *slog.Logger) error {
	extracted, err := ExtractDump(cfg, log)
	if err != nil {
		return err
	}
	mapping := []struct{ local, remote string }{
		{filepath.Join(extracted, "data"), cfg.Target.DataDir},
		{filepath.Join(extracted, "repos"), cfg.Target.RepoRoot},
		{filepath.Join(extracted, "custom"), cfg.Target.CustomDir},
	}
	for _, m := range mapping {
		if err := RsyncToTarget(cfg, m.local, m.remote, log); err != nil {
			return err
		}
	}
	return nil
}

// stageFilesLocal is the loopback optimization: extract the tarball
// onto the target's OWN filesystem, then rename the subdirs into their
// final homes. No cross-fs copy except the tar decompression itself.
//
// Staging dir selection mirrors dump's Docker-aware logic: when the
// target is containerized, use the HOST side of a target.docker.mounts
// entry so the staging dir is guaranteed to live on the same filesystem
// as — and be visible from — the container. Otherwise, use the parent
// of data_dir.
func stageFilesLocal(cfg *config.Config, log *slog.Logger) error {
	ext := cfg.Options.DumpFormat
	tarPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	if _, err := os.Stat(tarPath); err != nil {
		return fmt.Errorf("dump tarball not found: %w", err)
	}

	stage := pickStagingDir(cfg)
	if err := os.RemoveAll(stage); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean stale staging %s: %w", stage, err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("mkdir staging %s: %w", stage, err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	log.Info("extract: on target filesystem (loopback fast path)",
		"tar", tarPath, "stage", stage)
	if err := extractInto(tarPath, stage, ext, log); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Rename subdirs into place. For each subtree in the tarball, the
	// rename is O(1) intra-filesystem. Cross-filesystem renames (EXDEV)
	// fall back to rsync.
	mapping := []struct{ src, dst string }{
		{filepath.Join(stage, "data"), cfg.Target.DataDir},
		{filepath.Join(stage, "repos"), cfg.Target.RepoRoot},
		{filepath.Join(stage, "custom"), cfg.Target.CustomDir},
	}
	for _, m := range mapping {
		if _, err := os.Stat(m.src); err != nil {
			log.Info("staging: tarball subdir absent; skipping", "path", m.src)
			continue
		}
		if err := moveIntoPlace(cfg, m.src, m.dst, log); err != nil {
			return err
		}
	}
	return nil
}

// extractInto shells out to the system tar with the right decompressor
// flag. Mirrors ExtractDump's dispatch but writes to an arbitrary path.
func extractInto(tarPath, dst, ext string, log *slog.Logger) error {
	args := []string{"-x", "-f", tarPath, "-C", dst}
	switch ext {
	case "tar.zst":
		args = append([]string{"--zstd"}, args...)
	case "tar.gz":
		args = append([]string{"-z"}, args...)
	case "tar":
	case "zip":
		return runCmd(exec.Command("unzip", "-q", tarPath, "-d", dst), log, "unzip")
	default:
		return fmt.Errorf("unsupported dump_format %q", ext)
	}
	return runCmd(exec.Command("tar", args...), log, "tar")
}

// moveIntoPlace gets a subtree from staging into its target location.
// Tries rename first (intra-fs = O(1)); on EXDEV or a non-empty target
// falls back to local rsync (still no SSH).
func moveIntoPlace(cfg *config.Config, src, dst string, log *slog.Logger) error {
	// Ensure parent of dst exists.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dst, err)
	}
	// If dst exists and is empty, remove it so rename can land.
	if empty, err := isEmptyDir(dst); err == nil && empty {
		_ = os.RemoveAll(dst)
	}
	if err := os.Rename(src, dst); err == nil {
		log.Info("staging: renamed into place (zero copy)", "dst", dst)
		return nil
	}
	// Rename failed — fall back to rsync. Still local, still faster
	// than SSH rsync.
	log.Info("staging: rename failed; falling back to local rsync", "dst", dst)
	return RsyncToTarget(cfg, src, dst, log)
}

// pickStagingDir selects a HOST path to extract into such that subsequent
// os.Rename calls into the target directories land on the same filesystem.
//
// Preference order:
//   1. A subdir under the TARGET's docker bind mount host path (same
//      logic as dump's pickDockerScratch — guarantees intra-fs + visible
//      from the container if we need to chown through docker exec).
//   2. The parent of target.data_dir (bare-metal default).
func pickStagingDir(cfg *config.Config) string {
	const name = ".gitea2forgejo-stage"
	if cfg.Target.Docker != nil && len(cfg.Target.Docker.Mounts) > 0 {
		// Prefer a mount that covers data_dir (that's where we'll move
		// most bytes); fall back to the first mount.
		if cfg.Target.DataDir != "" && cfg.Target.Docker.HostToContainer(cfg.Target.DataDir) != "" {
			// Walk up data_dir until we hit a mount's host side.
			for _, m := range cfg.Target.Docker.Mounts {
				host := strings.TrimRight(m.Host, "/")
				if host == "" {
					continue
				}
				if cfg.Target.DataDir == host ||
					strings.HasPrefix(cfg.Target.DataDir+"/", host+"/") {
					return filepath.Join(host, name)
				}
			}
		}
		return filepath.Join(cfg.Target.Docker.Mounts[0].Host, name)
	}
	return filepath.Join(filepath.Dir(cfg.Target.DataDir), name)
}

func isEmptyDir(p string) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err == io.EOF || len(names) == 0 {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// StopService stops the target Forgejo. Docker targets use `docker stop
// <container>`; bare-metal targets use `systemctl stop forgejo`. The
// previous systemctl-only implementation exited 127 on hosts without
// systemd (Unraid, Alpine, Docker-only VMs).
func StopService(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	if cfg.Target.Docker != nil && cfg.Target.Docker.Container != "" {
		log.Info("target: stopping forgejo container", "container", cfg.Target.Docker.Container)
		cmd := fmt.Sprintf("%s stop %s",
			shQuote(orDefaultStr(cfg.Target.Docker.Binary, "docker")),
			shQuote(cfg.Target.Docker.Container))
		out, err := ssh.Run(cmd)
		if err != nil {
			return fmt.Errorf("docker stop: %w (%s)", err, string(out))
		}
		return nil
	}
	log.Info("target: stopping forgejo service (systemd)")
	out, err := ssh.Run("systemctl stop forgejo")
	if err != nil {
		return fmt.Errorf("systemctl stop: %w (%s)", err, string(out))
	}
	return nil
}

// StartService is the inverse of StopService.
func StartService(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	if cfg.Target.Docker != nil && cfg.Target.Docker.Container != "" {
		log.Info("target: starting forgejo container", "container", cfg.Target.Docker.Container)
		cmd := fmt.Sprintf("%s start %s",
			shQuote(orDefaultStr(cfg.Target.Docker.Binary, "docker")),
			shQuote(cfg.Target.Docker.Container))
		out, err := ssh.Run(cmd)
		if err != nil {
			return fmt.Errorf("docker start: %w (%s)", err, string(out))
		}
		return nil
	}
	log.Info("target: starting forgejo service (systemd)")
	out, err := ssh.Run("systemctl start forgejo")
	if err != nil {
		return fmt.Errorf("systemctl start: %w (%s)", err, string(out))
	}
	return nil
}

func orDefaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// ChownInContainer re-runs chown through `docker exec -u 0` against the
// already-started Forgejo container. The pre-start sidecar chown
// (chownViaDocker) gets everything that exists on disk before boot,
// but Forgejo's s6-overlay init can create fresh files during startup
// (most commonly /data/gitea/log) as root — leaving doctor unable to
// write because it runs as git. Running chown from inside the live
// container as uid 0 catches those post-boot additions.
//
// No-op on bare-metal targets; Chown() already did the host-side work.
func ChownInContainer(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	d := cfg.Target.Docker
	if d == nil || d.Container == "" {
		return nil
	}
	bin := orDefaultStr(d.Binary, "docker")
	user := orDefaultStr(d.User, "git")
	spec := user + ":" + user

	var dirs []string
	for _, hostDir := range []string{cfg.Target.DataDir, cfg.Target.RepoRoot, cfg.Target.CustomDir} {
		if hostDir == "" {
			continue
		}
		cont := d.HostToContainer(hostDir)
		if cont == "" {
			continue
		}
		dirs = append(dirs, cont)
	}
	if len(dirs) == 0 {
		return nil
	}

	// sh -c … so the exec runs chown against each dir with && short-
	// circuit, stopping on the first failure with a clear error. -u 0
	// forces root inside the container regardless of the image's USER.
	var paths []string
	for _, d := range dirs {
		paths = append(paths, shQuote(d))
	}
	inner := fmt.Sprintf("chown -R %s %s", shQuote(spec), strings.Join(paths, " "))
	cmd := fmt.Sprintf("%s exec -u 0 %s sh -c %s",
		shQuote(bin), shQuote(d.Container), shQuote(inner))
	log.Info("chown (docker exec post-start)", "dirs", dirs, "owner", spec)
	if out, err := ssh.Run(cmd); err != nil {
		return fmt.Errorf("post-start chown: %w (%s)", err, string(out))
	}
	return nil
}

// Chown flips ownership of the target data tree to the Forgejo user.
//
// Two paths:
//   Docker: run `docker exec <container> chown -R <user>:<user> <container-path>`.
//           The container's user namespace resolves the name correctly
//           (gitea/forgejo images include the `git` user); host-side
//           users like `forgejo` often don't exist on minimal distros
//           like Unraid. Paths are translated host→container via
//           target.docker.HostToContainer.
//   Bare-metal: `ssh chown -R forgejo:forgejo <host-path>` as before.
func Chown(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	if cfg.Target.Docker != nil && cfg.Target.Docker.Container != "" {
		return chownViaDocker(ssh, cfg, log)
	}
	user := cfg.Target.RunAs
	if user == "" {
		user = "forgejo"
	}
	spec := user + ":" + user
	for _, dir := range []string{cfg.Target.DataDir, cfg.Target.RepoRoot, cfg.Target.CustomDir} {
		if dir == "" {
			continue
		}
		cmd := fmt.Sprintf("chown -R %s %s", spec, shQuote(dir))
		log.Info("chown (host)", "dir", dir, "owner", spec)
		out, err := ssh.Run(cmd)
		if err != nil {
			return fmt.Errorf("chown %s: %w (%s)", dir, err, string(out))
		}
	}
	return nil
}

func chownViaDocker(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	d := cfg.Target.Docker
	bin := orDefaultStr(d.Binary, "docker")
	user := orDefaultStr(d.User, "git")
	spec := user + ":" + user

	// Collect (host, container) path pairs for every configured data dir
	// that (a) translates to a container path via the bind-mount table
	// and (b) actually exists on the host. Gitea dumps commonly omit
	// the custom/ subdir, leaving cfg.Target.CustomDir with no host-
	// side directory to touch; passing a missing path to chown makes
	// the whole batched invocation exit 1. test -d on the host side
	// is cheap and avoids round-tripping through docker just to fail.
	type entry struct{ host, cont string }
	var entries []entry
	for _, h := range []string{cfg.Target.DataDir, cfg.Target.RepoRoot, cfg.Target.CustomDir} {
		if h == "" {
			continue
		}
		cont := d.HostToContainer(h)
		if cont == "" {
			log.Warn("chown (docker): host path not under any bind mount; skipping",
				"host", h)
			continue
		}
		if _, err := ssh.Run(fmt.Sprintf("test -d %s", shQuote(h))); err != nil {
			log.Warn("chown (docker): host path not present on target; skipping",
				"host", h)
			continue
		}
		entries = append(entries, entry{h, cont})
	}
	if len(entries) == 0 {
		return nil
	}

	// Resolve the Forgejo container's image so the throwaway chown
	// container has the same /etc/passwd (critical: "git" must resolve
	// to the same uid/gid Forgejo runs as). `docker exec` would be
	// simpler but the Forgejo container is stopped for the restore
	// window — exec requires a running container, run --volumes-from
	// does not. The inherited volumes land at the same paths the
	// Forgejo container sees, so the container-side paths we already
	// computed work verbatim.
	image, err := inspectContainerImage(ssh, bin, d.Container)
	if err != nil {
		return err
	}

	// Forgejo's official image ships USER 1000:1000 so its default
	// process runs as the unprivileged git user. Overriding the
	// entrypoint doesn't reset the user, so chown -R on root-owned
	// files (those rsync just wrote as the SSH user) fails with
	// EPERM. Force the sidecar to root; the chown target is still
	// git:git because /etc/passwd comes from the Forgejo image.
	args := []string{
		shQuote(bin), "run", "--rm",
		"--user", "0:0",
		"--volumes-from", shQuote(d.Container),
		"--entrypoint", "chown",
		shQuote(image),
		"-R", shQuote(spec),
	}
	var contDirs []string
	for _, e := range entries {
		args = append(args, shQuote(e.cont))
		contDirs = append(contDirs, e.cont)
	}
	cmd := strings.Join(args, " ")
	log.Info("chown (docker sidecar)", "image", image, "dirs", contDirs, "owner", spec)
	out, err := ssh.Run(cmd)
	if err != nil {
		return fmt.Errorf("chown via sidecar: %w (%s)", err, string(out))
	}
	return nil
}

// inspectContainerImage returns the image name for the named container.
// Retries up to 3 times when the inspect output isn't printable ASCII:
// an empty or binary response has been observed on some Unraid sshd +
// docker combinations, sometimes after a failing test -d in the same
// SSH multiplex. Letting null-padded bytes flow into `docker run`
// produces an opaque EOF — re-running docker inspect is cheap and
// covers the glitch.
func inspectContainerImage(ssh *remote.Client, bin, container string) (string, error) {
	cmd := fmt.Sprintf("%s inspect --format '{{.Config.Image}}' %s",
		shQuote(bin), shQuote(container))
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		out, err := ssh.Run(cmd)
		if err != nil {
			last = fmt.Errorf("docker inspect (attempt %d): %w (%q)", attempt, err, string(out))
			continue
		}
		image := strings.TrimSpace(string(out))
		if image != "" && isPrintableASCII(image) {
			return image, nil
		}
		preview := []byte(image)
		if len(preview) > 32 {
			preview = preview[:32]
		}
		last = fmt.Errorf("docker inspect returned non-printable output on attempt %d (%d bytes, hex=%x)",
			attempt, len(image), preview)
	}
	return "", fmt.Errorf("resolve container image for %q: %w — try restarting sshd / docker on the target, or set target.docker.image manually",
		container, last)
}

// isPrintableASCII reports whether every byte of s is in 0x20..0x7e.
// Used to sanity-check SSH-captured output that is meant to be a
// repo:tag style string.
func isPrintableASCII(s string) bool {
	if s == "" {
		return false
	}
	for _, b := range []byte(s) {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

func runCmd(cmd *exec.Cmd, log *slog.Logger, prefix string) error {
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() { streamLines(stdout, log, prefix); done <- struct{}{} }()
	go func() { streamLines(stderr, log, prefix); done <- struct{}{} }()
	<-done
	<-done
	return cmd.Wait()
}

func streamLines(r io.ReadCloser, log *slog.Logger, prefix string) {
	defer r.Close()
	buf := make([]byte, 4096)
	var line []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			line = append(line, buf[:n]...)
			for {
				i := bytesIndex(line, '\n')
				if i < 0 {
					break
				}
				log.Info(prefix, "msg", strings.TrimRight(string(line[:i]), "\r"))
				line = line[i+1:]
			}
		}
		if err != nil {
			if len(line) > 0 {
				log.Info(prefix, "msg", strings.TrimRight(string(line), "\r"))
			}
			return
		}
	}
}

func bytesIndex(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func ensureTrailingSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
