package restore

import (
	"fmt"
	"net/url"
	"strings"
)

// parseMySQLDSNImpl is a local copy of the parser in internal/dump to avoid
// circular imports. See that file for documentation; keep the two in sync.
func parseMySQLDSNImpl(dsn string) (host, port, user, pass, db string, err error) {
	if strings.HasPrefix(dsn, "mysql://") {
		u, e := url.Parse(dsn)
		if e != nil {
			return "", "", "", "", "", e
		}
		host = u.Hostname()
		port = u.Port()
		if port == "" {
			port = "3306"
		}
		user = u.User.Username()
		pass, _ = u.User.Password()
		db = strings.TrimPrefix(u.Path, "/")
		return
	}
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		err = fmt.Errorf("no @ in DSN")
		return
	}
	creds, rest := dsn[:at], dsn[at+1:]
	if i := strings.Index(creds, ":"); i >= 0 {
		user, pass = creds[:i], creds[i+1:]
	} else {
		user = creds
	}
	lp := strings.Index(rest, "(")
	rp := strings.Index(rest, ")")
	if lp < 0 || rp < 0 || rp < lp {
		err = fmt.Errorf("malformed DSN net part")
		return
	}
	proto := rest[:lp]
	addr := rest[lp+1 : rp]
	if proto != "tcp" {
		err = fmt.Errorf("unsupported mysql DSN protocol %q (only tcp)", proto)
		return
	}
	if i := strings.Index(addr, ":"); i >= 0 {
		host, port = addr[:i], addr[i+1:]
	} else {
		host, port = addr, "3306"
	}
	tail := rest[rp+1:]
	tail = strings.TrimPrefix(tail, "/")
	if i := strings.Index(tail, "?"); i >= 0 {
		db = tail[:i]
	} else {
		db = tail
	}
	if db == "" {
		err = fmt.Errorf("no database name in DSN")
	}
	return
}

func sqliteDSNPathImpl(dsn string) string {
	s := strings.TrimPrefix(dsn, "file:")
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i]
	}
	return s
}
