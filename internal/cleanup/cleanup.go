// Package cleanup removes the on-disk artifacts left behind by
// `gitea2forgejo dump` (local work_dir) and the remote source scratch
// directory (via SSH + docker exec when Docker was used).
//
// Run after a successful migration to reclaim space. Safe to run when
// there's nothing to clean up — missing files are a no-op.
package cleanup

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// Options controls cleanup behavior.
type Options struct {
	Force      bool // skip interactive confirmation
	KeepLocal  bool // keep work_dir contents; only clean the source host
	KeepRemote bool // keep the source host scratch; only clean work_dir
}

// Run inventories the artifacts, prompts the operator (unless Force),
// and deletes them.
func Run(cfg *config.Config, opt Options, log *slog.Logger) error {
	local := listLocalArtifacts(cfg)
	remoteScratch, remoteSize := resolveRemoteScratch(cfg, log)

	if len(local) == 0 && remoteScratch == "" {
		log.Info("nothing to clean up", "work_dir", cfg.WorkDir)
		return nil
	}

	// Print an itemized summary.
	fmt.Fprintln(os.Stderr, "The following will be removed:")
	if !opt.KeepLocal {
		for _, p := range local {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
	}
	if !opt.KeepRemote && remoteScratch != "" {
		fmt.Fprintf(os.Stderr, "  %s (remote, container %s, %s)\n",
			remoteScratch, cfg.Source.Docker.Container, humanBytes(remoteSize))
	}

	if !opt.Force && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "\nProceed? [y/N]: ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" &&
			strings.ToLower(strings.TrimSpace(line)) != "yes" {
			return fmt.Errorf("cancelled")
		}
	}

	// Local work_dir contents.
	if !opt.KeepLocal {
		for _, p := range local {
			if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
				log.Warn("remove failed", "path", p, "err", err)
				continue
			}
			log.Info("removed", "path", p)
		}
	}

	// Remote source scratch.
	if !opt.KeepRemote && remoteScratch != "" {
		if err := removeRemoteScratch(cfg, remoteScratch, log); err != nil {
			return fmt.Errorf("remove remote scratch: %w", err)
		}
	}
	return nil
}

// listLocalArtifacts returns all known work_dir paths that dump / restore
// create. Missing entries are silently skipped.
func listLocalArtifacts(cfg *config.Config) []string {
	ext := cfg.Options.DumpFormat
	candidates := []string{
		filepath.Join(cfg.WorkDir, "gitea-dump."+ext),
		filepath.Join(cfg.WorkDir, "source-manifest.json"),
		filepath.Join(cfg.WorkDir, "gitea.sqlite"),
		filepath.Join(cfg.WorkDir, "gitea.dump"),
		filepath.Join(cfg.WorkDir, "gitea.sql"),
		filepath.Join(cfg.WorkDir, "preflight-report.md"),
		filepath.Join(cfg.WorkDir, "verify-dump-report.md"),
		filepath.Join(cfg.WorkDir, "target-app.ini"),
		filepath.Join(cfg.WorkDir, "extracted"),
		filepath.Join(cfg.WorkDir, "s3"),
	}
	var out []string
	for _, p := range candidates {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// resolveRemoteScratch inspects the symlink at work_dir/gitea-dump.<ext>
// (if present) to discover the remote source scratch path. Returns the
// directory portion + total byte size for display.
func resolveRemoteScratch(cfg *config.Config, log *slog.Logger) (dir string, size int64) {
	ext := cfg.Options.DumpFormat
	linkPath := filepath.Join(cfg.WorkDir, "gitea-dump."+ext)
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", 0
	}
	// Target is a HOST path (the tool operates on host paths). When
	// source is Docker, translate it back to the container path so we
	// can rm -rf via `docker exec`.
	dir = path.Dir(target)
	if fi, err := os.Stat(target); err == nil {
		size = fi.Size()
	}
	return dir, size
}

// removeRemoteScratch removes the discovered scratch directory on the
// source host. Uses docker exec when Docker is configured so the remove
// runs as the container's gitea user (avoiding permission issues on
// files the container owns).
func removeRemoteScratch(cfg *config.Config, hostDir string, log *slog.Logger) error {
	if cfg.Source.SSH == nil {
		return fmt.Errorf("source.ssh not configured; cannot reach %s", hostDir)
	}
	cli, err := remote.Dial(cfg.Source.SSH)
	if err != nil {
		return err
	}
	defer cli.Close()

	// Prefer docker exec when source is containerized — the container
	// user owns the files, and this matches the path the symlink was
	// created against (container-internal path).
	if cfg.Source.Docker != nil && cfg.Source.Docker.Container != "" {
		containerDir := cfg.Source.Docker.HostToContainer(hostDir)
		if containerDir != "" {
			cmd := fmt.Sprintf("%s exec %s rm -rf %s",
				shQuote(orDefault(cfg.Source.Docker.Binary, "docker")),
				shQuote(cfg.Source.Docker.Container),
				shQuote(containerDir))
			log.Info("removing remote scratch (via docker exec)", "path", containerDir)
			if out, err := cli.Run(cmd); err != nil {
				return fmt.Errorf("%s: %w (%s)", cmd, err, string(out))
			}
			return nil
		}
	}

	// Bare-metal fallback: rm directly on the host.
	log.Info("removing remote scratch (host)", "path", hostDir)
	if out, err := cli.Run("rm -rf " + shQuote(hostDir)); err != nil {
		return fmt.Errorf("rm -rf %s: %w (%s)", hostDir, err, string(out))
	}
	return nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
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
