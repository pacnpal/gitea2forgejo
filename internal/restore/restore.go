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

	// Populate target.docker.mounts from `docker inspect` if the config
	// didn't record any. Mirrors dump's behavior so older config.yaml
	// files keep working. All downstream host→container path translation
	// (chown, doctor --config, regenerate-hooks) depends on this list.
	if err := ensureTargetMounts(ssh, cfg, log); err != nil {
		return fmt.Errorf("discover target mounts: %w", err)
	}

	// Detect a pre-populated target DB BEFORE the expensive tar extract +
	// rsync — otherwise the operator watches 40GB move for several minutes
	// only to hit this failure at the end. pg_restore --clean only drops
	// tables that are IN the source dump; Forgejo-native tables like
	// quota_* survive and collide with forward migrations. Refuse unless
	// the operator explicitly opts into a full reset (config, or the
	// interactive prompt below on a TTY).
	state, err := InspectTargetDB(cfg)
	if err != nil {
		return fmt.Errorf("inspect target db: %w", err)
	}
	resetNeeded := false
	if !state.Empty {
		if !cfg.Options.ResetTargetDB && !confirmResetTargetDB(cfg, state) {
			return fmt.Errorf("target DB is not empty (%d tables, version=%d, forgejo_extras=%v) — "+
				"most likely Forgejo's setup wizard has been run. "+
				"Set options.reset_target_db: true to drop + recreate the schema, "+
				"or drop the database manually and re-run",
				state.TableCount, state.VersionRow, state.HasForgejoExtras)
		}
		resetNeeded = true
		log.Warn("target DB not empty; will reset after Forgejo is stopped",
			"tables", state.TableCount, "version", state.VersionRow,
			"forgejo_extras", state.HasForgejoExtras)
	}

	if err := StopService(ssh, cfg, log); err != nil {
		log.Warn("stop forgejo failed (continuing — may already be stopped)", "err", err)
	}

	if err := StageFiles(cfg, log); err != nil {
		return fmt.Errorf("stage files: %w", err)
	}
	if _, err := TranslateAppIni(cfg, log, ssh); err != nil {
		return fmt.Errorf("translate app.ini: %w", err)
	}

	if resetNeeded {
		if err := ResetTargetDB(cfg, log); err != nil {
			return fmt.Errorf("reset target db: %w", err)
		}
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
	if err := StartService(ssh, cfg, log); err != nil {
		return fmt.Errorf("start forgejo: %w", err)
	}
	// Forgejo runs forward schema migrations at startup. Give it a moment
	// before poking it with doctor.
	log.Info("waiting 10s for Forgejo to finish forward migrations")
	waitSeconds(10)

	// Re-chown inside the running container to catch anything the
	// s6-overlay init created during startup (most commonly
	// /data/gitea/log as root). doctor / regenerate-hooks run as git
	// and would otherwise hit "permission denied" on the log dir.
	if err := ChownInContainer(ssh, cfg, log); err != nil {
		log.Warn("post-start chown failed (continuing — doctor may report permission errors)", "err", err)
	}

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
