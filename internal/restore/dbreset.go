package restore

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// TargetDBState reports what's currently in the target DB.
type TargetDBState struct {
	Empty      bool
	TableCount int
	// VersionRow is the value of the `version` row if the xorm_version /
	// version table is present. Zero otherwise.
	VersionRow int
	// HasForgejoExtras is true when Forgejo-only tables (e.g. quota_*) are
	// present — a strong signal the setup wizard has run.
	HasForgejoExtras bool
}

// InspectTargetDB returns a description of the current target DB state
// without modifying it. Used by both preflight and restore.
func InspectTargetDB(cfg *config.Config) (*TargetDBState, error) {
	db, err := remote.OpenDB(cfg.Target.DB)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return inspectDB(db, cfg.Target.DB.Dialect)
}

func inspectDB(db *sql.DB, dialect string) (*TargetDBState, error) {
	var countQuery string
	switch dialect {
	case "postgres":
		countQuery = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`
	case "mysql":
		countQuery = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()`
	case "sqlite3":
		countQuery = `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`
	default:
		return nil, fmt.Errorf("unsupported dialect %q", dialect)
	}
	state := &TargetDBState{}
	if err := db.QueryRow(countQuery).Scan(&state.TableCount); err != nil {
		return nil, fmt.Errorf("count target tables: %w", err)
	}
	state.Empty = state.TableCount == 0
	if state.Empty {
		return state, nil
	}
	// Best-effort: read version row.
	if err := db.QueryRow(`SELECT version FROM version WHERE id=1`).Scan(&state.VersionRow); err != nil {
		state.VersionRow = 0
	}
	// Best-effort: detect Forgejo-only tables. Any hit implies the setup
	// wizard (or a prior Forgejo run) has populated the DB.
	forgejoOnly := []string{"quota", "quota_group", "quota_group_mapping", "quota_rule"}
	for _, t := range forgejoOnly {
		var ok int
		var q string
		switch dialect {
		case "postgres":
			q = `SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1`
		case "mysql":
			q = `SELECT 1 FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`
		case "sqlite3":
			q = `SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`
		}
		if err := db.QueryRow(q, t).Scan(&ok); err == nil {
			state.HasForgejoExtras = true
			break
		}
	}
	return state, nil
}

// ResetTargetDB drops every object in the target DB so a fresh restore can
// proceed. DESTRUCTIVE — caller must confirm `options.reset_target_db` is
// set before invoking.
//
//   - postgres: DROP SCHEMA public CASCADE; CREATE SCHEMA public;
//   - mysql/mariadb: DROP DATABASE <db>; CREATE DATABASE <db>;
//   - sqlite3: unlink the DB file
func ResetTargetDB(cfg *config.Config, log *slog.Logger) error {
	d := cfg.Target.DB
	log.Warn("resetting target database (DESTRUCTIVE)", "dialect", d.Dialect)
	switch d.Dialect {
	case "postgres":
		return resetPostgres(d, log)
	case "mysql":
		return resetMySQL(d, log)
	case "sqlite3":
		return resetSQLite(cfg, log)
	default:
		return fmt.Errorf("unsupported dialect %q", d.Dialect)
	}
}

func resetPostgres(d config.DB, log *slog.Logger) error {
	db, err := remote.OpenDB(d)
	if err != nil {
		return err
	}
	defer db.Close()
	stmts := []string{
		`DROP SCHEMA IF EXISTS public CASCADE`,
		`CREATE SCHEMA public`,
		`GRANT ALL ON SCHEMA public TO public`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("reset postgres (%s): %w", s, err)
		}
		log.Info("postgres reset", "stmt", s)
	}
	return nil
}

func resetMySQL(d config.DB, log *slog.Logger) error {
	// Parse DSN to get DB name (can't call DROP DATABASE on yourself).
	_, _, _, _, dbName, err := parseMySQLDSNImpl(d.DSN)
	if err != nil {
		return err
	}
	if dbName == "" {
		return fmt.Errorf("no dbname in target mysql DSN")
	}
	// Connect with no default database by swapping the /dbname out.
	noDB := strings.Replace(d.DSN, "/"+dbName, "/", 1)
	db, err := remote.OpenDB(config.DB{Dialect: "mysql", DSN: noDB})
	if err != nil {
		return fmt.Errorf("open mysql (no db): %w", err)
	}
	defer db.Close()
	// Quote the identifier with backticks; reject obviously invalid names.
	if strings.ContainsAny(dbName, "`\"' ;") {
		return fmt.Errorf("refusing to drop suspicious db name %q", dbName)
	}
	for _, s := range []string{
		"DROP DATABASE IF EXISTS `" + dbName + "`",
		"CREATE DATABASE `" + dbName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin",
	} {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("reset mysql (%s): %w", s, err)
		}
		log.Info("mysql reset", "stmt", s)
	}
	return nil
}

func resetSQLite(cfg *config.Config, log *slog.Logger) error {
	path := sqliteDSNPathImpl(cfg.Target.DB.DSN)
	if path == "" {
		return fmt.Errorf("empty sqlite DSN")
	}
	// Remove the DB file (including WAL/SHM sidecars).
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		p := path + suffix
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		log.Info("sqlite reset", "removed", p)
	}
	return nil
}
