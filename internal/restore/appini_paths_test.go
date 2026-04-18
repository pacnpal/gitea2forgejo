package restore

import (
	"testing"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestTargetPathForAppIni(t *testing.T) {
	cases := []struct {
		name     string
		hostPath string
		docker   *config.Docker
		want     string
	}{
		{
			name:     "bare-metal-passes-through",
			hostPath: "/var/lib/forgejo/git/repositories",
			docker:   nil,
			want:     "/var/lib/forgejo/git/repositories",
		},
		{
			name:     "docker-translates-to-container",
			hostPath: "/mnt/user/appdata/forgejo/git/repositories",
			docker: &config.Docker{
				Container: "Forgejo",
				Mounts: []config.Mount{
					{Host: "/mnt/user/appdata/forgejo", Container: "/data"},
				},
			},
			want: "/data/git/repositories",
		},
		{
			name:     "docker-no-mount-keeps-host",
			hostPath: "/var/log/forgejo",
			docker: &config.Docker{
				Container: "Forgejo",
				Mounts: []config.Mount{
					{Host: "/mnt/user/appdata/forgejo", Container: "/data"},
				},
			},
			want: "/var/log/forgejo",
		},
		{
			name:     "empty-stays-empty",
			hostPath: "",
			docker:   &config.Docker{Container: "Forgejo"},
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Target.Docker = tc.docker
			if got := targetPathForAppIni(cfg, tc.hostPath); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSourcePathForAppIni(t *testing.T) {
	// Same translation semantics, but via cfg.Source.Docker.
	cfg := &config.Config{}
	cfg.Source.Docker = &config.Docker{
		Container: "Gitea",
		Mounts: []config.Mount{
			{Host: "/mnt/user/appdata/gitea", Container: "/data"},
		},
	}
	got := sourcePathForAppIni(cfg, "/mnt/user/appdata/gitea/git/repositories")
	if got != "/data/git/repositories" {
		t.Errorf("got %q, want /data/git/repositories", got)
	}

	// Bare-metal source: keep host path.
	cfg2 := &config.Config{}
	got = sourcePathForAppIni(cfg2, "/var/lib/gitea/data")
	if got != "/var/lib/gitea/data" {
		t.Errorf("got %q, want /var/lib/gitea/data", got)
	}
}
