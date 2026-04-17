package initcmd

import "testing"

func TestTranslateToHost(t *testing.T) {
	mounts := []Mount{
		{HostPath: "/mnt/user/appdata/forgejo", ContainerPath: "/data"},
		{HostPath: "/var/run/docker.sock", ContainerPath: "/var/run/docker.sock"},
	}
	cases := []struct {
		in, want string
	}{
		{"/data/gitea/conf/app.ini", "/mnt/user/appdata/forgejo/gitea/conf/app.ini"},
		{"/data/gitea", "/mnt/user/appdata/forgejo/gitea"},
		{"/data", "/mnt/user/appdata/forgejo"},
		// No mount match — return unchanged.
		{"/tmp/foo", "/tmp/foo"},
		// Exact match on a non-prefix path.
		{"/var/run/docker.sock", "/var/run/docker.sock"},
		// Empty input.
		{"", ""},
	}
	for _, c := range cases {
		if got := TranslateToHost(c.in, mounts); got != c.want {
			t.Errorf("TranslateToHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTranslateToHost_longestPrefixWins(t *testing.T) {
	mounts := []Mount{
		{HostPath: "/host/outer", ContainerPath: "/data"},
		{HostPath: "/host/inner", ContainerPath: "/data/nested"},
	}
	if got := TranslateToHost("/data/nested/x", mounts); got != "/host/inner/x" {
		t.Errorf("longest-prefix failed: got %q", got)
	}
	if got := TranslateToHost("/data/other", mounts); got != "/host/outer/other" {
		t.Errorf("shallower match failed: got %q", got)
	}
}
