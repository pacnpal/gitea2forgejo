package dump

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// NativeDump writes a native DB dump of the source into workDir.
//
//   - postgres: `pg_dump -Fc` (custom format; compressed, restorable with pg_restore)
//   - mysql/mariadb: `mysqldump --single-transaction --routines --triggers --quick`
//   - sqlite3: SFTP-fetch the DB file from source host
//
// Output file is workDir/gitea.dump (postgres), .sql (mysql), or .sqlite
// (sqlite). Returns the local path.
func NativeDump(cfg *config.Config, log *slog.Logger) (string, error) {
	d := cfg.Source.DB
	switch d.Dialect {
	case "postgres":
		return dumpPostgres(cfg, log)
	case "mysql":
		return dumpMySQL(cfg, log)
	case "sqlite3":
		return dumpSQLite(cfg, log)
	default:
		return "", fmt.Errorf("unsupported db dialect %q", d.Dialect)
	}
}

func dumpPostgres(cfg *config.Config, log *slog.Logger) (string, error) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		return "", fmt.Errorf("pg_dump not found in PATH: %w", err)
	}
	out := filepath.Join(cfg.WorkDir, "gitea.dump")
	cmd := exec.Command("pg_dump",
		"-Fc",
		"--no-owner", "--no-acl",
		"-f", out,
		cfg.Source.DB.DSN,
	)
	cmd.Stdout = &logWriter{log: log, prefix: "pg_dump"}
	cmd.Stderr = &logWriter{log: log, prefix: "pg_dump"}
	log.Info("pg_dump: starting", "out", out)
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pg_dump: %w", err)
	}
	fi, _ := os.Stat(out)
	log.Info("pg_dump: done",
		"elapsed", time.Since(start).Round(time.Second),
		"size_bytes", sizeOrZero(fi))
	return out, nil
}

func dumpMySQL(cfg *config.Config, log *slog.Logger) (string, error) {
	if _, err := exec.LookPath("mysqldump"); err != nil {
		return "", fmt.Errorf("mysqldump not found in PATH: %w", err)
	}
	host, port, user, pass, db, err := parseMySQLDSN(cfg.Source.DB.DSN)
	if err != nil {
		return "", fmt.Errorf("parse mysql DSN: %w", err)
	}
	out := filepath.Join(cfg.WorkDir, "gitea.sql")
	args := []string{
		"--single-transaction",
		"--routines",
		"--triggers",
		"--quick",
		"--default-character-set=utf8mb4",
		"-h", host, "-P", port, "-u", user,
		"-r", out,
		db,
	}
	cmd := exec.Command("mysqldump", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pass)
	cmd.Stdout = &logWriter{log: log, prefix: "mysqldump"}
	cmd.Stderr = &logWriter{log: log, prefix: "mysqldump"}
	log.Info("mysqldump: starting", "out", out, "host", host, "db", db)
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mysqldump: %w", err)
	}
	fi, _ := os.Stat(out)
	log.Info("mysqldump: done",
		"elapsed", time.Since(start).Round(time.Second),
		"size_bytes", sizeOrZero(fi))
	return out, nil
}

func dumpSQLite(cfg *config.Config, log *slog.Logger) (string, error) {
	// DSN for modernc.org/sqlite is just a filesystem path (optionally with
	// query params we'll strip).
	srcPath := sqliteDSNPath(cfg.Source.DB.DSN)
	if srcPath == "" {
		return "", fmt.Errorf("empty sqlite DSN")
	}
	if cfg.Source.SSH == nil {
		// Assume DSN path is local to the machine running the tool.
		out := filepath.Join(cfg.WorkDir, "gitea.sqlite")
		log.Info("sqlite: local copy", "from", srcPath, "to", out)
		return out, copyLocalFile(srcPath, out)
	}
	cli, err := remote.Dial(cfg.Source.SSH)
	if err != nil {
		return "", err
	}
	defer cli.Close()
	out := filepath.Join(cfg.WorkDir, "gitea.sqlite")
	log.Info("sqlite: fetching via SFTP", "from", srcPath, "to", out)
	start := time.Now()
	if _, err := cli.FetchFile(srcPath, out); err != nil {
		return "", fmt.Errorf("sftp fetch sqlite: %w", err)
	}
	fi, _ := os.Stat(out)
	log.Info("sqlite: done",
		"elapsed", time.Since(start).Round(time.Second),
		"size_bytes", sizeOrZero(fi))
	return out, nil
}

// parseMySQLDSN accepts the go-sql-driver/mysql DSN format
// `user:password@tcp(host:port)/dbname?opts` as well as URL form.
func parseMySQLDSN(dsn string) (host, port, user, pass, db string, err error) {
	// Try URL form first (mysql://user:pass@host:port/db).
	if strings.HasPrefix(dsn, "mysql://") {
		u, e := url.Parse(dsn)
		if e != nil {
			return "", "", "", "", "", e
		}
		host = u.Hostname()
		port = u.Port()
		if port == "" {
			port = "3306"
		}
		user = u.User.Username()
		pass, _ = u.User.Password()
		db = strings.TrimPrefix(u.Path, "/")
		return
	}
	// go-sql-driver form: user:password@tcp(host:port)/dbname?opts
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		err = fmt.Errorf("no @ in DSN")
		return
	}
	creds, rest := dsn[:at], dsn[at+1:]
	if i := strings.Index(creds, ":"); i >= 0 {
		user, pass = creds[:i], creds[i+1:]
	} else {
		user = creds
	}
	// rest = "tcp(host:port)/dbname?opts" OR "unix(/socket)/dbname" etc.
	lp := strings.Index(rest, "(")
	rp := strings.Index(rest, ")")
	if lp < 0 || rp < 0 || rp < lp {
		err = fmt.Errorf("malformed DSN net part")
		return
	}
	proto := rest[:lp]
	addr := rest[lp+1 : rp]
	if proto != "tcp" {
		err = fmt.Errorf("unsupported mysql DSN protocol %q (only tcp)", proto)
		return
	}
	if i := strings.Index(addr, ":"); i >= 0 {
		host, port = addr[:i], addr[i+1:]
	} else {
		host, port = addr, "3306"
	}
	tail := rest[rp+1:]
	tail = strings.TrimPrefix(tail, "/")
	if i := strings.Index(tail, "?"); i >= 0 {
		db = tail[:i]
	} else {
		db = tail
	}
	if db == "" {
		err = fmt.Errorf("no database name in DSN")
	}
	return
}

// sqliteDSNPath strips any "file:" prefix and "?..." suffix from a sqlite DSN.
func sqliteDSNPath(dsn string) string {
	s := strings.TrimPrefix(dsn, "file:")
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i]
	}
	return s
}

func sizeOrZero(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.Size()
}

func copyLocalFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
