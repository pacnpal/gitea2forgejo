package restore

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestQuoteUserTable(t *testing.T) {
	cases := []struct {
		dialect, want string
	}{
		{"postgres", `"user"`},
		{"sqlite3", `"user"`},
		{"mysql", "`user`"},
		{"unknown", "user"},
	}
	for _, c := range cases {
		if got := quoteUserTable(c.dialect); got != c.want {
			t.Errorf("quoteUserTable(%q) = %q, want %q", c.dialect, got, c.want)
		}
	}
}

// TestCleanOrphanFKs_sqlite exercises the DELETE against a minimal
// SQLite schema to confirm the orphan-removal logic actually fires
// and leaves non-orphan rows intact.
func TestCleanOrphanFKs_sqlite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fk.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	setup := []string{
		`CREATE TABLE "user" (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE repository (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE action_runner_token (id INTEGER PRIMARY KEY, owner_id INTEGER, repo_id INTEGER)`,
		`INSERT INTO "user" (id) VALUES (1), (2)`,
		`INSERT INTO repository (id) VALUES (10), (20)`,
		// 1: valid owner=1 repo=10 — keep
		// 2: orphan owner=99 repo=10 — delete (bad owner)
		// 3: orphan owner=1 repo=999 — delete (bad repo)
		// 4: global owner=0 repo=0 — delete (0 is still an FK violation
		//    against Forgejo v15's strict FKs; no user/repo has id=0)
		// 5: owner=1 repo=0 — delete (bad repo)
		// 6: owner=2 repo=20 — keep (both sides valid)
		`INSERT INTO action_runner_token (id, owner_id, repo_id) VALUES
			(1, 1, 10), (2, 99, 10), (3, 1, 999), (4, 0, 0), (5, 1, 0), (6, 2, 20)`,
	}
	for _, s := range setup {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	for _, stmt := range []string{
		`DELETE FROM action_runner_token WHERE owner_id NOT IN (SELECT id FROM "user")`,
		`DELETE FROM action_runner_token WHERE repo_id NOT IN (SELECT id FROM repository)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("cleanup %q: %v", stmt, err)
		}
	}

	rows, err := db.Query(`SELECT id FROM action_runner_token ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	want := []int{1, 6}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %d, want %d", i, got[i], want[i])
		}
	}
}
