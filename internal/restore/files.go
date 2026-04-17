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

// RsyncToTarget copies a local directory tree up to the target host via SSH.
// `remoteDst` is the absolute path on the target.
//
// It shells out to rsync because rsync handles large trees with hardlinks,
// sparse files, and transient failures far better than any Go-native copy.
func RsyncToTarget(cfg *config.Config, localSrc, remoteDst string, log *slog.Logger) error {
	if _, err := os.Stat(localSrc); err != nil {
		log.Info("rsync: source missing, skipping", "src", localSrc)
		return nil
	}
	if cfg.Target.SSH == nil {
		return fmt.Errorf("rsync requires target.ssh block")
	}
	rspec := fmt.Sprintf("%s@%s:%s", cfg.Target.SSH.User, cfg.Target.SSH.Host, remoteDst)
	sshCmd := fmt.Sprintf("ssh -p %d -i %s -o StrictHostKeyChecking=no",
		cfg.Target.SSH.Port, cfg.Target.SSH.Key)
	args := []string{
		"-aHAX",           // archive + hardlinks + ACLs + xattrs
		"--delete-after",  // mirror (remove target-only files after transfer)
		"--numeric-ids",
		"--info=progress2",
		"-e", sshCmd,
		ensureTrailingSlash(localSrc),
		rspec,
	}
	cmd := exec.Command("rsync", args...)
	log.Info("rsync: starting", "src", localSrc, "dst", rspec)
	start := time.Now()
	if err := runCmd(cmd, log, "rsync"); err != nil {
		return fmt.Errorf("rsync %s -> %s: %w", localSrc, rspec, err)
	}
	log.Info("rsync: done", "elapsed", time.Since(start).Round(time.Second))
	return nil
}

// StageFiles extracts the dump and rsyncs its data/repos/custom subtrees to
// the target host, preserving Git hooks and OIDC JWT signing keys.
func StageFiles(cfg *config.Config, log *slog.Logger) error {
	extracted, err := ExtractDump(cfg, log)
	if err != nil {
		return err
	}
	// Typical layout produced by `gitea dump`:
	//   extracted/
	//     app.ini              (may be present; we translate separately)
	//     custom/
	//     data/
	//     repos/
	//     gitea-db.sql         (xorm SQL — unused; we restore the native dump instead)
	//     log/                 (skipped)
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

// StopService and StartService operate the Forgejo systemd unit on the target.
func StopService(ssh *remote.Client, log *slog.Logger) error {
	log.Info("target: stopping forgejo service")
	out, err := ssh.Run("systemctl stop forgejo")
	if err != nil {
		return fmt.Errorf("systemctl stop: %w (%s)", err, string(out))
	}
	return nil
}

func StartService(ssh *remote.Client, log *slog.Logger) error {
	log.Info("target: starting forgejo service")
	out, err := ssh.Run("systemctl start forgejo")
	if err != nil {
		return fmt.Errorf("systemctl start: %w (%s)", err, string(out))
	}
	return nil
}

// Chown flips ownership of the target data tree to the forgejo user.
func Chown(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
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
		log.Info("chown", "dir", dir, "owner", spec)
		out, err := ssh.Run(cmd)
		if err != nil {
			return fmt.Errorf("chown %s: %w (%s)", dir, err, string(out))
		}
	}
	return nil
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
