package restore

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// SchemaTrick applies the forgejo#7638 migration workaround: force the
// `version` row to a value Forgejo v15 knows how to migrate forward from.
//
// The exact value (305) is what forgejo#7638 documents; this function accepts
// an override via cfg.Options (future work) but defaults to 305.
//
// Must be run after DBImport, before Forgejo is started.
func SchemaTrick(cfg *config.Config, log *slog.Logger) error {
	const targetVersion = 305
	db, err := remote.OpenDB(cfg.Target.DB)
	if err != nil {
		return fmt.Errorf("open target db: %w", err)
	}
	defer db.Close()

	var current int
	if err := db.QueryRow(`SELECT version FROM "version" WHERE id=1`).Scan(&current); err != nil {
		// Postgres quotes; try unquoted for mysql/sqlite.
		if err2 := db.QueryRow(`SELECT version FROM version WHERE id=1`).Scan(&current); err2 != nil {
			return fmt.Errorf("read current version: %w (fallback: %v)", err, err2)
		}
	}
	log.Info("schema trick: current version in DB", "current", current, "target", targetVersion)
	if current == targetVersion {
		log.Info("schema trick: already at target version, no-op")
		return nil
	}
	if _, err := db.Exec(`UPDATE version SET version = $1 WHERE id = 1`, targetVersion); err != nil {
		// Retry with unquoted-placeholder dialect.
		if _, err2 := db.Exec(`UPDATE version SET version = ? WHERE id = 1`, targetVersion); err2 != nil {
			return fmt.Errorf("update version: %w (fallback: %v)", err, err2)
		}
	}
	log.Info("schema trick: version reset", "from", current, "to", targetVersion)
	return nil
}

// WipeBleve removes stale Bleve indexer files on the target. They are not
// portable across major Forgejo versions; Forgejo will regenerate them on
// first boot.
func WipeBleve(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	idx := cfg.Target.DataDir + "/indexers"
	log.Info("wiping bleve indexers", "path", idx)
	out, err := ssh.Run(fmt.Sprintf("rm -rf %s", shQuote(idx)))
	if err != nil {
		return fmt.Errorf("wipe bleve: %w (%s)", err, string(out))
	}
	return nil
}

// dbHasVersionTable returns true if the target DB has the `version` table
// populated. Callers can use this to skip SchemaTrick when restoring into
// a DB that was never a Gitea DB (i.e. the import didn't happen).
func dbHasVersionTable(db *sql.DB) bool {
	var one int
	err := db.QueryRow(`SELECT 1 FROM version WHERE id=1`).Scan(&one)
	return err == nil
}
