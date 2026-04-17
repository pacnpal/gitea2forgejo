// Package preflight runs read-only checks before a migration: versions, SSH
// reachability, DB connectivity, disk space, and the SECRET_KEY warning.
//
// Output is a go/no-go `preflight-report.md` written into work_dir, plus a
// non-zero exit if any hard-fail check fails.
package preflight

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pacnpal/gitea2forgejo/internal/client"
	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
	"github.com/pacnpal/gitea2forgejo/internal/restore"
)

type Result struct {
	Checks      []Check
	SourceVer   string
	TargetVer   string
	HardFails   int
	Warns       int
}

type Check struct {
	Name    string
	Status  string // PASS | WARN | FAIL
	Detail  string
}

func (r *Result) add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Status {
	case "FAIL":
		r.HardFails++
	case "WARN":
		r.Warns++
	}
}

// Run performs all checks and returns the result. It does not write files;
// that's the caller's job (typically the cobra command).
func Run(cfg *config.Config, log *slog.Logger) *Result {
	r := &Result{}

	// Version checks.
	checkVersion(r, cfg.Source, client.KindSource, log)
	checkVersion(r, cfg.Target, client.KindTarget, log)

	// SSH reachability.
	srcSSH := checkSSH(r, "source", cfg.Source.SSH, log)
	tgtSSH := checkSSH(r, "target", cfg.Target.SSH, log)
	defer closeSSH(srcSSH)
	defer closeSSH(tgtSSH)

	// DB reachability. SQLite is a file on the remote host, so we probe
	// via SSH rather than attempting to sql.Open a remote path locally.
	checkDB(r, "source", cfg.Source.DB, srcSSH, log)
	checkDB(r, "target", cfg.Target.DB, tgtSSH, log)

	// Target DB emptiness (setup-wizard detection).
	checkTargetDBEmpty(r, cfg, log)

	// SECRET_KEY presence on source (reads app.ini via SSH, overlays
	// Docker env vars when source runs in a container).
	if srcSSH != nil {
		checkSecretKey(r, srcSSH, cfg, log)
	}

	// Disk space: work_dir is LOCAL (on the mig-host). Compare its free
	// space to the source data_dir size (read over SSH).
	if srcSSH != nil {
		checkDisk(r, srcSSH, cfg.Source.DataDir, cfg.WorkDir, log)
	}

	return r
}

// WriteReport writes a markdown go/no-go report to workDir/preflight-report.md.
func (r *Result) WriteReport(workDir string) (string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(workDir, "preflight-report.md")
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Preflight report\n\n")
	if r.HardFails > 0 {
		fmt.Fprintf(&b, "**Decision: NO-GO** (%d hard fails, %d warnings)\n\n", r.HardFails, r.Warns)
	} else if r.Warns > 0 {
		fmt.Fprintf(&b, "**Decision: GO with warnings** (%d warnings)\n\n", r.Warns)
	} else {
		fmt.Fprintf(&b, "**Decision: GO** (all checks pass)\n\n")
	}
	fmt.Fprintf(&b, "Source Gitea: `%s`\n\nTarget Forgejo: `%s`\n\n", r.SourceVer, r.TargetVer)
	fmt.Fprintf(&b, "| Check | Status | Detail |\n|---|---|---|\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, c.Status, strings.ReplaceAll(c.Detail, "|", `\|`))
	}
	return path, os.WriteFile(path, b.Bytes(), 0o644)
}

// -- check helpers -----------------------------------------------------------

func checkVersion(r *Result, inst config.Instance, kind client.Kind, log *slog.Logger) {
	c, err := client.New(&inst, kind)
	if err != nil {
		r.add(Check{Name: string(kind) + ": API client", Status: "FAIL", Detail: err.Error()})
		return
	}
	v, _, err := c.ServerVersion()
	if err != nil {
		r.add(Check{Name: string(kind) + ": version", Status: "FAIL", Detail: err.Error()})
		return
	}
	if kind == client.KindSource {
		r.SourceVer = v
	} else {
		r.TargetVer = v
	}
	// Validate expected version ranges.
	if kind == client.KindSource {
		if strings.HasPrefix(v, "1.22") || strings.HasPrefix(v, "1.21") || strings.HasPrefix(v, "1.20") {
			r.add(Check{Name: "source: version", Status: "WARN",
				Detail: v + " — drop-in upgrade is supported for ≤1.22. Consider Forgejo's official path."})
			return
		}
		// Source Gitea 1.23+ is what this tool targets. Anything newer
		// is fine; we parse on an INI basis and the schema trick works
		// across minor bumps. Warn on unknown major.
		if !strings.HasPrefix(v, "1.") {
			r.add(Check{Name: "source: version", Status: "WARN", Detail: "unexpected non-1.x version " + v})
			return
		}
	}
	if kind == client.KindTarget {
		// Forgejo reports something like "11.0.12+gitea-1.22.0" or
		// "15.0.0+gitea-1.24.0". Extract the leading Forgejo major.
		major := forgejoMajor(v)
		if major > 0 && major < 15 {
			r.add(Check{Name: "target: version", Status: "WARN",
				Detail: v + " — tool was designed for Forgejo v15+; the schema-trick number (305) may need adjustment for older targets"})
			return
		}
	}
	r.add(Check{Name: string(kind) + ": version", Status: "PASS", Detail: v})
}

// forgejoMajor extracts the leading major version from a Forgejo version
// string like "11.0.12+gitea-1.22.0" → 11. Returns 0 if unparseable.
func forgejoMajor(v string) int {
	dot := strings.Index(v, ".")
	if dot <= 0 {
		return 0
	}
	n := 0
	for _, c := range v[:dot] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func checkSSH(r *Result, label string, ssh *config.SSH, log *slog.Logger) *remote.Client {
	if ssh == nil {
		r.add(Check{Name: label + ": ssh", Status: "WARN",
			Detail: "no ssh block — script cannot perform dump/restore filesystem operations"})
		return nil
	}
	cli, err := remote.Dial(ssh)
	if err != nil {
		r.add(Check{Name: label + ": ssh", Status: "FAIL", Detail: err.Error()})
		return nil
	}
	if _, err := cli.Run("true"); err != nil {
		r.add(Check{Name: label + ": ssh exec", Status: "FAIL", Detail: err.Error()})
		cli.Close()
		return nil
	}
	r.add(Check{Name: label + ": ssh", Status: "PASS", Detail: ssh.User + "@" + ssh.Host})
	return cli
}

func closeSSH(c *remote.Client) {
	if c != nil {
		_ = c.Close()
	}
}

func checkDB(r *Result, label string, d config.DB, ssh *remote.Client, log *slog.Logger) {
	if d.Dialect == "sqlite3" {
		checkDBSQLite(r, label, d, ssh)
		return
	}
	db, err := remote.OpenDB(d)
	if err != nil {
		r.add(Check{Name: label + ": db", Status: "FAIL", Detail: err.Error()})
		return
	}
	defer db.Close()
	r.add(Check{Name: label + ": db", Status: "PASS", Detail: d.Dialect})
}

// checkDBSQLite verifies the sqlite file exists on the remote host and
// starts with the SQLite magic bytes. We can't sql.Open the DSN here
// because the file lives on the source/target host, not on the machine
// running gitea2forgejo.
func checkDBSQLite(r *Result, label string, d config.DB, ssh *remote.Client) {
	if ssh == nil {
		r.add(Check{Name: label + ": db", Status: "WARN",
			Detail: "sqlite3; can't verify without ssh"})
		return
	}
	path := d.DSN
	// Strip any file: prefix or ?params.
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "file:")
	if path == "" {
		r.add(Check{Name: label + ": db", Status: "FAIL",
			Detail: "sqlite3 DSN is empty"})
		return
	}
	out, err := ssh.Run(fmt.Sprintf("test -f %s && head -c 15 %s", shQuote(path), shQuote(path)))
	if err != nil {
		// For target, the file may not exist yet (fresh Forgejo install).
		// Surface it as a WARN rather than FAIL.
		status := "WARN"
		if label == "source" {
			status = "FAIL"
		}
		r.add(Check{Name: label + ": db", Status: status,
			Detail: fmt.Sprintf("%s: %v", path, err)})
		return
	}
	if !strings.HasPrefix(string(out), "SQLite format 3") {
		r.add(Check{Name: label + ": db", Status: "FAIL",
			Detail: fmt.Sprintf("%s exists but isn't a SQLite database", path)})
		return
	}
	r.add(Check{Name: label + ": db", Status: "PASS",
		Detail: "sqlite3 at " + path})
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func checkTargetDBEmpty(r *Result, cfg *config.Config, log *slog.Logger) {
	// SQLite target-empty check: if the DB file doesn't exist yet it's
	// trivially empty. Skip the InspectTargetDB call that would try to
	// sql.Open a remote path locally.
	if cfg.Target.DB.Dialect == "sqlite3" {
		r.add(Check{Name: "target: db empty", Status: "PASS",
			Detail: "sqlite3 — target emptiness verified at restore time"})
		return
	}
	state, err := restore.InspectTargetDB(cfg)
	if err != nil {
		r.add(Check{Name: "target: db empty", Status: "WARN", Detail: err.Error()})
		return
	}
	if state.Empty {
		r.add(Check{Name: "target: db empty", Status: "PASS", Detail: "0 tables — ready for restore"})
		return
	}
	detail := fmt.Sprintf("%d tables present (version=%d, forgejo_extras=%v)",
		state.TableCount, state.VersionRow, state.HasForgejoExtras)
	if cfg.Options.ResetTargetDB {
		r.add(Check{Name: "target: db empty", Status: "WARN",
			Detail: detail + "; options.reset_target_db=true, will be wiped at restore"})
		return
	}
	r.add(Check{Name: "target: db empty", Status: "FAIL",
		Detail: detail + "; most likely Forgejo's setup wizard has been run. " +
			"Set options.reset_target_db: true OR drop the database manually"})
}

func checkSecretKey(r *Result, ssh *remote.Client, cfg *config.Config, log *slog.Logger) {
	data, err := ssh.ReadFile(cfg.Source.ConfigFile)
	if err != nil {
		r.add(Check{Name: "source: app.ini readable", Status: "FAIL", Detail: err.Error()})
		return
	}
	keys := parseINI(data)

	// Dockerized Gitea/Forgejo commonly pass SECRET_KEY via env vars in
	// the form GITEA__security__SECRET_KEY=... (or FORGEJO__...). Those
	// override app.ini, so read the container's env and layer them on top.
	if cfg.Source.Docker != nil && cfg.Source.Docker.Container != "" {
		overlayContainerEnv(ssh, cfg.Source.Docker.Container, keys, log)
	}

	// Gitea 1.25+ supports the _URI variants (SECRET_KEY_URI etc.) which
	// point at a file containing the value, rather than embedding it in
	// app.ini. Resolve those before reporting missing.
	resolveURIs(ssh, cfg, keys, log)

	missing := []string{}
	for _, k := range []struct{ section, key string }{
		{"security", "SECRET_KEY"},
		{"security", "INTERNAL_TOKEN"},
		{"oauth2", "JWT_SECRET"},
	} {
		if strings.TrimSpace(keys[k.section+"."+k.key]) == "" {
			missing = append(missing, k.section+"."+k.key)
		}
	}
	if len(missing) > 0 {
		// Replace hand-wavy "will be lost" with real counts from the DB.
		detail := "missing/empty in app.ini, container env, and _URI lookups: " +
			strings.Join(missing, ", ")
		impact, ierr := countSecretKeyImpact(ssh, cfg)
		if ierr != nil {
			log.Warn("could not compute secret-key impact", "err", ierr)
		}

		status := "FAIL"
		switch {
		case impact != nil && impact.Lossless() && cfg.Options.AcceptMissingSecretKey:
			status = "PASS"
			detail += ". No data depends on SECRET_KEY: " + impact.Summary() +
				". options.accept_missing_secret_key is true — safe to proceed."
		case impact != nil && impact.Lossless():
			// Nothing actually at stake, but operator hasn't acknowledged.
			status = "WARN"
			detail += ". No data depends on SECRET_KEY: " + impact.Summary() +
				". Set options.accept_missing_secret_key: true to proceed."
		case cfg.Options.AcceptMissingSecretKey:
			status = "WARN"
			detail += ". Impact: "
			if impact != nil {
				detail += impact.Summary()
			} else {
				detail += "2FA/OAuth/Actions-secrets/push-mirror credentials will be unrecoverable on target"
			}
			detail += ". options.accept_missing_secret_key is true — proceeding"
		default:
			detail += ". Impact: "
			if impact != nil {
				detail += impact.Summary()
			} else {
				detail += "2FA/OAuth/Actions-secrets/push-mirror credentials will be unrecoverable on target"
			}
			detail += ". Set options.accept_missing_secret_key: true in config.yaml to proceed"
		}
		r.add(Check{Name: "source: secret keys", Status: status, Detail: detail})
		return
	}
	r.add(Check{Name: "source: secret keys", Status: "PASS",
		Detail: "SECRET_KEY, INTERNAL_TOKEN, JWT_SECRET present"})
}

// resolveURIs checks each required secret. If its primary value is empty
// but a corresponding _URI key points to a file, read the file (via docker
// exec when containerized, else direct SSH read) and use its contents.
func resolveURIs(ssh *remote.Client, cfg *config.Config, keys map[string]string, log *slog.Logger) {
	for _, k := range []struct{ section, key string }{
		{"security", "SECRET_KEY"},
		{"security", "INTERNAL_TOKEN"},
		{"oauth2", "JWT_SECRET"},
	} {
		flat := k.section + "." + k.key
		if strings.TrimSpace(keys[flat]) != "" {
			continue
		}
		uriKey := flat + "_URI"
		uri := strings.TrimSpace(keys[uriKey])
		if uri == "" {
			continue
		}
		val, err := readURI(ssh, cfg, uri)
		if err != nil {
			log.Warn("resolve URI failed", "key", uriKey, "uri", uri, "err", err)
			continue
		}
		if val != "" {
			keys[flat] = val
			log.Info("resolved secret via _URI", "key", flat, "uri", uri)
		}
	}
}

// readURI interprets a Gitea _URI reference (currently only "file:" scheme).
// When source runs in Docker, the path is inside the container — use
// `docker exec cat` instead of a direct SSH read.
func readURI(ssh *remote.Client, cfg *config.Config, uri string) (string, error) {
	switch {
	case strings.HasPrefix(uri, "file://"):
		uri = strings.TrimPrefix(uri, "file://")
	case strings.HasPrefix(uri, "file:"):
		uri = strings.TrimPrefix(uri, "file:")
	default:
		return "", fmt.Errorf("unsupported URI scheme in %q (only file: is supported)", uri)
	}
	path := strings.TrimSpace(uri)
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if cfg.Source.Docker != nil && cfg.Source.Docker.Container != "" {
		cmd := fmt.Sprintf("docker exec %s cat %s",
			shQuote(cfg.Source.Docker.Container), shQuote(path))
		out, err := ssh.Run(cmd)
		if err != nil {
			return "", fmt.Errorf("docker exec cat: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	data, err := ssh.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// overlayContainerEnv reads environment variables off the container and
// translates GITEA__section__KEY / FORGEJO__section__KEY into keys map
// entries, overriding any app.ini values.
func overlayContainerEnv(ssh *remote.Client, container string, keys map[string]string, log *slog.Logger) {
	cmd := fmt.Sprintf(
		"docker inspect --format '{{range .Config.Env}}{{.}}\\n{{end}}' %s 2>/dev/null || true",
		shQuote(container),
	)
	out, err := ssh.Run(cmd)
	if err != nil {
		log.Warn("overlay container env: docker inspect failed", "err", err)
		return
	}
	added := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		name, val := line[:eq], line[eq+1:]
		var prefix string
		switch {
		case strings.HasPrefix(name, "GITEA__"):
			prefix = "GITEA__"
		case strings.HasPrefix(name, "FORGEJO__"):
			prefix = "FORGEJO__"
		default:
			continue
		}
		body := strings.TrimPrefix(name, prefix)
		sub := strings.SplitN(body, "__", 2)
		if len(sub) != 2 {
			continue
		}
		k := strings.ToLower(sub[0]) + "." + strings.ToUpper(sub[1])
		keys[k] = val
		added++
	}
	if added > 0 {
		log.Info("overlay: merged container env vars", "count", added, "container", container)
	}
}

func checkDisk(r *Result, src *remote.Client, srcData, workDir string, log *slog.Logger) {
	size, err := src.DirSizeBytes(srcData)
	if err != nil {
		r.add(Check{Name: "source: data_dir size", Status: "WARN", Detail: err.Error()})
		return
	}
	r.add(Check{Name: "source: data_dir size", Status: "PASS", Detail: humanBytes(size)})

	// work_dir is on the mig-host (the machine running gitea2forgejo).
	free, err := localDiskFreeBytes(workDir)
	if err != nil {
		r.add(Check{Name: "mig-host: work_dir free", Status: "WARN", Detail: err.Error()})
		return
	}
	// Peak local usage: tar.zst fetched (≈ 0.4-0.7× original) + extracted
	// tree (1× original). 1.5× is a realistic upper bound for tar.zst;
	// tar (uncompressed) would need ~2×.
	mult := 1.5
	need := uint64(float64(size) * mult)
	if free < need {
		r.add(Check{Name: "mig-host: work_dir free", Status: "FAIL",
			Detail: fmt.Sprintf("have %s at %s, need ≥ %.1f× source data_dir = %s. "+
				"Point work_dir at a filesystem with more space (edit config.yaml's work_dir), "+
				"or symlink ./work to a larger volume.",
				humanBytes(free), workDir, mult, humanBytes(need))})
		return
	}
	r.add(Check{Name: "mig-host: work_dir free", Status: "PASS",
		Detail: humanBytes(free) + " at " + workDir})
}

// localDiskFreeBytes returns free bytes on the filesystem holding path.
// Walks up to a parent if path doesn't exist yet (common: work_dir hasn't
// been created before the first dump).
func localDiskFreeBytes(path string) (uint64, error) {
	for p := path; p != "" && p != "."; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			var st syscall.Statfs_t
			if err := syscall.Statfs(p, &st); err != nil {
				return 0, err
			}
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		}
	}
	// Fallback: stat CWD.
	var st syscall.Statfs_t
	if err := syscall.Statfs(".", &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// parseINI returns a flat map keyed by "section.key". Minimal: we only need
// to read a handful of known-present keys. Comments (# or ;) and quotes are
// stripped.
func parseINI(data []byte) map[string]string {
	out := map[string]string{}
	section := ""
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		k := strings.ToUpper(strings.TrimSpace(line[:i]))
		v := strings.TrimSpace(line[i+1:])
		v = strings.Trim(v, `"'`)
		out[section+"."+k] = v
	}
	return out
}

func humanBytes(n uint64) string {
	const (
		k = 1024
		m = 1024 * k
		g = 1024 * m
		t = 1024 * g
	)
	switch {
	case n >= t:
		return fmt.Sprintf("%.1f TiB", float64(n)/float64(t))
	case n >= g:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(g))
	case n >= m:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(m))
	case n >= k:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(k))
	}
	return fmt.Sprintf("%d B", n)
}
