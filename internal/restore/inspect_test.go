package restore

import "testing"

func TestIsPrintableASCII(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"codeberg.org/forgejo/forgejo:15", true},
		{"forgejo:1.21@sha256:abc123", true},
		{"alpine", true},
		{"", false},
		{"\x00\x00\x00", false},
		{"forgejo\x00:15", false},
		{"forgejo\n", false}, // trailing newline should've been TrimSpace'd; be strict
		{"for\tgejo", false},
		{"forgejo\xff", false},
	}
	for _, tc := range cases {
		if got := isPrintableASCII(tc.in); got != tc.want {
			t.Errorf("isPrintableASCII(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
