package appini

import (
	"strings"
	"testing"
)

const samplePostgresIni = `
APP_NAME = Gitea
[server]
DOMAIN = gitea.example.com
ROOT_URL = https://gitea.example.com/
APP_DATA_PATH = /var/lib/gitea/data
[repository]
ROOT = /var/lib/gitea/git/repositories
[database]
DB_TYPE = postgres
HOST = db.internal:5432
NAME = gitea
USER = gitea
PASSWD = sekret
[security]
SECRET_KEY = "abcd1234"
INTERNAL_TOKEN = token
[oauth2]
JWT_SECRET = jwt
[storage]
STORAGE_TYPE = minio
MINIO_BUCKET = gitea
MINIO_ENDPOINT = https://s3.example.com
`

func TestSummarize_postgres(t *testing.T) {
	s := Summarize(Flat([]byte(samplePostgresIni)))
	if s.AppName != "Gitea" || s.Domain != "gitea.example.com" {
		t.Errorf("basic fields: %+v", s)
	}
	if s.AppDataPath != "/var/lib/gitea/data" {
		t.Errorf("app_data_path: %q", s.AppDataPath)
	}
	if s.RepoRoot != "/var/lib/gitea/git/repositories" {
		t.Errorf("repo_root: %q", s.RepoRoot)
	}
	if s.DBType != "postgres" || s.DBHost != "db.internal" || s.DBPort != "5432" ||
		s.DBName != "gitea" || s.DBUser != "gitea" || s.DBPassword != "sekret" {
		t.Errorf("db: %+v", s)
	}
	if !s.HasSecretKey || !s.HasInternalToken || !s.HasJWTSecret {
		t.Errorf("keys: %+v", s)
	}
	if s.StorageType != "minio" || s.S3Bucket != "gitea" ||
		s.S3Endpoint != "https://s3.example.com" {
		t.Errorf("storage: %+v", s)
	}
	dsn, err := s.BuildDSN("")
	if err != nil {
		t.Fatal(err)
	}
	// URL-encoded form may escape colon/at in some cases; just spot-check.
	for _, want := range []string{"postgres://", "gitea:sekret", "db.internal:5432", "/gitea", "sslmode=disable"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn missing %q: %s", want, dsn)
		}
	}
}

func TestSummarize_mysql(t *testing.T) {
	ini := `
[database]
DB_TYPE = mysql
HOST = 127.0.0.1:3306
NAME = gitea
USER = gitea
PASSWD = pw
`
	s := Summarize(Flat([]byte(ini)))
	dsn, err := s.BuildDSN("")
	if err != nil {
		t.Fatal(err)
	}
	if dsn != "gitea:pw@tcp(127.0.0.1:3306)/gitea?parseTime=true" {
		t.Errorf("mysql dsn: %s", dsn)
	}
}

func TestSummarize_sqliteDefaultPort(t *testing.T) {
	ini := `
[database]
DB_TYPE = sqlite3
PATH = /var/lib/gitea/gitea.db
`
	s := Summarize(Flat([]byte(ini)))
	dsn, err := s.BuildDSN("")
	if err != nil {
		t.Fatal(err)
	}
	if dsn != "/var/lib/gitea/gitea.db" {
		t.Errorf("sqlite dsn: %s", dsn)
	}
}

func TestSummarize_mysqlImpliesPort(t *testing.T) {
	// HOST without port → default 3306 applied.
	s := Summarize(Flat([]byte("[database]\nDB_TYPE = mysql\nHOST = db\nNAME=g\nUSER=u\nPASSWD=p\n")))
	if s.DBPort != "3306" {
		t.Errorf("default mysql port: %q", s.DBPort)
	}
}

func TestSummarize_postgresImpliesPort(t *testing.T) {
	s := Summarize(Flat([]byte("[database]\nDB_TYPE = postgres\nHOST = db\nNAME=g\nUSER=u\nPASSWD=p\n")))
	if s.DBPort != "5432" {
		t.Errorf("default postgres port: %q", s.DBPort)
	}
}
