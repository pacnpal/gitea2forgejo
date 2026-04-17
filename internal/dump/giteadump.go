package dump

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// GiteaDump runs `gitea dump` on the source, fetches the resulting tarball
// into workDir/gitea-dump.<ext>, and returns the local path.
//
// Two flows depending on whether the source is containerized:
//
//   - Bare-metal (source.docker unset): SSH to host, run `gitea dump` with
//     HOST paths, SFTP the file back.
//   - Docker: SSH to host, query `docker inspect` to translate the host
//     config path to its container-internal equivalent, run `gitea dump`
//     inside the container writing to /tmp (container), then `docker cp`
//     the file out to a host temp location, then SFTP to local.
//
// Large instances can take hours; output streams to the logger live.
func GiteaDump(cfg *config.Config, log *slog.Logger) (string, error) {
	if cfg.Source.SSH == nil {
		return "", fmt.Errorf("gitea dump requires source.ssh block (filesystem access)")
	}
	cli, err := remote.Dial(cfg.Source.SSH)
	if err != nil {
		return "", err
	}
	defer cli.Close()

	if cfg.Source.Docker != nil && cfg.Source.Docker.Container != "" {
		return giteaDumpDocker(cfg, cli, log)
	}
	return giteaDumpBareMetal(cfg, cli, log)
}

func giteaDumpBareMetal(cfg *config.Config, cli *remote.Client, log *slog.Logger) (string, error) {
	remoteDir := cfg.Source.RemoteWorkDir
	if _, err := cli.Run("mkdir -p " + shQuote(remoteDir)); err != nil {
		return "", fmt.Errorf("mkdir remote %s: %w", remoteDir, err)
	}
	if cfg.Source.RunAs != "" {
		if _, err := cli.Run("chown " + shQuote(cfg.Source.RunAs) + " " + shQuote(remoteDir)); err != nil {
			log.Warn("chown remote work dir (continuing)", "err", err)
		}
	}
	ext := cfg.Options.DumpFormat
	remotePath := path.Join(remoteDir, "gitea-dump."+ext)

	cmd := buildGiteaDumpCmd(cfg.Source, remotePath, ext)
	log.Info("gitea dump: starting (bare-metal)", "host", cfg.Source.SSH.Host)
	start := time.Now()
	if err := cli.RunStream(cmd, &logWriter{log: log, prefix: "gitea-dump"}); err != nil {
		return "", fmt.Errorf("gitea dump failed: %w", err)
	}
	log.Info("gitea dump: done", "elapsed", time.Since(start).Round(time.Second))

	stat, err := cli.Run("stat -c '%s' " + shQuote(remotePath))
	if err != nil {
		return "", fmt.Errorf("stat remote dump: %w", err)
	}
	log.Info("gitea dump: remote file", "path", remotePath, "size_bytes", string(bytes.TrimSpace(stat)))

	localPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	fetchStart := time.Now()
	if err := cli.FetchFile(remotePath, localPath); err != nil {
		return "", fmt.Errorf("fetch dump: %w", err)
	}
	if fi, err := os.Stat(localPath); err == nil {
		log.Info("gitea dump: fetched",
			"size_bytes", fi.Size(),
			"elapsed", time.Since(fetchStart).Round(time.Second))
	}
	_, _ = cli.Run("rm -f " + shQuote(remotePath))
	return localPath, nil
}

// giteaDumpDocker runs `gitea dump` inside a container.
//
// Writing the tarball to container `/tmp` is unsafe in practice — that's
// often a small tmpfs (Docker default = 64 MiB to a few hundred MiB),
// nowhere near enough for a multi-GB dump of a real instance. Instead we
// prefer a SUBDIRECTORY UNDER A BIND-MOUNT so the dump lands on host-
// backed disk with real capacity, and SFTP can read it directly from the
// host side without any docker cp round-trip. Fallback to /tmp + docker
// cp only when nothing is bind-mounted (vanishingly rare).
func giteaDumpDocker(cfg *config.Config, cli *remote.Client, log *slog.Logger) (string, error) {
	d := cfg.Source.Docker
	ext := cfg.Options.DumpFormat

	mounts, err := inspectMounts(cli, d.Container)
	if err != nil {
		return "", fmt.Errorf("docker inspect mounts: %w", err)
	}
	containerConfig := hostToContainer(cfg.Source.ConfigFile, mounts)
	if containerConfig == "" {
		return "", fmt.Errorf(
			"source.config_file (%s) is not under any bind mount of container %q — "+
				"either ensure it's bind-mounted, or set source.config_file to the container-internal path",
			cfg.Source.ConfigFile, d.Container)
	}

	// Preferred scratch: a subdir under data_dir (bind-mounted → host disk).
	// Alt 1: under remote_work_dir if it's bind-mounted.
	// Alt 2 (fallback): container /tmp + docker cp. Space-risky.
	hostWork, containerWork, needsDockerCp := pickDockerScratch(cfg, mounts)

	// mkdir (and chown if we have a user) inside the container.
	mkdirInner := fmt.Sprintf("mkdir -p %s", shQuote(containerWork))
	if u := orDefault(d.User, "git"); u != "" {
		mkdirInner += fmt.Sprintf(" && chown -R %s %s", shQuote(u), shQuote(containerWork))
	}
	mkdirCmd := fmt.Sprintf("%s exec %s sh -c %s",
		shQuote(orDefault(d.Binary, "docker")),
		shQuote(d.Container),
		shQuote(mkdirInner))
	if _, err := cli.Run(mkdirCmd); err != nil {
		return "", fmt.Errorf("mkdir in container: %w", err)
	}

	containerFile := path.Join(containerWork, "gitea-dump."+ext)

	// Run `gitea dump` inside the container with CONTAINER paths. We also
	// point --tempdir at the same bind-mounted directory so the staging
	// files benefit from the host disk too.
	inner := strings.Join([]string{
		shQuote(orDefault(cfg.Source.Binary, "gitea")),
		"dump",
		"--config", shQuote(containerConfig),
		"--file", shQuote(containerFile),
		"--type", shQuote(ext),
		"--tempdir", shQuote(containerWork),
		"--skip-log",
		"--skip-index",
	}, " ")
	dumpCmd := wrapDockerCmd(d, inner)
	log.Info("gitea dump: starting (docker)",
		"container", d.Container,
		"container_config", containerConfig,
		"container_scratch", containerWork,
		"host_scratch", hostWork,
		"docker_cp_fallback", needsDockerCp)
	start := time.Now()
	if err := cli.RunStream(dumpCmd, &logWriter{log: log, prefix: "gitea-dump"}); err != nil {
		return "", fmt.Errorf("gitea dump failed: %w", err)
	}
	log.Info("gitea dump: done", "elapsed", time.Since(start).Round(time.Second))

	// Locate the file on the host side so we can SFTP it.
	var hostFile string
	if needsDockerCp {
		// Container /tmp → docker cp to a host temp location.
		hostFile = fmt.Sprintf("/tmp/gitea2forgejo-dump.%s", ext)
		cpCmd := fmt.Sprintf("%s cp %s:%s %s",
			shQuote(orDefault(d.Binary, "docker")),
			shQuote(d.Container),
			shQuote(containerFile),
			shQuote(hostFile))
		log.Info("docker cp: extracting dump", "src", d.Container+":"+containerFile, "dst", hostFile)
		if _, err := cli.Run(cpCmd); err != nil {
			return "", fmt.Errorf("docker cp: %w", err)
		}
	} else {
		// Bind-mounted: the file is already visible on the host at hostWork.
		hostFile = path.Join(hostWork, "gitea-dump."+ext)
	}

	localPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	fetchStart := time.Now()
	if err := cli.FetchFile(hostFile, localPath); err != nil {
		return "", fmt.Errorf("fetch dump from %s: %w", hostFile, err)
	}
	if fi, err := os.Stat(localPath); err == nil {
		log.Info("gitea dump: fetched",
			"size_bytes", fi.Size(),
			"elapsed", time.Since(fetchStart).Round(time.Second))
	}

	// Cleanup. Remove the container-side scratch (which also clears the
	// host side when bind-mounted). When we used docker cp, also remove
	// the host tempfile.
	_, _ = cli.Run(fmt.Sprintf("%s exec %s rm -rf %s",
		shQuote(orDefault(d.Binary, "docker")), shQuote(d.Container), shQuote(containerWork)))
	if needsDockerCp {
		_, _ = cli.Run("rm -f " + shQuote(hostFile))
	}
	return localPath, nil
}

// pickDockerScratch chooses where to land the dump output. Returns
// (hostPath, containerPath, needsDockerCp).
//
// Priority:
//  1. A subdir of cfg.Source.DataDir (guaranteed bind-mounted).
//  2. cfg.Source.RemoteWorkDir if it's under a bind mount.
//  3. Container /tmp (docker-cp fallback — small, only usable for
//     very small instances).
func pickDockerScratch(cfg *config.Config, mounts [][2]string) (hostPath, containerPath string, needsDockerCp bool) {
	const subdir = "gitea2forgejo-dump"

	if cfg.Source.DataDir != "" {
		if cc := hostToContainer(cfg.Source.DataDir, mounts); cc != "" {
			return path.Join(cfg.Source.DataDir, subdir),
				path.Join(cc, subdir),
				false
		}
	}
	if cfg.Source.RemoteWorkDir != "" {
		if cc := hostToContainer(cfg.Source.RemoteWorkDir, mounts); cc != "" {
			return cfg.Source.RemoteWorkDir, cc, false
		}
	}
	// Container /tmp — needs docker cp to get it out, and subject to
	// tmpfs size limits.
	return "", "/tmp/" + subdir, true
}

// inspectMounts returns all bind-mount records for a container as
// [{host, container}]. Uses the same format string initcmd.dockerMounts
// does; duplicated to avoid a dependency cycle.
func inspectMounts(cli *remote.Client, container string) ([][2]string, error) {
	out, err := cli.Run(fmt.Sprintf(
		"docker inspect --format '{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}' %s",
		shQuote(container)))
	if err != nil {
		return nil, err
	}
	var ms [][2]string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		ms = append(ms, [2]string{fields[0], fields[1]}) // [host, container]
	}
	return ms, nil
}

// hostToContainer finds the longest-prefix bind-mount whose host side is
// a prefix of hostPath, and returns the equivalent path on the container
// side. Returns "" when no mount matches.
func hostToContainer(hostPath string, mounts [][2]string) string {
	if hostPath == "" {
		return ""
	}
	hostPath = strings.TrimRight(hostPath, "/")
	best := -1
	for i, m := range mounts {
		host := strings.TrimRight(m[0], "/")
		if hostPath == host || strings.HasPrefix(hostPath, host+"/") {
			if best < 0 ||
				len(strings.TrimRight(mounts[best][0], "/")) < len(host) {
				best = i
			}
		}
	}
	if best < 0 {
		return ""
	}
	host := strings.TrimRight(mounts[best][0], "/")
	cont := strings.TrimRight(mounts[best][1], "/")
	rel := strings.TrimPrefix(hostPath, host)
	return path.Clean(cont + rel)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// buildGiteaDumpCmd constructs the remote shell command. Quoting matters
// here: paths may contain spaces and we cannot trust operator configuration.
func buildGiteaDumpCmd(src config.Instance, remotePath, ext string) string {
	// gitea dump requires: --file, --type, and --tempdir. --config must point
	// at the existing app.ini.
	parts := []string{
		shQuote(src.Binary), "dump",
		"--config", shQuote(src.ConfigFile),
		"--file", shQuote(remotePath),
		"--type", shQuote(ext),
		"--tempdir", shQuote(src.RemoteWorkDir),
		"--skip-log",
		"--skip-index",
	}
	cmd := strings.Join(parts, " ")
	if src.Docker != nil && src.Docker.Container != "" {
		cmd = wrapDockerCmd(src.Docker, cmd)
	} else if src.RunAs != "" {
		cmd = "sudo -u " + shQuote(src.RunAs) + " -- " + cmd
	}
	return cmd
}

// wrapDockerCmd wraps cmd in `docker exec -u USER CONTAINER sh -c 'cmd'`.
// Matches the same helper in internal/restore; kept package-local to avoid
// cycles.
func wrapDockerCmd(d *config.Docker, cmd string) string {
	prefix := shQuote(d.Binary) + " exec"
	if d.User != "" {
		prefix += " -u " + shQuote(d.User)
	}
	return prefix + " " + shQuote(d.Container) + " sh -c " + shQuote(cmd)
}

// shQuote single-quotes s for safe substitution into a remote shell command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
