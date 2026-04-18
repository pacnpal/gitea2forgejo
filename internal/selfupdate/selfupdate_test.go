package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.2.7", "v0.2.8", true},
		{"v0.2.8", "v0.2.8", false},
		{"v0.2.9", "v0.2.8", false},
		// Dev build (git describe): v0.2.6-7-g... is older than v0.2.6 per
		// go-version's pre-release semantics — upgrade prompt should fire.
		{"v0.2.6-7-g353ce2e", "v0.2.6", true},
		{"v0.2.6-7-g353ce2e", "v0.2.8", true},
		// Unparseable current treated as always outdated.
		{"dev", "v0.2.8", true},
	}
	for _, c := range cases {
		got, err := IsNewer(c.current, c.latest)
		if err != nil {
			t.Errorf("IsNewer(%q,%q) err: %v", c.current, c.latest, err)
			continue
		}
		if got != c.want {
			t.Errorf("IsNewer(%q,%q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
