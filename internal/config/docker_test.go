package config

import "testing"

func TestDocker_HostToContainer(t *testing.T) {
	d := &Docker{Mounts: []Mount{
		{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
		{Host: "/var/run/docker.sock", Container: "/var/run/docker.sock"},
	}}
	cases := []struct {
		in, want string
	}{
		{"/mnt/user/appdata/forgejo", "/var/lib/forgejo"},
		{"/mnt/user/appdata/forgejo/gitea", "/var/lib/forgejo/gitea"},
		{"/mnt/user/appdata/forgejo/git/repositories", "/var/lib/forgejo/git/repositories"},
		{"/var/run/docker.sock", "/var/run/docker.sock"},
		{"/etc/forgejo/app.ini", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := d.HostToContainer(c.in); got != c.want {
			t.Errorf("HostToContainer(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestDocker_ContainerToHost(t *testing.T) {
	d := &Docker{Mounts: []Mount{
		{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
		{Host: "/var/run/docker.sock", Container: "/var/run/docker.sock"},
	}}
	cases := []struct {
		in, want string
	}{
		{"/var/lib/forgejo", "/mnt/user/appdata/forgejo"},
		{"/var/lib/forgejo/custom", "/mnt/user/appdata/forgejo/custom"},
		{"/var/lib/forgejo/git/repositories", "/mnt/user/appdata/forgejo/git/repositories"},
		{"/var/run/docker.sock", "/var/run/docker.sock"},
		{"/etc/forgejo/app.ini", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := d.ContainerToHost(c.in); got != c.want {
			t.Errorf("ContainerToHost(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestDocker_ContainerToHost_longestPrefixWins(t *testing.T) {
	d := &Docker{Mounts: []Mount{
		{Host: "/host/outer", Container: "/ctr"},
		{Host: "/host/inner", Container: "/ctr/nested"},
	}}
	if got := d.ContainerToHost("/ctr/nested/x"); got != "/host/inner/x" {
		t.Errorf("longest prefix should win; got %q", got)
	}
	if got := d.ContainerToHost("/ctr/other"); got != "/host/outer/other" {
		t.Errorf("shallow mount should still match; got %q", got)
	}
}

func TestDocker_ContainerToHost_nilReceiver(t *testing.T) {
	var d *Docker
	if got := d.ContainerToHost("/any/path"); got != "" {
		t.Errorf("nil receiver should return empty; got %q", got)
	}
}
