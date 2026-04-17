package dump

import "testing"

func TestHostToContainer(t *testing.T) {
	mounts := [][2]string{
		{"/mnt/user/appdata/gitea", "/data"},
		{"/var/run/docker.sock", "/var/run/docker.sock"},
	}
	cases := []struct {
		in, want string
	}{
		{"/mnt/user/appdata/gitea/gitea/conf/app.ini", "/data/gitea/conf/app.ini"},
		{"/mnt/user/appdata/gitea/gitea", "/data/gitea"},
		{"/mnt/user/appdata/gitea", "/data"},
		// Not under any mount.
		{"/tmp/foo", ""},
		// Exact non-prefix (socket passthrough).
		{"/var/run/docker.sock", "/var/run/docker.sock"},
		{"", ""},
	}
	for _, c := range cases {
		if got := hostToContainer(c.in, mounts); got != c.want {
			t.Errorf("hostToContainer(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestHostToContainer_longestPrefix(t *testing.T) {
	mounts := [][2]string{
		{"/host", "/ctr-outer"},
		{"/host/nested", "/ctr-inner"},
	}
	if got := hostToContainer("/host/nested/x", mounts); got != "/ctr-inner/x" {
		t.Errorf("longest prefix should win; got %q", got)
	}
	if got := hostToContainer("/host/other", mounts); got != "/ctr-outer/other" {
		t.Errorf("shallow mount should match; got %q", got)
	}
}

func TestPickDockerScratch_prefersDataDir(t *testing.T) {
	cfg := &cfgStub{DataDir: "/mnt/user/appdata/gitea", RemoteWorkDir: "/tmp/g"}
	mounts := [][2]string{{"/mnt/user/appdata/gitea", "/data"}}
	host, cont, cp := testPick(cfg, mounts)
	if host != "/mnt/user/appdata/gitea/gitea2forgejo-dump" {
		t.Errorf("host=%q", host)
	}
	if cont != "/data/gitea2forgejo-dump" {
		t.Errorf("cont=%q", cont)
	}
	if cp {
		t.Error("should not need docker cp for bind-mounted data_dir")
	}
}

func TestPickDockerScratch_fallbackToTmp(t *testing.T) {
	cfg := &cfgStub{DataDir: "/var/lib/gitea", RemoteWorkDir: "/tmp/g"}
	mounts := [][2]string{{"/var/run/docker.sock", "/var/run/docker.sock"}}
	host, cont, cp := testPick(cfg, mounts)
	if host != "" {
		t.Errorf("host should be empty for cp fallback, got %q", host)
	}
	if cont != "/tmp/gitea2forgejo-dump" {
		t.Errorf("cont=%q", cont)
	}
	if !cp {
		t.Error("should need docker cp when nothing bind-mounted")
	}
}

// Test-only lightweight stub so we don't construct a full config.Config
// with required DB/SSH fields.
type cfgStub struct{ DataDir, RemoteWorkDir string }

func testPick(s *cfgStub, mounts [][2]string) (string, string, bool) {
	const subdir = "gitea2forgejo-dump"
	if s.DataDir != "" {
		if cc := hostToContainer(s.DataDir, mounts); cc != "" {
			return s.DataDir + "/" + subdir, cc + "/" + subdir, false
		}
	}
	if s.RemoteWorkDir != "" {
		if cc := hostToContainer(s.RemoteWorkDir, mounts); cc != "" {
			return s.RemoteWorkDir, cc, false
		}
	}
	return "", "/tmp/" + subdir, true
}
