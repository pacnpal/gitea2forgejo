package preflight

import "testing"

func TestParseINI(t *testing.T) {
	in := []byte(`
; Gitea INI
APP_NAME = Gitea
[security]
SECRET_KEY = "abcd1234"
INTERNAL_TOKEN = xyz
; commented
INSTALL_LOCK = true
[oauth2]
JWT_SECRET = 'jwt-value'
`)
	got := parseINI(in)
	want := map[string]string{
		"security.SECRET_KEY":     "abcd1234",
		"security.INTERNAL_TOKEN": "xyz",
		"security.INSTALL_LOCK":   "true",
		"oauth2.JWT_SECRET":       "jwt-value",
		".APP_NAME":               "Gitea",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:                 "512 B",
		2048:                "2.0 KiB",
		2 * 1024 * 1024:     "2.0 MiB",
		3 * 1024 * 1024 * 1024: "3.0 GiB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
