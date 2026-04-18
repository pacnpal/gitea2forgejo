package restore

import (
	"testing"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestTargetConfigPath(t *testing.T) {
	cases := []struct {
		name    string
		cfgFile string
		docker  *config.Docker
		want    string
	}{
		{
			name:    "bare-metal-passes-through",
			cfgFile: "/etc/forgejo/app.ini",
			docker:  nil,
			want:    "/etc/forgejo/app.ini",
		},
		{
			name:    "docker-but-no-container-passes-through",
			cfgFile: "/mnt/user/appdata/forgejo/gitea/conf/app.ini",
			docker:  &config.Docker{}, // Container == ""
			want:    "/mnt/user/appdata/forgejo/gitea/conf/app.ini",
		},
		{
			name:    "docker-translates-host-to-container",
			cfgFile: "/mnt/user/appdata/forgejo/gitea/conf/app.ini",
			docker: &config.Docker{
				Container: "Forgejo",
				Mounts: []config.Mount{
					{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
				},
			},
			want: "/var/lib/forgejo/gitea/conf/app.ini",
		},
		{
			name:    "docker-no-mount-match-returns-original",
			cfgFile: "/etc/forgejo/app.ini", // already container-native
			docker: &config.Docker{
				Container: "Forgejo",
				Mounts: []config.Mount{
					{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
				},
			},
			want: "/etc/forgejo/app.ini",
		},
		{
			name:    "docker-no-mounts-returns-original",
			cfgFile: "/somewhere/app.ini",
			docker:  &config.Docker{Container: "Forgejo"}, // Mounts nil
			want:    "/somewhere/app.ini",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Target.ConfigFile = tc.cfgFile
			cfg.Target.Docker = tc.docker
			if got := targetConfigPath(cfg); got != tc.want {
				t.Errorf("targetConfigPath() = %q, want %q", got, tc.want)
			}
		})
	}
}
