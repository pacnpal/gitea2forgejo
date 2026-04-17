package dump

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pacnpal/gitea2forgejo/internal/manifest"
)

// LoginSources reads the `login_source` table, which holds LDAP/OAuth2/SMTP
// auth source definitions. They do NOT round-trip through app.ini and are not
// exposed on the public API, so the DB is the only faithful way to capture
// them.
//
// Column names are stable across Gitea 1.x and Forgejo 7+ (verified against
// models/auth/source.go); we only read the handful of fields needed to build
// a manifest entry.
func LoginSources(db *sql.DB, log *slog.Logger) ([]manifest.LoginSource, error) {
	// `type` in the DB is an integer enum. Translate to the strings Gitea's
	// source table uses in its constants: 2=ldap, 3=smtp, 4=pam, 5=dldap,
	// 6=oauth2, 7=sspi. Anything else → "unknown".
	rows, err := db.Query(`SELECT id, name, is_active, type FROM login_source`)
	if err != nil {
		return nil, fmt.Errorf("query login_source: %w", err)
	}
	defer rows.Close()
	var out []manifest.LoginSource
	for rows.Next() {
		var (
			id       int64
			name     string
			isActive bool
			typeInt  int
		)
		if err := rows.Scan(&id, &name, &isActive, &typeInt); err != nil {
			return nil, fmt.Errorf("scan login_source: %w", err)
		}
		out = append(out, manifest.LoginSource{
			ID:       id,
			Name:     name,
			IsActive: isActive,
			Type:     loginSourceTypeName(typeInt),
		})
	}
	return out, rows.Err()
}

func loginSourceTypeName(t int) string {
	switch t {
	case 2:
		return "ldap"
	case 3:
		return "smtp"
	case 4:
		return "pam"
	case 5:
		return "dldap"
	case 6:
		return "oauth2"
	case 7:
		return "sspi"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}
