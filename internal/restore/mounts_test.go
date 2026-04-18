package restore

import (
	"reflect"
	"testing"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestParseDockerMounts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []config.Mount
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "single",
			in:   "/mnt/user/appdata/forgejo\t/var/lib/forgejo\n",
			want: []config.Mount{{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"}},
		},
		{
			name: "multiple",
			in:   "/mnt/user/appdata/forgejo\t/var/lib/forgejo\n/srv/gitea-extra\t/extra\n",
			want: []config.Mount{
				{Host: "/mnt/user/appdata/forgejo", Container: "/var/lib/forgejo"},
				{Host: "/srv/gitea-extra", Container: "/extra"},
			},
		},
		{
			name: "trailing-newline-and-blank-lines",
			in:   "\n/a\t/b\n\n/c\t/d\n\n",
			want: []config.Mount{
				{Host: "/a", Container: "/b"},
				{Host: "/c", Container: "/d"},
			},
		},
		{
			name: "missing-field-skipped",
			in:   "/a\n/b\t/c\n\t/d\n",
			want: []config.Mount{{Host: "/b", Container: "/c"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDockerMounts(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseDockerMounts(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
