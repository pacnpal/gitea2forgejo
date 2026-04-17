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

// countSecretKeyImpact classifies rows in the source DB that depend on
// SECRET_KEY. Queries are run one-at-a-time and individual failures
// (missing table on an unusual schema) are swallowed — we want a best-
// effort count, not an all-or-nothing.
//
// Currently supports SQLite only; Postgres/MySQL fall back to generic
// messaging in the caller.
//
// OAuth2 apps with uid=0 are Gitea's built-in system apps
// (tea / Git Credential Manager / git-credential-oauth) — PKCE public
// clients that need no secret. Classified as safe, not dead.
func countSecretKeyImpact(ssh *remote.Client, cfg *config.Config) (*SecretKeyImpact, error) {
	if cfg.Source.DB.Dialect != "sqlite3" {
		return nil, nil
	}
	impact := &SecretKeyImpact{}
	targets := []struct {
		query string
		into  *int
	}{
		{"SELECT COUNT(*) FROM two_factor", &impact.TOTP},
		{"SELECT COUNT(*) FROM oauth2_application WHERE length(client_secret) > 0", &impact.OAuth2Active},
		{"SELECT COUNT(*) FROM oauth2_application WHERE length(client_secret) = 0 AND uid > 0", &impact.OAuth2DeadUser},
		{"SELECT COUNT(*) FROM oauth2_application WHERE length(client_secret) = 0 AND uid = 0", &impact.OAuth2BuiltIn},
		{"SELECT COUNT(*) FROM push_mirror", &impact.PushMirrors},
		{"SELECT COUNT(*) FROM secret", &impact.ActionsSecrets},
		{"SELECT COUNT(*) FROM login_source WHERE type IN (2, 5)", &impact.LDAPSources},
		{"SELECT COUNT(*) FROM webauthn_credential", &impact.Webauthn},
	}
	any := false
	for _, t := range targets {
		n, err := runSqliteCount(ssh, cfg, t.query)
		if err != nil {
			continue // missing table or other per-query error — leave count at 0
		}
		any = true
		*t.into = n
	}
	if !any {
		return nil, fmt.Errorf("could not query sqlite DB (host sqlite3 + common container paths both failed)")
	}
	return impact, nil
}

// runSqliteCount runs a single COUNT(*) query and returns the integer.
// Uses the same host-sqlite3-then-docker-exec fallback as runSqlite.
func runSqliteCount(ssh *remote.Client, cfg *config.Config, query string) (int, error) {
	out, err := runSqlite(ssh, cfg, query)
	if err != nil {
		return 0, err
	}
	// SQLite prints a single column/row here, e.g. "42" with newline.
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, fmt.Errorf("empty response")
	}
	// Some sqlite builds print headers when they feel like it; take
	// the LAST non-empty line to be robust.
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", line, err)
		}
		return n, nil
	}
	return 0, fmt.Errorf("no parseable line in output")
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
