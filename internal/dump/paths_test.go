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
