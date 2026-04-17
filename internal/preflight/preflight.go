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

	// DB reachability.
	checkDB(r, "source", cfg.Source.DB, log)
	checkDB(r, "target", cfg.Target.DB, log)

	// Target DB emptiness (setup-wizard detection).
	checkTargetDBEmpty(r, cfg, log)

	// SECRET_KEY presence on source (reads app.ini via SSH).
	if srcSSH != nil {
		checkSecretKey(r, srcSSH, cfg.Source.ConfigFile, log)
	}

	// Disk space on target work_dir vs source data_dir size.
	if srcSSH != nil && tgtSSH != nil {
		checkDisk(r, srcSSH, cfg.Source.DataDir, tgtSSH, cfg.WorkDir, log)
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

func checkDB(r *Result, label string, d config.DB, log *slog.Logger) {
	db, err := remote.OpenDB(d)
	if err != nil {
		r.add(Check{Name: label + ": db", Status: "FAIL", Detail: err.Error()})
		return
	}
	defer db.Close()
	r.add(Check{Name: label + ": db", Status: "PASS", Detail: d.Dialect})
}

func checkTargetDBEmpty(r *Result, cfg *config.Config, log *slog.Logger) {
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

func checkSecretKey(r *Result, ssh *remote.Client, configFile string, log *slog.Logger) {
	data, err := ssh.ReadFile(configFile)
	if err != nil {
		r.add(Check{Name: "source: app.ini readable", Status: "FAIL", Detail: err.Error()})
		return
	}
	keys := parseINI(data)
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
		r.add(Check{Name: "source: secret keys",
			Status: "FAIL",
			Detail: "missing/empty in app.ini: " + strings.Join(missing, ", ") +
				" — without these, 2FA/OAuth/encrypted-secret values will not survive migration"})
		return
	}
	r.add(Check{Name: "source: secret keys", Status: "PASS",
		Detail: "SECRET_KEY, INTERNAL_TOKEN, JWT_SECRET present"})
}

func checkDisk(r *Result, src *remote.Client, srcData string, tgt *remote.Client, workDir string, log *slog.Logger) {
	size, err := src.DirSizeBytes(srcData)
	if err != nil {
		r.add(Check{Name: "source: data_dir size", Status: "WARN", Detail: err.Error()})
		return
	}
	r.add(Check{Name: "source: data_dir size", Status: "PASS", Detail: humanBytes(size)})

	free, err := tgt.DiskFreeBytes(workDir)
	if err != nil {
		// Maybe work_dir doesn't exist yet; try the parent.
		if free2, err2 := tgt.DiskFreeBytes(filepath.Dir(workDir)); err2 == nil {
			free = free2
		} else {
			r.add(Check{Name: "target: work_dir free", Status: "WARN", Detail: err.Error()})
			return
		}
	}
	need := size * 2
	if free < need {
		r.add(Check{Name: "target: work_dir free", Status: "FAIL",
			Detail: fmt.Sprintf("have %s, need ≥ 2× source data_dir = %s", humanBytes(free), humanBytes(need))})
		return
	}
	r.add(Check{Name: "target: work_dir free", Status: "PASS", Detail: humanBytes(free)})
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
