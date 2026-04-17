package dump

import (
	"strings"
	"testing"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestBuildGiteaDumpCmd_basic(t *testing.T) {
	src := config.Instance{
		Binary:        "gitea",
		ConfigFile:    "/etc/gitea/app.ini",
		RemoteWorkDir: "/tmp/work",
	}
	cmd := buildGiteaDumpCmd(src, "/tmp/work/gitea-dump.tar.zst", "tar.zst")
	for _, want := range []string{
		"'gitea'", "dump",
		"--config '/etc/gitea/app.ini'",
		"--file '/tmp/work/gitea-dump.tar.zst'",
		"--type 'tar.zst'",
		"--tempdir '/tmp/work'",
		"--skip-log", "--skip-index",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cmd missing %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "sudo") {
		t.Errorf("unexpected sudo when RunAs empty: %s", cmd)
	}
}

func TestBuildGiteaDumpCmd_sudo(t *testing.T) {
	src := config.Instance{
		Binary:        "/usr/local/bin/gitea",
		ConfigFile:    "/etc/gitea/app.ini",
		RemoteWorkDir: "/tmp",
		RunAs:         "gitea",
	}
	cmd := buildGiteaDumpCmd(src, "/tmp/d.tar", "tar")
	if !strings.HasPrefix(cmd, "sudo -u 'gitea' -- ") {
		t.Errorf("expected sudo prefix, got: %s", cmd)
	}
}

func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"plain":      "'plain'",
		"has space":  "'has space'",
		"it's fine":  `'it'\''s fine'`,
		"":           "''",
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q)=%q want %q", in, got, want)
		}
	}
}
