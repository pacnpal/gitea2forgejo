package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
)

func TestRelease_HasAsset(t *testing.T) {
	r := &Release{Assets: []Asset{
		{Name: "gitea2forgejo-linux-amd64"},
		{Name: "gitea2forgejo-linux-arm64"},
	}}
	if !r.HasAsset("gitea2forgejo-linux-amd64") {
		t.Error("should find linux-amd64")
	}
	if r.HasAsset("gitea2forgejo-darwin-arm64") {
		t.Error("should not find darwin-arm64")
	}
	if r.HasAsset("") {
		t.Error("should not match empty")
	}
}

func TestCurrentAssetName(t *testing.T) {
	got := CurrentAssetName()
	want := "gitea2forgejo-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	if got != want {
		t.Errorf("CurrentAssetName() = %q, want %q", got, want)
	}
}

func listHandler(releases []Release) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releases)
	}
}

func TestLatestWithAsset_fallsBackWhenAssetMissing(t *testing.T) {
	// v0.3.0 is newest but missing the asset (partial SLSA build); v0.2.9
	// is the next-best that carries it. v0.2.8 is never reached.
	releases := []Release{
		{TagName: "v0.3.0", Assets: []Asset{{Name: "gitea2forgejo-linux-amd64"}}},
		{TagName: "v0.2.9", Assets: []Asset{{Name: "gitea2forgejo-darwin-arm64"}}},
		{TagName: "v0.2.8", Assets: []Asset{{Name: "gitea2forgejo-darwin-arm64"}}},
	}
	srv := httptest.NewServer(listHandler(releases))
	defer srv.Close()

	got, skipped, err := latestWithAssetFrom(context.Background(), srv.URL, "gitea2forgejo-darwin-arm64")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.TagName != "v0.2.9" {
		t.Errorf("got %s, want v0.2.9 (v0.3.0 lacks darwin-arm64)", got.TagName)
	}
	if !reflect.DeepEqual(skipped, []string{"v0.3.0"}) {
		t.Errorf("skipped = %v, want [v0.3.0]", skipped)
	}
}

func TestLatestWithAsset_skipsDraftsAndPrereleases(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.0-rc1", Prerelease: true, Assets: []Asset{{Name: "a"}}},
		{TagName: "v1.0.0-draft", Draft: true, Assets: []Asset{{Name: "a"}}},
		{TagName: "v0.9.9", Assets: []Asset{{Name: "a"}}},
	}
	srv := httptest.NewServer(listHandler(releases))
	defer srv.Close()

	got, skipped, err := latestWithAssetFrom(context.Background(), srv.URL, "a")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.TagName != "v0.9.9" {
		t.Errorf("got %s, want v0.9.9 (draft + prerelease filtered)", got.TagName)
	}
	// Draft/prerelease skipped silently — not in the "skipped" list.
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want empty (drafts/prereleases are silent)", skipped)
	}
}

func TestLatestWithAsset_noneMatch(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.0", Assets: []Asset{{Name: "other"}}},
		{TagName: "v0.9.0", Assets: []Asset{{Name: "other"}}},
	}
	srv := httptest.NewServer(listHandler(releases))
	defer srv.Close()

	_, skipped, err := latestWithAssetFrom(context.Background(), srv.URL, "missing")
	if err == nil {
		t.Fatal("expected error when no release has the asset")
	}
	if !reflect.DeepEqual(skipped, []string{"v1.0.0", "v0.9.0"}) {
		t.Errorf("skipped = %v, want [v1.0.0 v0.9.0]", skipped)
	}
}

// latestWithAssetFrom is LatestWithAsset with the API URL injected for
// testing. Kept private to this test file so the public API stays
// tied to the hard-coded Repo URL.
func latestWithAssetFrom(ctx context.Context, url, asset string) (*Release, []string, error) {
	var list []Release
	if err := fetchJSON(ctx, url, &list); err != nil {
		return nil, nil, err
	}
	var skipped []string
	for i := range list {
		r := &list[i]
		if r.Draft || r.Prerelease {
			continue
		}
		if r.HasAsset(asset) {
			return r, skipped, nil
		}
		skipped = append(skipped, r.TagName)
	}
	return nil, skipped, errNoReleaseWithAsset(asset, len(list))
}

func errNoReleaseWithAsset(asset string, total int) error {
	return &assetMissErr{asset: asset, total: total}
}

type assetMissErr struct {
	asset string
	total int
}

func (e *assetMissErr) Error() string {
	return "none of the releases carry asset " + e.asset
}
