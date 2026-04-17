package restore

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInspectDB_sqliteEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	state, err := inspectDB(db, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Empty || state.TableCount != 0 {
		t.Errorf("expected empty, got %+v", state)
	}
}

func TestInspectDB_sqliteWithTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "populated.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		`CREATE TABLE "version" (id INTEGER PRIMARY KEY, version INTEGER)`,
		`INSERT INTO version VALUES (1, 307)`,
		`CREATE TABLE quota (id INTEGER PRIMARY KEY)`, // Forgejo-only
		`CREATE TABLE "user" (id INTEGER PRIMARY KEY)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	state, err := inspectDB(db, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if state.Empty {
		t.Error("expected non-empty")
	}
	if state.TableCount != 3 {
		t.Errorf("table count = %d, want 3", state.TableCount)
	}
	if state.VersionRow != 307 {
		t.Errorf("version row = %d, want 307", state.VersionRow)
	}
	if !state.HasForgejoExtras {
		t.Error("expected HasForgejoExtras=true for quota table")
	}
}

func TestInspectDB_sqliteGiteaOnly(t *testing.T) {
	// Dump from a Gitea 1.23 instance has no quota_* tables.
	p := filepath.Join(t.TempDir(), "gitea-only.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		`CREATE TABLE "version" (id INTEGER PRIMARY KEY, version INTEGER)`,
		`INSERT INTO version VALUES (1, 305)`,
		`CREATE TABLE "user" (id INTEGER PRIMARY KEY)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	state, err := inspectDB(db, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if state.HasForgejoExtras {
		t.Error("expected HasForgejoExtras=false for Gitea-only schema")
	}
	if state.VersionRow != 305 {
		t.Errorf("version = %d", state.VersionRow)
	}
}
