package restore

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// CleanOrphanFKs deletes rows that would violate FK constraints that
// Forgejo's v15 forward migrations enforce. The imported Gitea DB may
// carry orphan rows — references to users/repos that were deleted in
// Gitea without cascading cleanup — and Forgejo's migration layer
// recreates affected tables with FKs that reject the orphans, looping
// ORM engine init forever until Forgejo gives up:
//
//	[E] [Error SQL Query] INSERT INTO __alter_action_runner_token ...
//	    FROM action_runner_token - FOREIGN KEY constraint failed
//	[F] InitDBEngine failed: foreign key constraint violation
//
// Run AFTER DBImport + SchemaTrick and BEFORE StartService. Tolerant
// of missing tables (older Gitea schemas predate action_runner_token).
func CleanOrphanFKs(cfg *config.Config, log *slog.Logger) error {
	db, err := remote.OpenDB(cfg.Target.DB)
	if err != nil {
		return fmt.Errorf("open target db: %w", err)
	}
	defer db.Close()

	userTable := quoteUserTable(cfg.Target.DB.Dialect)

	// `owner_id = 0` and `repo_id = 0` are Gitea's "no owner" / "no repo"
	// convention for global tokens. Forgejo v15's FKs are strict though
	// — any value not in user.id / repository.id fails, including 0
	// (there's no user/repo with id=0). v0.2.23's "!= 0" exclusion
	// missed exactly those rows and the migration still crashed. Runner
	// tokens are session-like and must be re-registered after any
	// migration (already in the post-restore checklist), so the only
	// safe answer is to remove every row whose FK target doesn't
	// resolve, 0 included.
	stmts := []struct {
		label string
		sql   string
	}{
		{
			label: "action_runner_token.owner_id → user",
			sql: fmt.Sprintf(
				`DELETE FROM action_runner_token WHERE owner_id NOT IN (SELECT id FROM %s)`,
				userTable),
		},
		{
			label: "action_runner_token.repo_id → repository",
			sql:   `DELETE FROM action_runner_token WHERE repo_id NOT IN (SELECT id FROM repository)`,
		},
	}

	var total int64
	for _, s := range stmts {
		n, err := execCount(db, s.sql)
		if err != nil {
			// Missing table / missing column is a WARN, not a failure —
			// the Gitea source may predate the table entirely, in which
			// case there's nothing to clean.
			log.Warn("fk-cleanup: statement skipped", "label", s.label, "err", err)
			continue
		}
		total += n
		if n > 0 {
			log.Info("fk-cleanup: deleted orphan rows", "label", s.label, "rows", n)
		}
	}
	if total == 0 {
		log.Info("fk-cleanup: no orphan rows needed removal")
	}
	return nil
}

// execCount runs sql and returns RowsAffected, forgiving drivers that
// don't report the count.
func execCount(db *sql.DB, sql string) (int64, error) {
	res, err := db.Exec(sql)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// quoteUserTable returns the `user` table name quoted for the given
// dialect. `user` is a reserved word in SQL and most dialects need
// quoting; unquoted works on SQLite by accident, not contract.
func quoteUserTable(dialect string) string {
	switch dialect {
	case "mysql":
		return "`user`"
	case "postgres", "sqlite3":
		return `"user"`
	default:
		return "user"
	}
}
