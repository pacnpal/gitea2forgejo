// Command gitea2forgejo migrates a Gitea ≥1.23 instance to Forgejo v15+.
//
// See the subcommand --help for details. Canonical design is in the plan at
// ~/.claude/plans/heavily-research-and-plan-cheerful-popcorn.md.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/dump"
	"github.com/pacnpal/gitea2forgejo/internal/preflight"
	"github.com/pacnpal/gitea2forgejo/internal/restore"
)

// Injected at build time via -ldflags -X. See .slsa-goreleaser.yml.
var (
	version = "dev"
	commit  = "none"
)

var (
	configPath string
	logLevel   string
	log        *slog.Logger
)

func main() {
	root := &cobra.Command{
		Use:     "gitea2forgejo",
		Short:   "One-time, full-fidelity migration from Gitea ≥1.23 to Forgejo v15+",
		Version: fmt.Sprintf("%s (commit %s)", version, commit),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			lvl := slog.LevelInfo
			switch logLevel {
			case "debug":
				lvl = slog.LevelDebug
			case "warn":
				lvl = slog.LevelWarn
			case "error":
				lvl = slog.LevelError
			}
			log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
			return nil
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "path to config YAML")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "debug|info|warn|error")

	root.AddCommand(newPreflightCmd())
	root.AddCommand(newDumpCmd())
	root.AddCommand(newRestoreCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	return config.Load(configPath)
}

func newPreflightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "Run read-only checks (versions, SSH, DB, disk, SECRET_KEY) and emit a go/no-go report",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log.Info("preflight: starting", "source", cfg.Source.URL, "target", cfg.Target.URL)
			result := preflight.Run(cfg, log)
			path, err := result.WriteReport(cfg.WorkDir)
			if err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			for _, c := range result.Checks {
				switch c.Status {
				case "FAIL":
					log.Error("check FAIL", "name", c.Name, "detail", c.Detail)
				case "WARN":
					log.Warn("check WARN", "name", c.Name, "detail", c.Detail)
				default:
					log.Info("check PASS", "name", c.Name, "detail", c.Detail)
				}
			}
			fmt.Fprintf(os.Stderr, "\nReport: %s\n", path)
			if result.HardFails > 0 {
				return fmt.Errorf("preflight: %d hard fails", result.HardFails)
			}
			return nil
		},
	}
}

func newDumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dump",
		Short: "Harvest source instance into a manifest and on-disk artifacts",
		Long: `Phase 1 of the migration.

Stages:
  1. API manifest harvest (users, orgs, teams, repos, packages)
  2. login_source DB read (LDAP/OAuth/SMTP definitions)
  3. gitea dump via SSH (full tarball fetched to work_dir)
  4. native DB dump (pg_dump / mysqldump / sqlite copy)
  5. S3/MinIO bucket mirror via mc (if source storage.type = s3)

Individual stages can be skipped via options.skip_* in config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log.Info("dump: starting", "source", cfg.Source.URL, "work_dir", cfg.WorkDir)
			return dump.Run(cfg, log)
		},
	}
}

func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore",
		Short: "Ingest dump artifacts into a fresh Forgejo v15 target",
		Long: `Phase 2 of the migration.

Steps:
  1. Stop forgejo service on target
  2. Extract gitea-dump.<ext> into work_dir/extracted/
  3. Rsync data/repos/custom subtrees to target
  4. Translate source app.ini → target app.ini; preserve SECRET_KEY,
     INTERNAL_TOKEN, JWT_SECRET; rewrite hostname and data paths
  5. Import DB (pg_restore / mysql / sqlite copy)
  6. UPDATE version SET version=305 (forgejo#7638 trick)
  7. Remove stale Bleve indexer files
  8. chown -R forgejo:forgejo
  9. Start forgejo service (runs forward migrations on boot)
 10. forgejo doctor check --all --fix
 11. forgejo admin regenerate hooks

The target Forgejo MUST be installed and configured with an empty DB.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log.Info("restore: starting", "target", cfg.Target.URL, "work_dir", cfg.WorkDir)
			return restore.Run(cfg, log)
		},
	}
}
