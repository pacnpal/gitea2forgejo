package restore

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptResetDB(t *testing.T) {
	state := &TargetDBState{TableCount: 112, VersionRow: 323, HasForgejoExtras: false}
	cases := []struct {
		name    string
		input   string
		dialect string
		want    bool
		wantOut string // substring expected in the prompt
	}{
		{"yes", "y\n", "postgres", true, "DROP SCHEMA public CASCADE"},
		{"yes-long", "YES\n", "postgres", true, ""},
		{"no", "n\n", "postgres", false, ""},
		{"blank-defaults-no", "\n", "postgres", false, ""},
		{"junk-defaults-no", "maybe\n", "postgres", false, ""},
		{"eof", "", "postgres", false, ""},
		{"mysql-action", "n\n", "mysql", false, "DROP DATABASE"},
		{"sqlite-action", "n\n", "sqlite3", false, "remove sqlite DB file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := promptResetDB(strings.NewReader(tc.input), &out, tc.dialect, state)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if tc.wantOut != "" && !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("prompt missing %q; got:\n%s", tc.wantOut, out.String())
			}
			// State fields should always be echoed.
			if !strings.Contains(out.String(), "112 tables") {
				t.Errorf("prompt missing table count; got:\n%s", out.String())
			}
		})
	}
}
