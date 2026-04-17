package restore

import (
	"log/slog"
	"strings"
	"testing"

	"gopkg.in/ini.v1"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestApplyRewrites_domainAndActions(t *testing.T) {
	src := `
[server]
DOMAIN = gitea.example.com
SSH_DOMAIN = gitea.example.com
ROOT_URL = https://gitea.example.com/
[security]
SECRET_KEY = keep-me
INTERNAL_TOKEN = keep-me
`
	f, err := ini.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Target: config.Instance{URL: "https://forgejo.example.com"},
	}
	if err := applyRewrites(f, cfg, slog.Default()); err != nil {
		t.Fatal(err)
	}
	got, _ := f.GetSection("server")
	if v := got.Key("DOMAIN").Value(); v != "forgejo.example.com" {
		t.Errorf("DOMAIN = %q", v)
	}
	if v := got.Key("ROOT_URL").Value(); v != "https://forgejo.example.com/" {
		t.Errorf("ROOT_URL = %q", v)
	}
	sec, _ := f.GetSection("security")
	if v := sec.Key("SECRET_KEY").Value(); v != "keep-me" {
		t.Errorf("SECRET_KEY was mutated: %q", v)
	}
	if v := sec.Key("COOKIE_REMEMBER_NAME").Value(); v != "gitea_incredible" {
		t.Errorf("COOKIE_REMEMBER_NAME = %q", v)
	}
	act, _ := f.GetSection("actions")
	if v := act.Key("DEFAULT_ACTIONS_URL").Value(); v != "https://code.forgejo.org" {
		t.Errorf("DEFAULT_ACTIONS_URL = %q", v)
	}
}

func TestApplyRewrites_dataDirSwap(t *testing.T) {
	src := `
[attachment]
PATH = /var/lib/gitea/data/attachments
[lfs]
PATH = /var/lib/gitea/data/lfs
[other]
UNRELATED = /var/lib/gitea2/foo
`
	f, err := ini.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Source: config.Instance{DataDir: "/var/lib/gitea"},
		Target: config.Instance{URL: "https://t.example.com", DataDir: "/var/lib/forgejo"},
	}
	if err := applyRewrites(f, cfg, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if v := f.Section("attachment").Key("PATH").Value(); !strings.HasPrefix(v, "/var/lib/forgejo/") {
		t.Errorf("attachment PATH not rewritten: %q", v)
	}
	if v := f.Section("lfs").Key("PATH").Value(); !strings.HasPrefix(v, "/var/lib/forgejo/") {
		t.Errorf("lfs PATH not rewritten: %q", v)
	}
	// /var/lib/gitea2 must NOT be rewritten.
	if v := f.Section("other").Key("UNRELATED").Value(); v != "/var/lib/gitea2/foo" {
		t.Errorf("unrelated path spuriously rewritten: %q", v)
	}
}

func TestContainsPath_boundaries(t *testing.T) {
	cases := []struct {
		s, p string
		want bool
	}{
		{"/var/lib/gitea/data", "/var/lib/gitea", true},
		{"/var/lib/gitea2/data", "/var/lib/gitea", false},
		{"prefix /var/lib/gitea/data", "/var/lib/gitea", true},
		{"nothingof sort", "/var/lib/gitea", false},
		{"/var/lib/gitea", "/var/lib/gitea", true},
	}
	for _, c := range cases {
		if got := containsPath(c.s, c.p); got != c.want {
			t.Errorf("containsPath(%q,%q)=%v want %v", c.s, c.p, got, c.want)
		}
	}
}
