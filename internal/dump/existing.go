package dump

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// existingDumpAction is the operator's choice when a prior dump is
// detected in work_dir. Returned by checkExistingDump and consumed by
// dump.Run to short-circuit the expensive stages.
type existingDumpAction int

const (
	actionProceed existingDumpAction = iota // no prior dump, run normally
	actionReuse                             // keep existing artifacts, skip regeneration
	actionReplace                           // remove existing and regenerate
)

// checkExistingDump looks at work_dir for artifacts from a prior run:
//
//   - gitea-dump.<ext> (the tarball or a symlink to it)
//   - gitea.sqlite / gitea.dump / gitea.sql (native DB dump)
//   - source-manifest.json
//
// If any exist and stdin is a TTY, the operator is prompted to
// reuse / replace / cancel. On non-TTY stdin we default to reuse
// (idempotent-friendly for CI).
//
// Returns actionProceed when nothing exists.
func checkExistingDump(cfg *config.Config, log *slog.Logger) (existingDumpAction, error) {
	existing := listExistingArtifacts(cfg)
	if len(existing) == 0 {
		return actionProceed, nil
	}

	// Brief, human summary of what's already there.
	for _, f := range existing {
		log.Info("existing artifact", "path", f.path, "size", humanBytes(f.size), "age", humanAge(f.age))
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		log.Info("non-TTY stdin; reusing existing dump artifacts")
		return actionReuse, nil
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "A prior dump already exists in work_dir.")
	fmt.Fprintln(os.Stderr, "  1) Reuse — skip the dump stage, proceed with existing artifacts")
	fmt.Fprintln(os.Stderr, "  2) Replace — delete existing and regenerate (sources may differ!)")
	fmt.Fprintln(os.Stderr, "  3) Cancel")
	fmt.Fprint(os.Stderr, "Choose [1/2/3]: ")

	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return actionProceed, fmt.Errorf("read choice: %w", err)
	}
	switch strings.TrimSpace(line) {
	case "1", "":
		return actionReuse, nil
	case "2":
		if err := deleteExistingArtifacts(existing, log); err != nil {
			return actionProceed, err
		}
		return actionReplace, nil
	case "3":
		return actionProceed, errors.New("cancelled by operator")
	default:
		return actionProceed, fmt.Errorf("unrecognized choice %q", strings.TrimSpace(line))
	}
}

// artifact describes one on-disk file from a prior dump.
type artifact struct {
	path string
	size int64
	age  time.Duration
}

func listExistingArtifacts(cfg *config.Config) []artifact {
	var out []artifact
	candidates := []string{
		filepath.Join(cfg.WorkDir, "gitea-dump."+cfg.Options.DumpFormat),
		filepath.Join(cfg.WorkDir, "source-manifest.json"),
		filepath.Join(cfg.WorkDir, "gitea.sqlite"),
		filepath.Join(cfg.WorkDir, "gitea.dump"),
		filepath.Join(cfg.WorkDir, "gitea.sql"),
	}
	for _, p := range candidates {
		fi, err := os.Lstat(p) // Lstat: don't follow symlink (lets us detect dangling)
		if err != nil {
			continue
		}
		// Follow symlinks for size/target existence.
		realFI, err := os.Stat(p)
		size := int64(0)
		if err == nil {
			size = realFI.Size()
		}
		out = append(out, artifact{
			path: p,
			size: size,
			age:  time.Since(fi.ModTime()),
		})
	}
	return out
}

func deleteExistingArtifacts(a []artifact, log *slog.Logger) error {
	for _, f := range a {
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", f.path, err)
		}
		log.Info("removed", "path", f.path)
	}
	// Also remove the extracted/ subdir if any run of `restore` left it.
	return nil
}

func humanAge(d time.Duration) string {
	switch {
	case d > 24*time.Hour:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	case d > time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	case d > time.Minute:
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
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
