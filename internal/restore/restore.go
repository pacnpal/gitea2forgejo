package restore

import (
	"fmt"
	"log/slog"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// Run performs Phase 2 end-to-end:
//
//  1. Stop Forgejo on target (systemctl stop)
//  2. Extract dump tarball locally
//  3. Rsync data/repos/custom to target
//  4. Translate + upload app.ini
//  5. Import DB (pg_restore / mysql / sqlite copy)
//  6. Schema trick (UPDATE version=305)
//  7. Wipe stale Bleve indexers
//  8. chown -R forgejo:forgejo
//  9. Start Forgejo (schema forward-migrates on boot)
//  10. Run `forgejo doctor check --all --fix`
//  11. Run `forgejo admin regenerate hooks`
//
// Each step logs its own progress; failures abort the run (the target
// Forgejo will remain stopped so the operator can inspect state).
func Run(cfg *config.Config, log *slog.Logger) error {
	if cfg.Target.SSH == nil {
		return fmt.Errorf("restore requires target.ssh block")
	}
	ssh, err := remote.Dial(cfg.Target.SSH)
	if err != nil {
		return fmt.Errorf("ssh target: %w", err)
	}
	defer ssh.Close()

	if err := StopService(ssh, log); err != nil {
		log.Warn("stop forgejo failed (continuing — may already be stopped)", "err", err)
	}

	if err := StageFiles(cfg, log); err != nil {
		return fmt.Errorf("stage files: %w", err)
	}
	if _, err := TranslateAppIni(cfg, log, ssh); err != nil {
		return fmt.Errorf("translate app.ini: %w", err)
	}
	if err := DBImport(cfg, log); err != nil {
		return fmt.Errorf("db import: %w", err)
	}
	if err := SchemaTrick(cfg, log); err != nil {
		return fmt.Errorf("schema trick: %w", err)
	}
	if err := WipeBleve(ssh, cfg, log); err != nil {
		return fmt.Errorf("wipe bleve: %w", err)
	}
	if err := Chown(ssh, cfg, log); err != nil {
		return fmt.Errorf("chown: %w", err)
	}
	if err := StartService(ssh, log); err != nil {
		return fmt.Errorf("start forgejo: %w", err)
	}
	// Forgejo runs forward schema migrations at startup. Give it a moment
	// before poking it with doctor.
	log.Info("waiting 10s for Forgejo to finish forward migrations")
	waitSeconds(10)

	if err := Doctor(ssh, cfg, log); err != nil {
		log.Warn("doctor reported issues", "err", err)
	}
	if err := RegenerateHooks(ssh, cfg, log); err != nil {
		log.Warn("regenerate hooks failed (non-fatal)", "err", err)
	}

	log.Info("restore: complete", "target", cfg.Target.URL)
	return nil
}

func waitSeconds(n int) {
	timeSleep(n)
}
