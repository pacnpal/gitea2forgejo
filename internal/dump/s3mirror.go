package dump

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// S3Mirror copies the source bucket tree into workDir/s3/ using the MinIO
// client (`mc`). It is a no-op if Source.Storage is nil or not of type s3.
//
// Returns the local path where the mirror landed, or "" if skipped.
func S3Mirror(cfg *config.Config, log *slog.Logger) (string, error) {
	s := cfg.Source.Storage
	if s == nil || s.Type != "s3" {
		log.Info("s3 mirror: skipped (no s3 storage configured)")
		return "", nil
	}
	if _, err := exec.LookPath("mc"); err != nil {
		return "", fmt.Errorf("mc (MinIO client) not found in PATH — install from https://min.io/docs/minio/linux/reference/minio-mc.html: %w", err)
	}
	if s.Endpoint == "" || s.Bucket == "" || s.AccessKey == "" || s.SecretKey == "" {
		return "", fmt.Errorf("s3 storage config incomplete: endpoint/bucket/access_key/secret_key all required")
	}

	localRoot := filepath.Join(cfg.WorkDir, "s3")
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		return "", err
	}

	// Use a tool-specific alias name so we don't clobber the operator's
	// existing `mc` config.
	alias := "gitea2forgejo-src"
	if err := mcAliasSet(alias, s, log); err != nil {
		return "", err
	}
	defer mcAliasRemove(alias, log)

	target := alias + "/" + s.Bucket
	log.Info("mc mirror: starting", "from", target, "to", localRoot)
	start := time.Now()
	cmd := exec.Command("mc", "mirror", "--overwrite", "--preserve", target, localRoot)
	cmd.Stdout = &logWriter{log: log, prefix: "mc"}
	cmd.Stderr = &logWriter{log: log, prefix: "mc"}
	if err := cmd.Run(); err != nil {
		return localRoot, fmt.Errorf("mc mirror: %w", err)
	}
	log.Info("mc mirror: done", "elapsed", time.Since(start).Round(time.Second))
	return localRoot, nil
}

func mcAliasSet(name string, s *config.Storage, log *slog.Logger) error {
	args := []string{"alias", "set", name, s.Endpoint, s.AccessKey, s.SecretKey}
	if s.Region != "" {
		args = append(args, "--api", "s3v4")
	}
	cmd := exec.Command("mc", args...)
	cmd.Stdout = &logWriter{log: log, prefix: "mc-alias"}
	cmd.Stderr = &logWriter{log: log, prefix: "mc-alias"}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mc alias set: %w", err)
	}
	return nil
}

func mcAliasRemove(name string, log *slog.Logger) {
	cmd := exec.Command("mc", "alias", "remove", name)
	if err := cmd.Run(); err != nil {
		log.Warn("mc alias remove", "alias", name, "err", err)
	}
}
