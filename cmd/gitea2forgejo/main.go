// Command gitea2forgejo migrates a Gitea ≥1.23 instance to Forgejo v15+.
//
// See the subcommand --help for details. Canonical design is in the plan at
// ~/.claude/plans/heavily-research-and-plan-cheerful-popcorn.md.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/dump"
	"github.com/pacnpal/gitea2forgejo/internal/initcmd"
	"github.com/pacnpal/gitea2forgejo/internal/preflight"
	"github.com/pacnpal/gitea2forgejo/internal/restore"
	"github.com/pacnpal/gitea2forgejo/internal/verifydump"
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
			// Emit the version banner as the very first line of stderr
			// for every subcommand. Helps users attach the right build
			// to bug reports without having to re-run with --version.
			fmt.Fprintf(os.Stderr, "gitea2forgejo %s (commit %s)\n", version, commit)

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

	root.AddCommand(newInitCmd())
	root.AddCommand(newPreflightCmd())
	root.AddCommand(newDumpCmd())
	root.AddCommand(newVerifyDumpCmd())
	root.AddCommand(newRestoreCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	return config.Load(configPath)
}

func newInitCmd() *cobra.Command {
	opt := &initcmd.Options{}
	var (
		sourceSSH string
		targetSSH string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Auto-discover source configuration and write a ready-to-run config.yaml",
		Long: `Connects to the source Gitea host via SSH, reads its app.ini,
extracts data paths + DB config + storage backend + Docker container (if any),
and emits a config.yaml with as many fields pre-filled as possible. Secrets
become env:<NAME> references; export them before running preflight.

Typical invocation:

  export SOURCE_ADMIN_TOKEN=gta_...
  export TARGET_ADMIN_TOKEN=fjo_...

  gitea2forgejo init \
    --source-url   https://gitea.example.com \
    --source-ssh   root@gitea.example.com \
    --target-url   https://forgejo.example.com \
    --target-ssh   root@forgejo.example.com \
    --ssh-key      ~/.ssh/gitea2forgejo \
    -o config.yaml

Then review config.yaml, fill in any env vars the generator flagged, and
run 'gitea2forgejo preflight --config config.yaml'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sourceSSH != "" {
				var err error
				opt.SourceSSHUser, opt.SourceSSHHost, opt.SourceSSHPort, err = parseSSHDest(sourceSSH)
				if err != nil {
					return fmt.Errorf("--source-ssh: %w", err)
				}
			}
			if targetSSH != "" {
				var err error
				opt.TargetSSHUser, opt.TargetSSHHost, opt.TargetSSHPort, err = parseSSHDest(targetSSH)
				if err != nil {
					return fmt.Errorf("--target-ssh: %w", err)
				}
			}
			// Interactive fill-in for anything missing (no-op on non-TTY).
			if err := initcmd.Interactive(opt); err != nil {
				return err
			}
			return initcmd.Run(opt, log)
		},
	}
	cmd.Flags().StringVar(&opt.SourceURL, "source-url", "", "source Gitea URL (required)")
	cmd.Flags().StringVar(&opt.SourceToken, "source-token", "", "source admin token (defaults to env:SOURCE_ADMIN_TOKEN reference)")
	cmd.Flags().StringVar(&sourceSSH, "source-ssh", "", "source SSH destination: [user@]host[:port] (required)")
	cmd.Flags().StringVar(&opt.SourceSSHKey, "source-ssh-key", "", "source SSH key (defaults to --ssh-key)")
	cmd.Flags().StringVar(&opt.SourceAppIni, "source-app-ini", "", "override app.ini path on source (default: auto-discover)")
	cmd.Flags().StringVar(&opt.SourceContainer, "source-container", "", "override Docker container name (default: auto-detect)")

	cmd.Flags().StringVar(&opt.TargetURL, "target-url", "", "target Forgejo URL (required)")
	cmd.Flags().StringVar(&opt.TargetToken, "target-token", "", "target admin token")
	cmd.Flags().StringVar(&targetSSH, "target-ssh", "", "target SSH destination: [user@]host[:port] (required)")
	cmd.Flags().StringVar(&opt.TargetSSHKey, "target-ssh-key", "", "target SSH key (defaults to --ssh-key)")
	cmd.Flags().StringVar(&opt.TargetAppIni, "target-app-ini", "", "override app.ini path on target")
	cmd.Flags().StringVar(&opt.TargetContainer, "target-container", "", "override Docker container name on target")

	var sharedKey string
	cmd.Flags().StringVar(&sharedKey, "ssh-key", "", "SSH private key used for both hosts (default ~/.ssh/id_ed25519)")
	cmd.Flags().StringVar(&opt.WorkDir, "work-dir", "./gitea2forgejo-work", "local scratch directory for dump artifacts")
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "config.yaml", "path to write the generated config")
	cmd.Flags().BoolVar(&opt.InsecureTLS, "insecure-tls", false, "skip TLS verification for source/target APIs")

	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if sharedKey == "" {
			sharedKey = firstExistingKey()
			// If none found, leave empty so remote.Dial falls back to
			// SSH_AUTH_SOCK (agent) — that's a common laptop setup.
		}
		if opt.SourceSSHKey == "" {
			opt.SourceSSHKey = sharedKey
		}
		if opt.TargetSSHKey == "" {
			opt.TargetSSHKey = sharedKey
		}
		return nil
	}
	return cmd
}

// firstExistingKey walks the usual private-key filenames in ~/.ssh and
// returns the first that exists. Empty string means "use the SSH agent."
//
// "gitea2forgejo" is checked FIRST because that's the name the bootstrap
// flow (see internal/initcmd.EnsureAuth) uses when it generates a key on
// the user's behalf. Without this, subsequent runs would re-trigger the
// bootstrap prompt even though a working key already exists from the
// previous run.
func firstExistingKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{
		"gitea2forgejo", // created by our own bootstrap; always check first
		"id_ed25519",
		"id_ecdsa",
		"id_rsa",
		"id_dsa",
	} {
		p := filepath.Join(home, ".ssh", name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// parseSSHDest accepts [user@]host[:port] and extracts the components.
func parseSSHDest(dest string) (user, host string, port int, err error) {
	if dest == "" {
		err = fmt.Errorf("empty destination")
		return
	}
	port = 22
	if i := strings.Index(dest, "@"); i > 0 {
		user = dest[:i]
		dest = dest[i+1:]
	}
	host = dest
	if i := strings.LastIndex(dest, ":"); i > 0 {
		p := dest[i+1:]
		var n int
		n, err = strconv.Atoi(p)
		if err != nil {
			err = fmt.Errorf("bad port %q", p)
			return
		}
		host = dest[:i]
		port = n
	}
	return
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

			// If we're at a TTY and detected fixable warnings, offer
			// to update config.yaml interactively and re-run.
			if preflight.OfferRemediationsFromResult(cfg, result, configPath, log) {
				log.Info("preflight: remediation applied, re-running checks")
				if cfg2, err := loadConfig(); err == nil {
					cfg = cfg2
				}
				result = preflight.Run(cfg, log)
			}

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

func newVerifyDumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify-dump",
		Short: "Sanity-check the dump artifacts in work_dir before restore",
		Long: `Read-only checks of the dump produced by 'gitea2forgejo dump':

  * source-manifest.json is parseable and non-empty
  * gitea-dump.tar.zst (or .tar.gz/.tar/.zip) is listable
  * tarball contains the expected top-level members
    (app.ini, custom/, data/, repos/, gitea-db.sql)
  * per-owner repo count in the tarball matches manifest.Repos
  * native DB dump exists and passes a format smoke test
    (pg_restore --list / SQLite magic / mysqldump header)
  * s3/ mirror directory present when S3 storage is configured

Emits work_dir/verify-dump-report.md with a go/no-go decision.
Exit non-zero when any check is FAIL so this can gate a CI pipeline.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log.Info("verify-dump: starting", "work_dir", cfg.WorkDir)
			res := verifydump.Run(cfg, log)
			path, err := res.WriteReport(cfg.WorkDir)
			if err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			for _, c := range res.Checks {
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
			if res.HardFails > 0 {
				return fmt.Errorf("verify-dump: %d hard fails", res.HardFails)
			}
			return nil
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
