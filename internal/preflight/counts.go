package preflight

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// SecretKeyImpact counts the DB rows that actually depend on SECRET_KEY.
// Built from real source data at preflight time, so the operator sees
// "0 TOTP users, 3 DEAD OAuth2 apps" instead of vague "secrets will be
// lost" warnings.
type SecretKeyImpact struct {
	TOTP           int // two_factor rows — TOTP codes
	OAuth2Active   int // user-owned app with a non-empty client_secret (will not decrypt)
	OAuth2DeadUser int // user-owned app with empty client_secret (already broken on source)
	OAuth2BuiltIn  int // uid=0 system apps (tea/GCM/git-credential-oauth; PKCE, safe)
	PushMirrors    int // push_mirror rows with stored credentials
	ActionsSecrets int // org/repo Actions secrets (value encrypted with SECRET_KEY)
	LDAPSources    int // login_source rows whose cfg blob includes a bind password
	Webauthn       int // webauthn_credential rows (always SAFE; counted for context)
}

// Summary returns a human-friendly one-line summary.
func (i *SecretKeyImpact) Summary() string {
	var parts []string
	if i.TOTP > 0 {
		parts = append(parts, fmt.Sprintf("%d TOTP 2FA users will lose codes (re-enroll required)", i.TOTP))
	}
	if i.OAuth2Active > 0 {
		parts = append(parts, fmt.Sprintf("%d user-owned OAuth2 apps will not decrypt", i.OAuth2Active))
	}
	if i.OAuth2DeadUser > 0 {
		parts = append(parts, fmt.Sprintf("%d user-owned OAuth2 apps already have empty client_secret (already broken, no loss)", i.OAuth2DeadUser))
	}
	if i.OAuth2BuiltIn > 0 {
		parts = append(parts, fmt.Sprintf("%d built-in OAuth2 apps are safe (PKCE public clients: tea/GCM/git-credential-oauth)", i.OAuth2BuiltIn))
	}
	if i.PushMirrors > 0 {
		parts = append(parts, fmt.Sprintf("%d push mirrors will lose stored credentials", i.PushMirrors))
	}
	if i.ActionsSecrets > 0 {
		parts = append(parts, fmt.Sprintf("%d Actions secrets will become unreadable (re-entry required)", i.ActionsSecrets))
	}
	if i.LDAPSources > 0 {
		parts = append(parts, fmt.Sprintf("%d LDAP login sources will lose bind-password (re-enter required)", i.LDAPSources))
	}
	if i.Webauthn > 0 {
		parts = append(parts, fmt.Sprintf("%d passkey registrations are safe (public key, no encryption)", i.Webauthn))
	}
	if len(parts) == 0 {
		return "no affected data"
	}
	return strings.Join(parts, "; ")
}

// Lossless returns true if migrating without SECRET_KEY actually loses nothing.
func (i *SecretKeyImpact) Lossless() bool {
	return i.TOTP == 0 &&
		i.OAuth2Active == 0 &&
		i.PushMirrors == 0 &&
		i.ActionsSecrets == 0 &&
		i.LDAPSources == 0
}

// countSecretKeyImpact runs a single query over the source DB to classify
// every piece of data that would be affected by a missing SECRET_KEY.
// Currently supports SQLite only (the most common case for Docker
// deployments). For Postgres/MySQL we return nil — operator gets the
// generic warning, no real regression.
//
// OAuth2 apps with empty client_secret AND owner-user 0 are Gitea's
// built-in system apps (`tea` CLI, Git Credential Manager,
// git-credential-oauth) — PKCE public clients that need no secret.
// They migrate fine and Forgejo ships the same IDs, so we classify
// them as safe rather than dead.
func countSecretKeyImpact(ssh *remote.Client, cfg *config.Config) (*SecretKeyImpact, error) {
	if cfg.Source.DB.Dialect != "sqlite3" {
		return nil, nil
	}
	// Each row tolerates a missing table (via sqlite_master existence
	// check) so we don't break when a very old or very new schema omits
	// one of these. For unavailable tables, we silently count 0.
	query := `
SELECT 'totp', COUNT(*) FROM two_factor;
SELECT 'oauth2_active', COUNT(*) FROM oauth2_application WHERE length(client_secret) > 0;
SELECT 'oauth2_dead_user', COUNT(*) FROM oauth2_application WHERE length(client_secret) = 0 AND uid > 0;
SELECT 'oauth2_builtin',   COUNT(*) FROM oauth2_application WHERE length(client_secret) = 0 AND uid = 0;
SELECT 'push_mirror', COUNT(*) FROM push_mirror;
SELECT 'actions_secrets', COUNT(*) FROM (
  SELECT 1 FROM sqlite_master WHERE type='table' AND name='secret' LIMIT 0
  UNION ALL SELECT 1 FROM secret
) ;
SELECT 'ldap_sources', COUNT(*) FROM login_source WHERE type IN (2, 5);
SELECT 'webauthn', COUNT(*) FROM webauthn_credential;`

	out, err := runSqlite(ssh, cfg, query)
	if err != nil {
		return nil, err
	}
	impact := &SecretKeyImpact{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		n, _ := strconv.Atoi(parts[1])
		switch parts[0] {
		case "totp":
			impact.TOTP = n
		case "oauth2_active":
			impact.OAuth2Active = n
		case "oauth2_dead_user":
			impact.OAuth2DeadUser = n
		case "oauth2_builtin":
			impact.OAuth2BuiltIn = n
		case "push_mirror":
			impact.PushMirrors = n
		case "actions_secrets":
			impact.ActionsSecrets = n
		case "ldap_sources":
			impact.LDAPSources = n
		case "webauthn":
			impact.Webauthn = n
		}
	}
	return impact, nil
}

// runSqlite executes a query against the source SQLite DB. Tries the host
// sqlite3 binary first (fast path when the DB file is on a bind-mount
// accessible by root); falls back to `docker exec sqlite3` for Docker
// installs where the host doesn't have sqlite3 installed.
func runSqlite(ssh *remote.Client, cfg *config.Config, query string) (string, error) {
	dbPath := cfg.Source.DB.DSN
	if i := strings.Index(dbPath, "?"); i >= 0 {
		dbPath = dbPath[:i]
	}
	dbPath = strings.TrimPrefix(dbPath, "file:")

	// Host sqlite3.
	if dbPath != "" {
		cmd := fmt.Sprintf("command -v sqlite3 >/dev/null 2>&1 && sqlite3 %s %s", shQuote(dbPath), shQuote(query))
		if out, err := ssh.Run(cmd); err == nil && len(out) > 0 {
			return string(out), nil
		}
	}

	// Fall back to `docker exec sqlite3` when we know the container.
	if cfg.Source.Docker != nil && cfg.Source.Docker.Container != "" {
		for _, p := range []string{
			"/data/gitea/gitea.db",
			"/data/gitea.db",
			"/var/lib/gitea/gitea.db",
			"/app/gitea/data/gitea.db",
		} {
			cmd := fmt.Sprintf("docker exec %s sqlite3 %s %s",
				shQuote(cfg.Source.Docker.Container), shQuote(p), shQuote(query))
			if out, err := ssh.Run(cmd); err == nil && len(out) > 0 {
				return string(out), nil
			}
		}
	}
	return "", fmt.Errorf("could not query sqlite DB (tried host sqlite3 + common container paths)")
}
