// Package appini parses Gitea/Forgejo app.ini into the bits gitea2forgejo
// cares about: paths, DB config, storage config, security keys.
//
// It is intentionally minimal — we don't need a full INI editor here
// (that's gopkg.in/ini.v1 in internal/restore). All we want is to read
// specific keys to auto-populate config.yaml during `init` and to audit
// them during `preflight`.
package appini

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"strings"
)

// Flat returns a map keyed "section.KEY" (uppercase key, lowercase section).
// Quotes around values are stripped; comments (# or ;) are ignored.
func Flat(data []byte) map[string]string {
	out := map[string]string{}
	section := ""
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		k := strings.ToUpper(strings.TrimSpace(line[:i]))
		v := strings.TrimSpace(line[i+1:])
		v = strings.Trim(v, `"'`)
		out[section+"."+k] = v
	}
	return out
}

// Summary is the subset of app.ini fields gitea2forgejo needs.
type Summary struct {
	AppName      string
	Domain       string
	RootURL      string
	AppDataPath  string // data dir (attachments, lfs, avatars, etc.)
	RepoRoot     string
	CustomDir    string // may be empty; typically sibling of data_dir

	DBType     string // postgres | mysql | sqlite3
	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string
	DBPath     string // sqlite file path

	StorageType string // local | minio | s3
	LFSStorage  string // specific backend per storage type
	S3Bucket    string
	S3Endpoint  string

	HasSecretKey     bool
	HasInternalToken bool
	HasJWTSecret     bool
}

// Summarize extracts the fields in Summary from an already-parsed Flat map.
func Summarize(kv map[string]string) *Summary {
	s := &Summary{
		AppName:     kv[".APP_NAME"],
		Domain:      kv["server.DOMAIN"],
		RootURL:     kv["server.ROOT_URL"],
		AppDataPath: kv["server.APP_DATA_PATH"],
		RepoRoot:    kv["repository.ROOT"],

		DBType:     kv["database.DB_TYPE"],
		DBHost:     kv["database.HOST"],
		DBName:     kv["database.NAME"],
		DBUser:     kv["database.USER"],
		DBPassword: kv["database.PASSWD"],
		DBPath:     kv["database.PATH"],

		HasSecretKey:     kv["security.SECRET_KEY"] != "",
		HasInternalToken: kv["security.INTERNAL_TOKEN"] != "",
		HasJWTSecret:     kv["oauth2.JWT_SECRET"] != "",
	}
	// DB host may include the port as "host:port"
	if s.DBHost != "" {
		if i := strings.LastIndex(s.DBHost, ":"); i > 0 {
			s.DBPort = s.DBHost[i+1:]
			s.DBHost = s.DBHost[:i]
		}
	}
	// Default ports
	switch strings.ToLower(s.DBType) {
	case "postgres":
		if s.DBPort == "" {
			s.DBPort = "5432"
		}
	case "mysql":
		if s.DBPort == "" {
			s.DBPort = "3306"
		}
	}
	// Storage: the default storage backend applies unless overridden per-type.
	s.StorageType = strings.ToLower(kv["storage.STORAGE_TYPE"])
	if s.StorageType == "" {
		s.StorageType = "local"
	}
	if s.StorageType == "minio" || s.StorageType == "s3" {
		s.S3Bucket = kv["storage.MINIO_BUCKET"]
		if s.S3Bucket == "" {
			s.S3Bucket = kv["storage.BUCKET"]
		}
		s.S3Endpoint = kv["storage.MINIO_ENDPOINT"]
		if s.S3Endpoint == "" {
			s.S3Endpoint = kv["storage.ENDPOINT"]
		}
	}
	return s
}

// BuildDSN returns a DSN the gitea2forgejo tool can hand to its Go DB
// drivers. Format per dialect:
//
//   postgres://USER:PASSWORD@HOST:PORT/DBNAME?sslmode=<mode>
//   USER:PASSWORD@tcp(HOST:PORT)/DBNAME?parseTime=true
//   <absolute path>                                        (sqlite3)
//
// sslMode defaults to "disable" because that's what the Gitea installer
// uses; operators can override in the written config if they care.
func (s *Summary) BuildDSN(sslMode string) (string, error) {
	if sslMode == "" {
		sslMode = "disable"
	}
	switch strings.ToLower(s.DBType) {
	case "postgres":
		u := &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(s.DBUser, s.DBPassword),
			Host:   s.DBHost + ":" + s.DBPort,
			Path:   "/" + s.DBName,
		}
		q := u.Query()
		q.Set("sslmode", sslMode)
		u.RawQuery = q.Encode()
		return u.String(), nil
	case "mysql":
		// go-sql-driver format
		host := s.DBHost
		if s.DBPort != "" {
			host += ":" + s.DBPort
		}
		return fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true",
			s.DBUser, s.DBPassword, host, s.DBName), nil
	case "sqlite3":
		return s.DBPath, nil
	default:
		return "", fmt.Errorf("unsupported DB_TYPE %q", s.DBType)
	}
}
