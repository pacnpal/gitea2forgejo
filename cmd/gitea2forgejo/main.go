// Command gitea2forgejo migrates a Gitea ≥1.23 instance to Forgejo v15+.
//
// See the subcommand --help for details. Canonical design is in the plan at
// ~/.claude/plans/heavily-research-and-plan-cheerful-popcorn.md.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pacnpal/gitea2forgejo/internal/cleanup"
	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/dump"
	"github.com/pacnpal/gitea2forgejo/internal/initcmd"
	"github.com/pacnpal/gitea2forgejo/internal/migrate"
	"github.com/pacnpal/gitea2forgejo/internal/preflight"
	"github.com/pacnpal/gitea2forgejo/internal/restore"
	"github.com/pacnpal/gitea2forgejo/internal/selfupdate"
	"github.com/pacnpal/gitea2forgejo/internal/verifydump"
)

// Injected at build time via -ldflags -X. See .slsa-goreleaser.yml.
var (
	version = "dev"
	commit  = "none"
)

var (
	configPath     string
	logLevel       string
	noUpdateCheck  bool
	log            *slog.Logger
)

func main() {
	root := &cobra.Command{
		Use:     "gitea2forgejo",
		Short:   "One-time, full-fidelity migration from Gitea ≥1.23 to Forgejo v15+",
		Version: fmt.Sprintf("%s (commit %s)", version, commit),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Emit the version banner as the very first line of stderr
			// for every subcommand.
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

			// Auto-check for updates. Rate-limited to once per 6 hours
			// via a timestamp file in ~/.cache/gitea2forgejo, so this
			// doesn't hit GitHub every command. Skip when:
			//   - --no-update-check was passed
			//   - running the `update` subcommand (avoid recursion)
			//   - stdin isn't a TTY (CI / scripted runs)
			if !noUpdateCheck && cmd.Name() != "update" && cmd.Name() != "completion" {
				maybePromptUpdate(cmd.Context(), cmd.Name(), args)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "path to config YAML")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "debug|info|warn|error")
	root.PersistentFlags().BoolVar(&noUpdateCheck, "no-update-check", false, "disable the once-per-6h auto-check for a newer release")

	root.AddCommand(newInitCmd())
	root.AddCommand(newPreflightCmd())
	root.AddCommand(newDumpCmd())
	root.AddCommand(newVerifyDumpCmd())
	root.AddCommand(newRestoreCmd())
	root.AddCommand(newMigrateCmd())
	root.AddCommand(newDumpAndRestoreCmd())
	root.AddCommand(newCleanupCmd())
	root.AddCommand(newUpdateCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	return config.Load(configPath)
}

func newMigrateCmd() *cobra.Command {
	opt := migrate.Options{}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "End-to-end: preflight → dump → verify-dump → restore",
		Long: "Runs the full Gitea → Forgejo migration in one invocation.\n\n" +
			"Executes in order, stopping on the first hard failure:\n\n" +
			"  1. preflight    — read-only checks (versions, SSH, DB, SECRET_KEY,\n" +
			"                    target-db-empty, disk space)\n" +
			"  2. dump         — gitea dump + native DB dump + source manifest\n" +
			"  3. verify-dump  — cross-check dump artifacts against the manifest\n" +
			"  4. restore      — import into target Forgejo + doctor + hooks\n\n" +
			"Any stage can be skipped with --skip-<stage>. Useful when iterating:\n" +
			"once preflight passes you usually don't need to re-run it, so a\n" +
			"resumption flow looks like:\n\n" +
			"  gitea2forgejo migrate --skip-preflight --skip-dump --skip-verify\n\n" +
			"After a successful run, `gitea2forgejo cleanup` reclaims disk space.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			os.Setenv("CONFIG_PATH_HINT", configPath)
			return migrate.Run(cfg, opt, log)
		},
	}
	cmd.Flags().BoolVar(&opt.SkipPreflight, "skip-preflight", false, "don't run preflight (assume already passed)")
	cmd.Flags().BoolVar(&opt.SkipDump, "skip-dump", false, "don't run dump (assume artifacts already exist)")
	cmd.Flags().BoolVar(&opt.SkipVerify, "skip-verify", false, "don't run verify-dump")
	cmd.Flags().BoolVar(&opt.SkipRestore, "skip-restore", false, "don't run restore (dry-run up to verify-dump)")
	return cmd
}

func newDumpAndRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dump-and-restore",
		Short: "Shortcut: dump → verify-dump → restore (skip preflight)",
		Long: "Runs dump, verify-dump, and restore in sequence. Use this when\n" +
			"you've already validated the environment with a prior preflight\n" +
			"and just want to execute the data-moving stages.\n\n" +
			"Equivalent to:\n" +
			"  gitea2forgejo migrate --skip-preflight",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			os.Setenv("CONFIG_PATH_HINT", configPath)
			return migrate.Run(cfg, migrate.Options{SkipPreflight: true}, log)
		},
	}
}

func newCleanupCmd() *cobra.Command {
	opt := cleanup.Options{}
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove dump artifacts from work_dir and the source host scratch",
		Long: `Reclaims disk space after a successful migration.

Removes local artifacts (gitea-dump.<ext> tarball or symlink,
source-manifest.json, native DB dump, preflight / verify reports,
extracted/, s3/ mirror), then follows any symlink in work_dir back
to the source host's scratch directory and removes that too via
docker exec (when source is containerized) or direct SSH rm (bare
metal). Prompts for confirmation unless --force.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return cleanup.Run(cfg, opt, log)
		},
	}
	cmd.Flags().BoolVar(&opt.Force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&opt.KeepLocal, "keep-local", false, "only clean up the source host scratch, leave work_dir alone")
	cmd.Flags().BoolVar(&opt.KeepRemote, "keep-remote", false, "only clean up work_dir, leave the source host scratch alone")
	return cmd
}

// hintNext prints a consistent "Next:" line so operators know the
// canonical successor command to their current one. Plain stderr
// write — avoids entanglement with structured logs.
func hintNext(cmd string) {
	fmt.Fprintf(os.Stderr, "\nNext: %s\n", cmd)
}

func newUpdateCmd() *cobra.Command {
	var force bool
	var toTag string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Self-update to the latest release on GitHub",
		Long: `Queries https://api.github.com/repos/` + selfupdate.Repo + `/releases/latest,
compares to the running build, downloads the matching binary for the
current OS/arch, and atomically replaces the running executable.

Sends Cache-Control: no-cache so CDN edges don't serve a stale "latest"
right after a new release publishes, and retries once on 5xx / network
errors. If GitHub's /releases/latest still appears stuck, use --to
<tag> to pin a specific release directly (bypasses /releases/latest):

    gitea2forgejo update --to v0.2.15

Requires write access to the installed location (e.g. /usr/local/bin).
If permission is denied, re-run with sudo.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var release *selfupdate.Release
			var err error
			if toTag != "" {
				release, err = selfupdate.ByTag(cmd.Context(), toTag)
				if err != nil {
					return fmt.Errorf("query release %q: %w", toTag, err)
				}
			} else {
				release, err = selfupdate.Latest(cmd.Context())
				if err != nil {
					return fmt.Errorf("query latest release: %w", err)
				}
			}
			newer, err := selfupdate.IsNewer(version, release.TagName)
			if err != nil {
				return err
			}
			// --to is an explicit operator choice; allow downgrade /
			// reinstall without requiring --force.
			if !newer && !force && toTag == "" {
				fmt.Fprintf(os.Stderr, "already up to date (running %s, latest %s)\n", version, release.TagName)
				return nil
			}
			fmt.Fprintf(os.Stderr, "updating %s → %s\n", version, release.TagName)
			if err := selfupdate.Apply(cmd.Context(), release.TagName,
				func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }); err != nil {
				return fmt.Errorf("apply: %w", err)
			}
			selfupdate.RecordCheck()
			fmt.Fprintf(os.Stderr, "updated. new version: ")
			return runSelfOnce("--version")
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "download + install even if already at the latest version")
	cmd.Flags().StringVar(&toTag, "to", "", "update to a specific release tag (e.g. v0.2.15) instead of latest; allows downgrade")
	return cmd
}

// runSelfOnce execs the just-installed binary with one arg so the operator
// sees the new version string in the same output stream.
func runSelfOnce(arg string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, arg)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

// maybePromptUpdate performs the once-per-CheckTTL auto-check. If a
// newer release is available AND stdin is a TTY, prompt the operator to
// upgrade inline (downloads + replaces + re-execs the original command).
// Any failure is logged and ignored — update checks must never block a
// legitimate command.
func maybePromptUpdate(ctx context.Context, cmdName string, cmdArgs []string) {
	if !selfupdate.ShouldCheck() {
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := selfupdate.Latest(ctx)
	selfupdate.RecordCheck() // record even on failure; don't retry for 6h
	if err != nil {
		return
	}
	newer, err := selfupdate.IsNewer(version, release.TagName)
	if err != nil || !newer {
		return
	}
	fmt.Fprintf(os.Stderr, "\n━━ a newer gitea2forgejo is available ━━\n"+
		"   running:  %s\n"+
		"   latest:   %s (%s)\n"+
		"Update now? [Y/n]: ", version, release.TagName, release.HTMLURL)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr)
		return
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "n" || ans == "no" {
		fmt.Fprintln(os.Stderr, "  skipping update")
		return
	}
	if err := selfupdate.Apply(ctx, release.TagName,
		func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }); err != nil {
		fmt.Fprintf(os.Stderr, "  update failed: %v\n  continuing with old version\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "  updated to %s — re-executing your command\n", release.TagName)
	// syscall.Exec replaces this process with the new binary, preserving
	// the current CLI args. Works on Linux + macOS (the Apply() function
	// already refuses on Windows).
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  can't locate updated binary: %v\n  please re-run your command\n", err)
		os.Exit(0)
	}
	if err := syscallExec(self, os.Args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "  re-exec failed: %v\n  please re-run your command\n", err)
		os.Exit(0)
	}
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
			hintNext("gitea2forgejo dump --config " + configPath)
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
			if err := dump.Run(cfg, log); err != nil {
				return err
			}
			hintNext("gitea2forgejo verify-dump --config " + configPath)
			return nil
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
			hintNext("gitea2forgejo restore --config " + configPath)
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
			if err := restore.Run(cfg, log); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "Post-restore checklist:")
			fmt.Fprintln(os.Stderr, "  1. Smoke-test the target Forgejo in a browser")
			fmt.Fprintln(os.Stderr, "  2. Re-register Actions runners (tokens are hostname-scoped)")
			fmt.Fprintln(os.Stderr, "  3. Announce to users: re-login, regenerate PATs, verify 2FA")
			fmt.Fprintln(os.Stderr, "  4. Update DNS / reverse proxy when ready")
			fmt.Fprintln(os.Stderr)
			hintNext("gitea2forgejo cleanup --config " + configPath + "   # after confirming migration")
			return nil
		},
	}
}
