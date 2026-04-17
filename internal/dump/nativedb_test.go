package dump

import "testing"

func TestParseMySQLDSN_gosqldriver(t *testing.T) {
	host, port, user, pass, db, err := parseMySQLDSN("gitea:secret@tcp(db.example.com:3307)/gitea?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	if host != "db.example.com" || port != "3307" || user != "gitea" || pass != "secret" || db != "gitea" {
		t.Errorf("got host=%q port=%q user=%q pass=%q db=%q", host, port, user, pass, db)
	}
}

func TestParseMySQLDSN_defaultPort(t *testing.T) {
	host, port, _, _, _, err := parseMySQLDSN("u:p@tcp(h)/d")
	if err != nil {
		t.Fatal(err)
	}
	if host != "h" || port != "3306" {
		t.Errorf("host=%q port=%q", host, port)
	}
}

func TestParseMySQLDSN_urlForm(t *testing.T) {
	host, port, user, pass, db, err := parseMySQLDSN("mysql://u:p@h:3306/mydb")
	if err != nil {
		t.Fatal(err)
	}
	if host != "h" || port != "3306" || user != "u" || pass != "p" || db != "mydb" {
		t.Errorf("mismatch: %s %s %s %s %s", host, port, user, pass, db)
	}
}

func TestParseMySQLDSN_badProto(t *testing.T) {
	_, _, _, _, _, err := parseMySQLDSN("u:p@unix(/sock)/d")
	if err == nil {
		t.Error("expected error for non-tcp proto")
	}
}

func TestSQLiteDSNPath(t *testing.T) {
	cases := map[string]string{
		"/var/lib/gitea/gitea.db":              "/var/lib/gitea/gitea.db",
		"file:/var/lib/gitea/gitea.db":         "/var/lib/gitea/gitea.db",
		"/var/lib/gitea/gitea.db?_pragma=busy": "/var/lib/gitea/gitea.db",
		"file:/x/y.db?_fk=1&_ts=true":          "/x/y.db",
	}
	for in, want := range cases {
		if got := sqliteDSNPath(in); got != want {
			t.Errorf("sqliteDSNPath(%q)=%q want %q", in, got, want)
		}
	}
}
