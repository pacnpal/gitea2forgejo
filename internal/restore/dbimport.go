package restore

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// DBImport restores the native dump (produced by dump.NativeDump) into the
// target database. The target DB is expected to be empty.
func DBImport(cfg *config.Config, log *slog.Logger) error {
	switch cfg.Target.DB.Dialect {
	case "postgres":
		return importPostgres(cfg, log)
	case "mysql":
		return importMySQL(cfg, log)
	case "sqlite3":
		return importSQLite(cfg, log)
	default:
		return fmt.Errorf("unsupported target db dialect %q", cfg.Target.DB.Dialect)
	}
}

func importPostgres(cfg *config.Config, log *slog.Logger) error {
	dumpPath := filepath.Join(cfg.WorkDir, "gitea.dump")
	if _, err := os.Stat(dumpPath); err != nil {
		return fmt.Errorf("pg dump file not found at %s: %w", dumpPath, err)
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		return fmt.Errorf("pg_restore not in PATH: %w", err)
	}
	cmd := exec.Command("pg_restore",
		"--no-owner", "--no-acl",
		"--clean", "--if-exists",
		"-d", cfg.Target.DB.DSN,
		dumpPath,
	)
	log.Info("pg_restore: starting", "dump", dumpPath)
	start := time.Now()
	if err := runCmd(cmd, log, "pg_restore"); err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}
	log.Info("pg_restore: done", "elapsed", time.Since(start).Round(time.Second))
	return nil
}

func importMySQL(cfg *config.Config, log *slog.Logger) error {
	sqlPath := filepath.Join(cfg.WorkDir, "gitea.sql")
	if _, err := os.Stat(sqlPath); err != nil {
		return fmt.Errorf("mysql dump file not found at %s: %w", sqlPath, err)
	}
	if _, err := exec.LookPath("mysql"); err != nil {
		return fmt.Errorf("mysql client not in PATH: %w", err)
	}
	// Parse target DSN to extract connection params.
	// (Re-uses the helper from internal/dump; importing this path would
	// create a cycle, so we duplicate the small parser here. Fine for now.)
	host, port, user, pass, db, err := parseMySQLDSN(cfg.Target.DB.DSN)
	if err != nil {
		return fmt.Errorf("parse target mysql DSN: %w", err)
	}
	in, err := os.Open(sqlPath)
	if err != nil {
		return err
	}
	defer in.Close()
	cmd := exec.Command("mysql",
		"-h", host, "-P", port, "-u", user, db,
	)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pass)
	cmd.Stdin = in
	log.Info("mysql import: starting", "sql", sqlPath)
	start := time.Now()
	if err := runCmd(cmd, log, "mysql"); err != nil {
		return fmt.Errorf("mysql import: %w", err)
	}
	log.Info("mysql import: done", "elapsed", time.Since(start).Round(time.Second))
	return nil
}

func importSQLite(cfg *config.Config, log *slog.Logger) error {
	// For sqlite, the "import" is simply placing the .sqlite file at the
	// path the target app.ini references (or the path in the target DSN).
	// We resolve the target path from the DSN and copy in place.
	dumpPath := filepath.Join(cfg.WorkDir, "gitea.sqlite")
	if _, err := os.Stat(dumpPath); err != nil {
		return fmt.Errorf("sqlite dump file not found at %s: %w", dumpPath, err)
	}
	targetPath := sqliteDSNPath(cfg.Target.DB.DSN)
	if targetPath == "" {
		return fmt.Errorf("cannot derive target sqlite path from DSN")
	}
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}
	if cfg.Target.SSH != nil {
		cli, err := remote.Dial(cfg.Target.SSH)
		if err != nil {
			return err
		}
		defer cli.Close()
		return cli.WriteFile(targetPath, data, 0o640)
	}
	return os.WriteFile(targetPath, data, 0o640)
}

// -- inline DSN helpers to avoid an import cycle with internal/dump ----------

func parseMySQLDSN(dsn string) (host, port, user, pass, db string, err error) {
	// Delegates to the dump package via duplication kept minimal. If this
	// grows, move to a shared internal/dsn package.
	return parseMySQLDSNImpl(dsn)
}

func sqliteDSNPath(dsn string) string {
	return sqliteDSNPathImpl(dsn)
}
