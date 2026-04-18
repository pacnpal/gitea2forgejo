// Package selfupdate queries GitHub Releases for a newer version of
// gitea2forgejo and replaces the running binary when the operator accepts.
//
// Scope is deliberately narrow:
//
//   - Only downloads from https://github.com/pacnpal/gitea2forgejo/releases.
//   - Only compares against github.com's /releases/latest tag.
//   - Never runs automatically without either a TTY prompt (Y/n) or the
//     explicit `gitea2forgejo update` subcommand.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
)

const (
	Repo       = "pacnpal/gitea2forgejo"
	latestAPI  = "https://api.github.com/repos/" + Repo + "/releases/latest"
	userAgent  = "gitea2forgejo-self-update"
	httpClient = 6 * time.Second
	// listLimit caps how many historical releases LatestWithAsset walks
	// before giving up. Well above any plausible backlog of partial SLSA
	// uploads — if the current platform hasn't had a binary in 30
	// releases, the operator has a bigger problem than self-update.
	listLimit = 30
)

// Release is the subset of the GitHub Releases API response we use.
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	Assets      []Asset `json:"assets"`
}

// Asset is a single uploaded file attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// HasAsset reports whether the release carries an asset with exactly
// the given name.
func (r *Release) HasAsset(name string) bool {
	for _, a := range r.Assets {
		if a.Name == name {
			return true
		}
	}
	return false
}

// CurrentAssetName returns the binary filename uploaded by the SLSA
// matrix build for this process's GOOS / GOARCH. Windows adds .exe.
func CurrentAssetName() string {
	name := fmt.Sprintf("gitea2forgejo-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Latest returns /releases/latest as-is (may be missing the current
// platform's asset — prefer LatestWithAsset for the update path).
// Kept for callers that want GitHub's notion of "latest" verbatim.
// 6-second timeout, one retry on 5xx/network, Cache-Control: no-cache.
func Latest(ctx context.Context) (*Release, error) {
	var r Release
	if err := fetchJSON(ctx, latestAPI, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ByTag returns the release with the given tag (e.g. "v0.2.15"). Used
// by `update --to <tag>` to bypass /releases/latest when the operator
// knows exactly which version they want.
func ByTag(ctx context.Context, tag string) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", Repo, tag)
	var r Release
	if err := fetchJSON(ctx, url, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// LatestWithAsset walks /releases newest-first and returns the first
// non-draft, non-prerelease that actually has an asset matching
// assetName. Also returns the tags that were skipped along the way so
// the caller can surface them ("skipped v0.2.18 — missing asset for
// your platform").
//
// Handles the case where a release exists but the SLSA matrix build
// only finished uploading some architectures: the partial release
// stays visible to `/releases/latest`, so Apply would fail with HTTP
// 404 on the missing binary. Falling back to the previous good
// release keeps update monotonic and automatic.
func LatestWithAsset(ctx context.Context, assetName string) (release *Release, skipped []string, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", Repo, listLimit)
	var list []Release
	if err := fetchJSON(ctx, url, &list); err != nil {
		return nil, nil, err
	}
	for i := range list {
		r := &list[i]
		if r.Draft || r.Prerelease {
			continue
		}
		if r.HasAsset(assetName) {
			return r, skipped, nil
		}
		skipped = append(skipped, r.TagName)
	}
	return nil, skipped, fmt.Errorf("none of the %d most recent releases carry asset %q — check the workflow runs at https://github.com/%s/actions", len(list), assetName, Repo)
}

func fetchJSON(ctx context.Context, url string, out any) error {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		retryable, err := fetchJSONOnce(ctx, url, out)
		if err == nil {
			return nil
		}
		last = err
		if !retryable {
			return err
		}
	}
	return last
}

func fetchJSONOnce(ctx context.Context, url string, out any) (retryable bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, httpClient)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, fmt.Errorf("github api %s: not found (HTTP 404)", url)
	}
	if resp.StatusCode >= 500 {
		return true, fmt.Errorf("github api %s: HTTP %d", url, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github api %s: HTTP %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, err
	}
	return false, nil
}

// IsNewer reports whether `latest` is a strictly newer version than
// `current`. Dev builds (git describe output like "v0.2.6-7-g353ce2e")
// parse as less than the plain "v0.2.6" due to hashicorp/go-version's
// pre-release handling — which is the desired behavior: a dev build
// running between releases should be prompted to upgrade when a real
// tag lands.
func IsNewer(current, latest string) (bool, error) {
	cv, err := version.NewVersion(strings.TrimPrefix(current, "v"))
	if err != nil {
		// Unparseable current (e.g. "dev") — treat as always-outdated.
		return true, nil
	}
	lv, err := version.NewVersion(strings.TrimPrefix(latest, "v"))
	if err != nil {
		return false, fmt.Errorf("parse latest %q: %w", latest, err)
	}
	return lv.GreaterThan(cv), nil
}

// Apply downloads the release binary matching the current GOOS/GOARCH
// from `tag`'s assets and atomically replaces the currently-running
// executable.
//
// On Linux + macOS, os.Rename across a running binary is safe because
// the open inode stays valid until the process exits. On Windows the
// running .exe is locked by the kernel — Apply returns an instructive
// error on that platform (operator can re-download manually or use the
// release binary's update button).
func Apply(ctx context.Context, tag string, log func(format string, a ...any)) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve current binary: %w", err)
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("self-update not implemented for Windows — download the new release manually from https://github.com/%s/releases/latest", Repo)
	}

	assetName := CurrentAssetName()
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", Repo, tag, assetName)

	log("  downloading %s", url)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d at %s", resp.StatusCode, url)
	}

	// Stage a tempfile in the SAME directory as the target so os.Rename
	// is guaranteed to be same-filesystem.
	dir := filepath.Dir(self)
	tmp, err := os.CreateTemp(dir, ".gitea2forgejo-new-*")
	if err != nil {
		return fmt.Errorf("create tempfile in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // safety: gc if we bail before Rename

	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tempfile: %w", err)
	}
	log("  downloaded %d MB", n/(1<<20))

	// Cross-platform sanity: bail early if the file is suspiciously small.
	if n < 1<<20 {
		return fmt.Errorf("downloaded binary is only %d bytes — aborting", n)
	}

	log("  installing → %s", self)
	if err := os.Rename(tmpPath, self); err != nil {
		return fmt.Errorf("install: %w (is the target path writable?)", err)
	}

	// macOS: downloaded binaries pick up com.apple.quarantine, which
	// makes Gatekeeper refuse to run them on the next invocation. Strip
	// it + ad-hoc codesign so the binary is immediately usable. These
	// commands are best-effort — non-fatal if they fail (the user can
	// apply them manually per the README).
	if runtime.GOOS == "darwin" {
		macosPostInstall(self, log)
	}
	return nil
}

// macosPostInstall removes the Gatekeeper quarantine attribute and
// re-applies an ad-hoc signature so the newly-installed binary runs
// without the "cannot be opened" dialog. Both commands are available
// in the macOS base install (no Xcode required).
func macosPostInstall(binary string, log func(format string, a ...any)) {
	if err := exec.Command("xattr", "-dr", "com.apple.quarantine", binary).Run(); err != nil {
		log("  note: xattr -dr com.apple.quarantine failed (%v) — run manually if Gatekeeper blocks", err)
	} else {
		log("  macOS: cleared com.apple.quarantine attribute")
	}
	if err := exec.Command("codesign", "--force", "--sign", "-", binary).Run(); err != nil {
		log("  note: ad-hoc codesign failed (%v) — run `codesign --force --sign - %s` manually if needed", err, binary)
	} else {
		log("  macOS: ad-hoc signed (survives xattr reset)")
	}
}
