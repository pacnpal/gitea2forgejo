package initcmd

import (
	"testing"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestSetTargetDefault(t *testing.T) {
	cases := []struct {
		name    string
		start   string
		dflt    string
		docker  *config.Docker
		want    string
	}{
		{
			name:  "already-set-wins",
			start: "/already/set",
			dflt:  "/var/lib/forgejo",
			docker: &config.Docker{Container: "Forgejo", Mounts: []config.Mount{
				{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
			}},
			want: "/already/set",
		},
		{
			name:   "bare-metal-uses-container-default-verbatim",
			start:  "",
			dflt:   "/var/lib/forgejo",
			docker: nil,
			want:   "/var/lib/forgejo",
		},
		{
			name:   "docker-without-container-uses-default-verbatim",
			start:  "",
			dflt:   "/var/lib/forgejo",
			docker: &config.Docker{},
			want:   "/var/lib/forgejo",
		},
		{
			name:  "docker-with-matching-mount-translates",
			start: "",
			dflt:  "/var/lib/forgejo",
			docker: &config.Docker{Container: "Forgejo", Mounts: []config.Mount{
				{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
			}},
			want: "/mnt/user/appdata/forgejo",
		},
		{
			name:  "docker-custom-dir-translates",
			start: "",
			dflt:  "/var/lib/forgejo/custom",
			docker: &config.Docker{Container: "Forgejo", Mounts: []config.Mount{
				{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
			}},
			want: "/mnt/user/appdata/forgejo/custom",
		},
		{
			name:  "docker-with-no-covering-mount-keeps-default",
			start: "",
			dflt:  "/etc/forgejo/app.ini",
			docker: &config.Docker{Container: "Forgejo", Mounts: []config.Mount{
				{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
			}},
			want: "/etc/forgejo/app.ini",
		},
		{
			name:   "docker-with-no-mounts-keeps-default",
			start:  "",
			dflt:   "/var/lib/forgejo",
			docker: &config.Docker{Container: "Forgejo"},
			want:   "/var/lib/forgejo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.start
			setTargetDefault(&got, tc.dflt, tc.docker)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
