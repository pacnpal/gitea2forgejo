package config

import (
	"os"
	"path/filepath"
	"testing"
)

const minimalYAML = `
source:
  url: https://gitea.example.com
  admin_token: env:SRC_TOKEN
  config_file: /etc/gitea/app.ini
  data_dir: /var/lib/gitea
  db:
    dialect: postgres
    dsn: env:SRC_DSN
target:
  url: https://forgejo.example.com
  admin_token: env:TGT_TOKEN
  config_file: /etc/forgejo/app.ini
  data_dir: /var/lib/forgejo
  db:
    dialect: postgres
    dsn: env:TGT_DSN
work_dir: /tmp/work
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_expandsEnv(t *testing.T) {
	t.Setenv("SRC_TOKEN", "srctok")
	t.Setenv("TGT_TOKEN", "tgttok")
	t.Setenv("SRC_DSN", "postgres://src")
	t.Setenv("TGT_DSN", "postgres://tgt")
	c, err := Load(writeTemp(t, minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Source.AdminToken != "srctok" || c.Target.AdminToken != "tgttok" {
		t.Fatalf("env expansion failed: %+v", c)
	}
	if c.Source.DB.DSN != "postgres://src" || c.Target.DB.DSN != "postgres://tgt" {
		t.Fatalf("dsn expansion failed: %+v", c)
	}
	if c.Options.DumpFormat != "tar.zst" {
		t.Fatalf("default dump format: %q", c.Options.DumpFormat)
	}
}

func TestLoad_missingRequired(t *testing.T) {
	_, err := Load(writeTemp(t, `source: {}` + "\n" + `target: {}` + "\n"))
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoad_badDialect(t *testing.T) {
	body := minimalYAML + "\n" + `options:
  dump_format: rar
`
	t.Setenv("SRC_TOKEN", "t")
	t.Setenv("TGT_TOKEN", "t")
	t.Setenv("SRC_DSN", "d")
	t.Setenv("TGT_DSN", "d")
	_, err := Load(writeTemp(t, body))
	if err == nil {
		t.Fatal("expected dump_format error")
	}
}

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	got := expandHome("~/foo/bar")
	want := "/home/tester/foo/bar"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
