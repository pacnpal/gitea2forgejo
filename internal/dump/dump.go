package dump

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pacnpal/gitea2forgejo/internal/client"
	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// Run performs Phase 1 (dump):
//
//  1. API manifest harvest (always)
//  2. login_source DB read (if DB DSN reachable)
//  3. gitea dump shell-out on source host (unless SkipGiteaDump)
//  4. native DB dump (pg_dump/mysqldump/sqlite copy) (unless SkipNativeDB)
//  5. S3 bucket mirror via `mc` (unless SkipS3Mirror, or no s3 storage)
//
// Each stage is independent; failures are logged per-stage and the function
// returns the first hard error encountered.
func Run(cfg *config.Config, log *slog.Logger) error {
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("mkdir work_dir: %w", err)
	}

	src, err := client.New(&cfg.Source, client.KindSource)
	if err != nil {
		return err
	}

	// Stage 1: API harvest.
	m, err := Harvest(src, log)
	if err != nil {
		log.Warn("harvest finished with errors (partial manifest will still be written)", "err", err)
	}
	m.Target = cfg.Target.URL

	// Stage 2: login_source (native DB).
	if cfg.Source.DB.DSN != "" {
		if db, dbErr := remote.OpenDB(cfg.Source.DB); dbErr != nil {
			log.Warn("skipping login_source harvest: db open", "err", dbErr)
		} else {
			ls, lsErr := LoginSources(db, log)
			db.Close()
			if lsErr != nil {
				log.Warn("login_source harvest failed", "err", lsErr)
			} else {
				m.LoginSources = ls
				log.Info("harvest: login_source", "count", len(ls))
			}
		}
	}

	manifestPath := filepath.Join(cfg.WorkDir, "source-manifest.json")
	if err := m.Save(manifestPath); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}
	log.Info("manifest written", "path", manifestPath)

	// Stage 3: gitea dump.
	if cfg.Options.SkipGiteaDump {
		log.Info("gitea dump: skipped by config")
	} else {
		if _, err := GiteaDump(cfg, log); err != nil {
			return fmt.Errorf("gitea dump: %w", err)
		}
	}

	// Stage 4: native DB dump.
	if cfg.Options.SkipNativeDB {
		log.Info("native db dump: skipped by config")
	} else {
		if _, err := NativeDump(cfg, log); err != nil {
			return fmt.Errorf("native db dump: %w", err)
		}
	}

	// Stage 5: S3 mirror.
	if cfg.Options.SkipS3Mirror {
		log.Info("s3 mirror: skipped by config")
	} else {
		if _, err := S3Mirror(cfg, log); err != nil {
			return fmt.Errorf("s3 mirror: %w", err)
		}
	}

	log.Info("dump: all stages complete", "work_dir", cfg.WorkDir)
	return nil
}
