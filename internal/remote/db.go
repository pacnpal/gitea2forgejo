package remote

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// OpenDB opens a *sql.DB for the given config.DB and pings it with a 5s
// timeout. The returned DB uses the driver name compatible with the dialect.
func OpenDB(d config.DB) (*sql.DB, error) {
	driver := d.Dialect
	if d.Dialect == "sqlite3" {
		driver = "sqlite" // modernc.org/sqlite registers itself as "sqlite"
	}
	db, err := sql.Open(driver, d.DSN)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", d.Dialect, err)
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(4)
	if err := pingWithTimeout(db, 5*time.Second); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", d.Dialect, err)
	}
	return db, nil
}

func pingWithTimeout(db *sql.DB, d time.Duration) error {
	ch := make(chan error, 1)
	go func() { ch <- db.Ping() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return fmt.Errorf("timeout after %s", d)
	}
}
