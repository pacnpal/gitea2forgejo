// Package verifydump inspects a dump in work_dir and answers "is this
// safe to restore?" by cross-checking the on-disk artifacts against the
// harvested source manifest.
//
// Run after `gitea2forgejo dump`, BEFORE `restore`, to catch truncated
// tarballs, unreadable DB dumps, or wildly off entity counts before
// they become a partial-restore incident.
package verifydump

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/manifest"
)

// Check is one inspection finding.
type Check struct {
	Name   string
	Status string // PASS | WARN | FAIL
	Detail string
}

// Result aggregates all checks.
type Result struct {
	Checks    []Check
	HardFails int
	Warns     int
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

// Run performs all checks and returns the aggregated result.
func Run(cfg *config.Config, log *slog.Logger) *Result {
	r := &Result{}

	m, manifestOK := checkManifest(r, cfg)
	checkTarball(r, cfg, m, manifestOK)
	checkNativeDB(r, cfg)
	checkS3Mirror(r, cfg)

	return r
}

// WriteReport writes a markdown summary alongside the other reports.
func (r *Result) WriteReport(workDir string) (string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(workDir, "verify-dump-report.md")
	var b bytes.Buffer
	fmt.Fprintf(&b, "# verify-dump report\n\n")
	switch {
	case r.HardFails > 0:
		fmt.Fprintf(&b, "**Decision: DO NOT RESTORE** (%d hard fails, %d warnings)\n\n",
			r.HardFails, r.Warns)
	case r.Warns > 0:
		fmt.Fprintf(&b, "**Decision: restore is probably fine** (%d warnings — review)\n\n", r.Warns)
	default:
		fmt.Fprintf(&b, "**Decision: dump verified** (all checks passed)\n\n")
	}
	fmt.Fprintf(&b, "| Check | Status | Detail |\n|---|---|---|\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "| %s | %s | %s |\n",
			c.Name, c.Status, strings.ReplaceAll(c.Detail, "|", `\|`))
	}
	return path, os.WriteFile(path, b.Bytes(), 0o644)
}

// -- checks ------------------------------------------------------------------

func checkManifest(r *Result, cfg *config.Config) (*manifest.Manifest, bool) {
	path := filepath.Join(cfg.WorkDir, "source-manifest.json")
	m, err := manifest.Load(path)
	if err != nil {
		r.add(Check{Name: "manifest file", Status: "FAIL",
			Detail: fmt.Sprintf("%s: %v", path, err)})
		return nil, false
	}
	if len(m.Users) == 0 && len(m.Orgs) == 0 && len(m.Repos) == 0 {
		r.add(Check{Name: "manifest file", Status: "FAIL",
			Detail: "manifest is empty — dump may have aborted before harvest"})
		return m, false
	}
	r.add(Check{Name: "manifest file", Status: "PASS",
		Detail: fmt.Sprintf("%d users, %d orgs, %d repos, %d packages",
			len(m.Users), len(m.Orgs), len(m.Repos), len(m.Packages))})
	return m, true
}

func checkTarball(r *Result, cfg *config.Config, m *manifest.Manifest, manifestOK bool) {
	ext := cfg.Options.DumpFormat
	tarPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	fi, err := os.Stat(tarPath)
	if err != nil {
		if os.IsNotExist(err) && cfg.Options.SkipGiteaDump {
			r.add(Check{Name: "dump tarball", Status: "WARN",
				Detail: "not present; options.skip_gitea_dump is true"})
			return
		}
		r.add(Check{Name: "dump tarball", Status: "FAIL", Detail: err.Error()})
		return
	}
	if fi.Size() < 1024 {
		r.add(Check{Name: "dump tarball", Status: "FAIL",
			Detail: fmt.Sprintf("%s exists but is only %d bytes", tarPath, fi.Size())})
		return
	}

	entries, err := listTarball(tarPath, ext)
	if err != nil {
		r.add(Check{Name: "dump tarball: listable", Status: "FAIL",
			Detail: err.Error()})
		return
	}
	r.add(Check{Name: "dump tarball: listable", Status: "PASS",
		Detail: fmt.Sprintf("%d entries, %s", len(entries), humanBytes(fi.Size()))})

	// Sanity: expect at least one of these common members.
	expected := []string{"app.ini", "custom/", "data/", "repos/", "gitea-db.sql"}
	seen := map[string]bool{}
	for _, e := range entries {
		for _, want := range expected {
			if strings.HasPrefix(e, want) || e == want {
				seen[want] = true
			}
		}
	}
	var missing []string
	for _, w := range expected {
		if !seen[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		r.add(Check{Name: "dump tarball: structure", Status: "WARN",
			Detail: "not found in archive: " + strings.Join(missing, ", ")})
	} else {
		r.add(Check{Name: "dump tarball: structure", Status: "PASS",
			Detail: "contains app.ini, custom/, data/, repos/, gitea-db.sql"})
	}

	// Repo count cross-check — every .git directory at top of `repos/`
	// should correspond to one manifest repo (approximately — this is
	// rough because nested owners appear as `repos/<owner>/<repo>.git/`).
	if manifestOK && m != nil {
		repoDirs := 0
		for _, e := range entries {
			if strings.HasSuffix(e, ".git/") && strings.HasPrefix(e, "repos/") {
				// Depth check: exactly <owner>/<repo>.git/ (2 slashes after "repos/")
				rest := strings.TrimPrefix(e, "repos/")
				if strings.Count(rest, "/") == 2 {
					repoDirs++
				}
			}
		}
		if repoDirs == 0 {
			r.add(Check{Name: "dump tarball: repo count", Status: "WARN",
				Detail: "no repos/<owner>/<name>.git/ directories detected"})
		} else {
			diff := repoDirs - len(m.Repos)
			switch {
			case diff == 0:
				r.add(Check{Name: "dump tarball: repo count", Status: "PASS",
					Detail: fmt.Sprintf("%d repos match manifest", repoDirs)})
			case abs(diff) <= 2:
				r.add(Check{Name: "dump tarball: repo count", Status: "WARN",
					Detail: fmt.Sprintf("%d repos in archive vs %d in manifest (diff %+d; small drift tolerable)",
						repoDirs, len(m.Repos), diff)})
			default:
				r.add(Check{Name: "dump tarball: repo count", Status: "FAIL",
					Detail: fmt.Sprintf("%d repos in archive vs %d in manifest (diff %+d)",
						repoDirs, len(m.Repos), diff)})
			}
		}
	}
}

func checkNativeDB(r *Result, cfg *config.Config) {
	if cfg.Options.SkipNativeDB {
		r.add(Check{Name: "native db dump", Status: "WARN",
			Detail: "not present; options.skip_native_db is true"})
		return
	}
	var path string
	switch cfg.Source.DB.Dialect {
	case "postgres":
		path = filepath.Join(cfg.WorkDir, "gitea.dump")
	case "mysql":
		path = filepath.Join(cfg.WorkDir, "gitea.sql")
	case "sqlite3":
		path = filepath.Join(cfg.WorkDir, "gitea.sqlite")
	default:
		r.add(Check{Name: "native db dump", Status: "WARN",
			Detail: "unknown dialect " + cfg.Source.DB.Dialect})
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		r.add(Check{Name: "native db dump", Status: "FAIL", Detail: err.Error()})
		return
	}
	if fi.Size() < 512 {
		r.add(Check{Name: "native db dump", Status: "FAIL",
			Detail: fmt.Sprintf("%s is suspiciously small (%d bytes)", path, fi.Size())})
		return
	}
	if err := smokeTestDump(cfg.Source.DB.Dialect, path); err != nil {
		r.add(Check{Name: "native db dump", Status: "FAIL",
			Detail: fmt.Sprintf("%s: %v", path, err)})
		return
	}
	r.add(Check{Name: "native db dump", Status: "PASS",
		Detail: fmt.Sprintf("%s %s (%s)", cfg.Source.DB.Dialect, path, humanBytes(fi.Size()))})
}

func checkS3Mirror(r *Result, cfg *config.Config) {
	if cfg.Source.Storage == nil || cfg.Source.Storage.Type != "s3" {
		return // no S3 storage configured; nothing to verify
	}
	if cfg.Options.SkipS3Mirror {
		r.add(Check{Name: "s3 mirror", Status: "WARN",
			Detail: "not present; options.skip_s3_mirror is true"})
		return
	}
	s3Dir := filepath.Join(cfg.WorkDir, "s3")
	fi, err := os.Stat(s3Dir)
	if err != nil {
		r.add(Check{Name: "s3 mirror", Status: "FAIL",
			Detail: s3Dir + ": " + err.Error()})
		return
	}
	if !fi.IsDir() {
		r.add(Check{Name: "s3 mirror", Status: "FAIL",
			Detail: s3Dir + " is not a directory"})
		return
	}
	r.add(Check{Name: "s3 mirror", Status: "PASS",
		Detail: s3Dir + " present"})
}

// -- helpers -----------------------------------------------------------------

// listTarball shells out to `tar -t` with the right decompressor flag and
// returns the list of entry names. Tolerates empty lines, strips leading
// "./" prefixes for easier prefix matching.
func listTarball(path, ext string) ([]string, error) {
	var cmd *exec.Cmd
	switch ext {
	case "tar.zst":
		cmd = exec.Command("tar", "--zstd", "-tf", path)
	case "tar.gz":
		cmd = exec.Command("tar", "-z", "-tf", path)
	case "tar":
		cmd = exec.Command("tar", "-tf", path)
	case "zip":
		cmd = exec.Command("unzip", "-l", path)
	default:
		return nil, fmt.Errorf("unsupported dump_format %q", ext)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w (%s)", cmd.Path, err, bytes.TrimSpace(out))
	}
	var entries []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "./")
		if line == "" {
			continue
		}
		entries = append(entries, line)
	}
	return entries, nil
}

// smokeTestDump does a cheap "is this file plausibly a DB dump" check
// per-dialect. No full restoration; just enough to catch truncation
// and wrong-format artifacts.
func smokeTestDump(dialect, path string) error {
	switch dialect {
	case "postgres":
		if _, err := exec.LookPath("pg_restore"); err != nil {
			// No client installed; fall back to header inspection.
			return checkHeader(path, "PGDMP")
		}
		cmd := exec.Command("pg_restore", "--list", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("pg_restore --list: %w (%s)",
				err, bytes.TrimSpace(out))
		}
		return nil
	case "mysql":
		// Expect the dump to start with a MySQL-style header comment.
		return checkHeader(path, "-- MySQL dump")
	case "sqlite3":
		return checkHeader(path, "SQLite format 3")
	}
	return nil
}

func checkHeader(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, len(want)+16)
	n, _ := f.Read(buf)
	if !bytes.Contains(buf[:n], []byte(want)) {
		return fmt.Errorf("expected header %q not found in first %d bytes",
			want, n)
	}
	return nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func humanBytes(n int64) string {
	const (
		k = int64(1024)
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

// Debug helper: prints the JSON of a manifest to stderr for eyeballing.
// Not wired into the cobra subcommand; kept for one-off debugging.
func dumpJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(os.Stderr, string(b))
}
