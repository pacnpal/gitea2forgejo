package restore

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// Doctor runs `forgejo doctor check --all --fix` on the target host.
//
// Output is streamed to the logger. A non-zero exit does not automatically
// abort the migration because doctor --fix can emit warnings for things the
// operator may accept; we surface the exit status and let the caller decide.
func Doctor(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	binary := cfg.Target.Binary
	if binary == "" {
		binary = "forgejo"
	}
	logPath := "/tmp/gitea2forgejo-doctor.log"
	parts := []string{
		shQuote(binary), "doctor", "check", "--all", "--fix",
		"--config", shQuote(targetConfigPath(cfg)),
		"--log-file", shQuote(logPath),
	}
	cmd := strings.Join(parts, " ")
	if cfg.Target.Docker != nil && cfg.Target.Docker.Container != "" {
		cmd = wrapDockerCmd(cfg.Target.Docker, cmd)
	} else if cfg.Target.RunAs != "" {
		cmd = "sudo -u " + shQuote(cfg.Target.RunAs) + " -- " + cmd
	}
	log.Info("doctor: starting", "host", cfg.Target.SSH.Host, "cmd", cmd)
	start := time.Now()
	err := ssh.RunStream(cmd, &sshStreamer{log: log, prefix: "doctor"})
	log.Info("doctor: done", "elapsed", time.Since(start).Round(time.Second), "log_path_remote", logPath)
	if err != nil {
		log.Warn("doctor reported issues (check remote log)", "err", err)
	}
	return err
}

// RegenerateHooks runs `forgejo admin regenerate hooks`.
func RegenerateHooks(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	binary := cfg.Target.Binary
	if binary == "" {
		binary = "forgejo"
	}
	parts := []string{
		shQuote(binary), "admin", "regenerate", "hooks",
		"--config", shQuote(targetConfigPath(cfg)),
	}
	cmd := strings.Join(parts, " ")
	if cfg.Target.Docker != nil && cfg.Target.Docker.Container != "" {
		cmd = wrapDockerCmd(cfg.Target.Docker, cmd)
	} else if cfg.Target.RunAs != "" {
		cmd = "sudo -u " + shQuote(cfg.Target.RunAs) + " -- " + cmd
	}
	log.Info("regenerate hooks: starting")
	out, err := ssh.Run(cmd)
	if err != nil {
		return fmt.Errorf("regenerate hooks: %w (%s)", err, string(out))
	}
	log.Info("regenerate hooks: done")
	return nil
}

// targetConfigPath returns the path to pass to Forgejo as --config.
// For Docker targets it translates cfg.Target.ConfigFile host→container
// via the recorded mounts; when translation fails (operator wrote a
// container-internal path directly, or the file isn't under any bind
// mount) it returns the original path verbatim so the subsequent
// forgejo process surfaces the real error.
func targetConfigPath(cfg *config.Config) string {
	if cfg.Target.Docker == nil || cfg.Target.Docker.Container == "" {
		return cfg.Target.ConfigFile
	}
	if cc := cfg.Target.Docker.HostToContainer(cfg.Target.ConfigFile); cc != "" {
		return cc
	}
	return cfg.Target.ConfigFile
}

// sshStreamer wraps slog for RunStream output.
type sshStreamer struct {
	log    *slog.Logger
	prefix string
	buf    []byte
}

func (s *sshStreamer) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := indexByte(s.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := s.buf[:i]
		s.buf = s.buf[i+1:]
		s.log.Info(s.prefix, "msg", strings.TrimRight(string(line), "\r"))
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
