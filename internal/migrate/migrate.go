// Package migrate is the top-level orchestrator that runs the whole
// Gitea → Forgejo migration end-to-end. It's a thin wrapper around the
// existing preflight / dump / verify-dump / restore stages, with
// operator-friendly progress banners and between-stage hints.
package migrate

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/dump"
	"github.com/pacnpal/gitea2forgejo/internal/preflight"
	"github.com/pacnpal/gitea2forgejo/internal/restore"
	"github.com/pacnpal/gitea2forgejo/internal/verifydump"
)

// Options controls which stages run. Default (all false) = the full
// preflight → dump → verify-dump → restore sequence.
type Options struct {
	SkipPreflight bool
	SkipDump      bool
	SkipVerify    bool
	SkipRestore   bool
}

// Run executes the enabled stages in order. Stops at the first hard
// failure. Non-fatal WARN checks from preflight/verify-dump pass.
func Run(cfg *config.Config, opt Options, log *slog.Logger) error {
	log.Info("migrate: starting full migration", "source", cfg.Source.URL, "target", cfg.Target.URL)
	banner("STAGE 1 / 4 — PREFLIGHT")

	if opt.SkipPreflight {
		log.Info("preflight: skipped")
	} else {
		res := preflight.Run(cfg, log)
		if _, err := res.WriteReport(cfg.WorkDir); err != nil {
			return fmt.Errorf("preflight report: %w", err)
		}
		for _, c := range res.Checks {
			logCheck(log, c.Name, c.Status, c.Detail)
		}
		if res.HardFails > 0 {
			return fmt.Errorf("preflight: %d hard fails — fix and re-run (or use --skip-preflight if you've already addressed them)", res.HardFails)
		}
		log.Info("preflight: OK")
	}

	banner("STAGE 2 / 4 — DUMP")
	if opt.SkipDump {
		log.Info("dump: skipped")
	} else {
		if err := dump.Run(cfg, log); err != nil {
			return fmt.Errorf("dump: %w", err)
		}
		log.Info("dump: OK")
	}

	banner("STAGE 3 / 4 — VERIFY DUMP")
	if opt.SkipVerify {
		log.Info("verify-dump: skipped")
	} else {
		res := verifydump.Run(cfg, log)
		if _, err := res.WriteReport(cfg.WorkDir); err != nil {
			return fmt.Errorf("verify-dump report: %w", err)
		}
		for _, c := range res.Checks {
			logCheck(log, c.Name, c.Status, c.Detail)
		}
		if res.HardFails > 0 {
			return fmt.Errorf("verify-dump: %d hard fails — the dump is not safe to restore", res.HardFails)
		}
		log.Info("verify-dump: OK")
	}

	banner("STAGE 4 / 4 — RESTORE")
	if opt.SkipRestore {
		log.Info("restore: skipped")
	} else {
		if err := restore.Run(cfg, log); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		log.Info("restore: OK")
	}

	banner("MIGRATION COMPLETE")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Post-migration checklist:")
	fmt.Fprintln(os.Stderr, "  1. Smoke-test target Forgejo in a browser (log in, open a repo, fire a webhook).")
	fmt.Fprintln(os.Stderr, "  2. Re-register Actions runners on the new hostname (tokens are hostname-scoped).")
	fmt.Fprintln(os.Stderr, "  3. Announce to users: re-login, regenerate PATs, verify 2FA.")
	fmt.Fprintln(os.Stderr, "  4. When satisfied, reclaim space:")
	fmt.Fprintln(os.Stderr, "       gitea2forgejo cleanup --config "+escapeQ(os.Getenv("CONFIG_PATH_HINT")))
	fmt.Fprintln(os.Stderr, "  5. Update DNS / reverse proxy to point at the target.")
	fmt.Fprintln(os.Stderr)
	return nil
}

// banner prints a highly visible stage separator so `migrate` output
// stays readable even with multi-megabyte log streams.
func banner(s string) {
	line := strings.Repeat("━", 60)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, line)
	fmt.Fprintln(os.Stderr, "  "+s)
	fmt.Fprintln(os.Stderr, line)
}

func logCheck(log *slog.Logger, name, status, detail string) {
	switch status {
	case "FAIL":
		log.Error("check FAIL", "name", name, "detail", detail)
	case "WARN":
		log.Warn("check WARN", "name", name, "detail", detail)
	default:
		log.Info("check PASS", "name", name, "detail", detail)
	}
}

func escapeQ(s string) string {
	if s == "" {
		return "config.yaml"
	}
	return s
}
