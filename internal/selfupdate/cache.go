package selfupdate

import (
	"os"
	"path/filepath"
	"time"
)

// CheckTTL is how often the auto-check runs. Anything more frequent hits
// the GitHub API unnecessarily and slows every invocation.
const CheckTTL = 6 * time.Hour

// cacheFile returns ~/.cache/gitea2forgejo/last-checked.
func cacheFile() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "gitea2forgejo", "last-checked")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "gitea2forgejo", "last-checked")
}

// ShouldCheck reports whether enough time has passed since the last
// auto-check to justify hitting GitHub again.
func ShouldCheck() bool {
	p := cacheFile()
	if p == "" {
		return true
	}
	fi, err := os.Stat(p)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) > CheckTTL
}

// RecordCheck touches the cache file so subsequent runs within CheckTTL
// skip the API call. Failure is non-fatal — we'll just check again sooner.
func RecordCheck() {
	p := cacheFile()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	now := time.Now()
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		_ = f.Close()
		_ = os.Chtimes(p, now, now)
	}
}
