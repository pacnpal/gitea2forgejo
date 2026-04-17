package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetAcceptMissingSecretKey_insertsUnderOptions(t *testing.T) {
	p := writeTempConfig(t, `source:
  url: https://g.example.com
target:
  url: https://f.example.com
options:
  dump_format: tar.zst
`)
	if err := setAcceptMissingSecretKey(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	out := string(got)
	if !strings.Contains(out, "options:\n  accept_missing_secret_key: true") {
		t.Errorf("expected key inserted right after options:\n---\n%s\n---", out)
	}
	// dump_format should still be there.
	if !strings.Contains(out, "dump_format: tar.zst") {
		t.Errorf("lost existing dump_format key:\n%s", out)
	}
}

func TestSetAcceptMissingSecretKey_appendsNewBlock(t *testing.T) {
	p := writeTempConfig(t, "source: {url: x}\ntarget: {url: y}\n")
	if err := setAcceptMissingSecretKey(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), "options:\n  accept_missing_secret_key: true") {
		t.Errorf("expected new options block appended:\n%s", string(got))
	}
}

func TestSetAcceptMissingSecretKey_updatesExistingKey(t *testing.T) {
	p := writeTempConfig(t, `options:
  accept_missing_secret_key: false
  dump_format: tar.zst
`)
	if err := setAcceptMissingSecretKey(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), "accept_missing_secret_key: true") {
		t.Errorf("expected value flipped to true:\n%s", string(got))
	}
	if strings.Contains(string(got), "accept_missing_secret_key: false") {
		t.Errorf("old false still present:\n%s", string(got))
	}
	// No duplicate lines.
	if strings.Count(string(got), "accept_missing_secret_key") != 1 {
		t.Errorf("expected exactly one accept_missing_secret_key line:\n%s", string(got))
	}
}
