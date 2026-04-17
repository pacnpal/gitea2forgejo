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

// GiteaDump runs `gitea dump` on the source host via SSH, then SFTP-downloads
// the resulting tarball into workDir/gitea-dump.<ext>. Returns the local path.
//
// Large instances can take hours; progress is streamed to the logger as the
// remote command produces output.
func GiteaDump(cfg *config.Config, log *slog.Logger) (string, error) {
	if cfg.Source.SSH == nil {
		return "", fmt.Errorf("gitea dump requires source.ssh block (filesystem access)")
	}
	cli, err := remote.Dial(cfg.Source.SSH)
	if err != nil {
		return "", err
	}
	defer cli.Close()

	// Prepare remote scratch dir.
	remoteDir := cfg.Source.RemoteWorkDir
	if _, err := cli.Run("mkdir -p " + shQuote(remoteDir)); err != nil {
		return "", fmt.Errorf("mkdir remote %s: %w", remoteDir, err)
	}
	// chown to RunAs so the gitea user can write to it.
	if cfg.Source.RunAs != "" {
		if _, err := cli.Run("chown " + shQuote(cfg.Source.RunAs) + " " + shQuote(remoteDir)); err != nil {
			log.Warn("chown remote work dir (continuing)", "err", err)
		}
	}

	ext := cfg.Options.DumpFormat // tar.zst | tar.gz | tar | zip
	remotePath := path.Join(remoteDir, "gitea-dump."+ext)

	cmd := buildGiteaDumpCmd(cfg.Source, remotePath, ext)
	log.Info("gitea dump: starting", "host", cfg.Source.SSH.Host, "cmd", cmd)
	start := time.Now()
	if err := cli.RunStream(cmd, &logWriter{log: log, prefix: "gitea-dump"}); err != nil {
		return "", fmt.Errorf("gitea dump failed: %w", err)
	}
	log.Info("gitea dump: done", "elapsed", time.Since(start).Round(time.Second))

	// Verify file exists and has non-trivial size.
	stat, err := cli.Run("stat -c '%s' " + shQuote(remotePath))
	if err != nil {
		return "", fmt.Errorf("stat remote dump: %w", err)
	}
	log.Info("gitea dump: remote file", "path", remotePath, "size_bytes", string(bytes.TrimSpace(stat)))

	// Fetch.
	localPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	log.Info("gitea dump: fetching", "from", remotePath, "to", localPath)
	fetchStart := time.Now()
	if err := cli.FetchFile(remotePath, localPath); err != nil {
		return "", fmt.Errorf("fetch dump: %w", err)
	}
	if fi, err := os.Stat(localPath); err == nil {
		log.Info("gitea dump: fetched",
			"size_bytes", fi.Size(),
			"elapsed", time.Since(fetchStart).Round(time.Second))
	}

	// Best-effort cleanup of remote tarball.
	if _, err := cli.Run("rm -f " + shQuote(remotePath)); err != nil {
		log.Warn("remote cleanup failed (not fatal)", "err", err)
	}

	return localPath, nil
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
	if src.RunAs != "" {
		cmd = "sudo -u " + shQuote(src.RunAs) + " -- " + cmd
	}
	return cmd
}

// shQuote single-quotes s for safe substitution into a remote shell command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
