package config

import (
	"net/url"
	"strings"
)

// DatabaseURLEnv overrides Database for this process: a postgres:// (or
// postgresql://) URL selects PostgreSQL with that URL. It lets container and
// service deployments keep credentials out of config.json.
const DatabaseURLEnv = "ETHER_DATABASE_URL"

const (
	DatabaseSQLite   = "sqlite"
	DatabasePostgres = "postgres"
)

// Database is this node's storage backend. It is node-local: a standalone
// server keeps SQLite in its data directory; a cluster node points at the
// owner-supplied PostgreSQL database. The URL carries credentials, so it is
// sealed in config.json like other secrets and must only ever be logged
// through Redacted.
type Database struct {
	// Driver is "sqlite" (default) or "postgres".
	Driver string `json:"driver,omitempty"`
	// URL is the PostgreSQL connection URL, e.g.
	// postgres://ether:secret@db.lan:5432/ether?sslmode=require
	URL string `json:"url,omitempty"`
	// DeferMigrations makes this node wait, at startup, instead of applying
	// new schema migrations to the shared database while another live node
	// runs a version of Ether that does not know them. Off by default:
	// migrations are additive, so older nodes keep working on a newer
	// schema and a rolling update never stalls on it.
	DeferMigrations bool `json:"deferMigrations,omitempty"`
}

// Effective applies the ETHER_DATABASE_URL override (read through getenv)
// and normalises the driver name.
func (d Database) Effective(getenv func(string) string) Database {
	if getenv != nil {
		if v := strings.TrimSpace(getenv(DatabaseURLEnv)); v != "" && isPostgresURL(v) {
			d.Driver, d.URL = DatabasePostgres, v
		}
	}
	switch strings.ToLower(strings.TrimSpace(d.Driver)) {
	case "postgres", "postgresql", "pg", "pgx":
		d.Driver = DatabasePostgres
	case "":
		if isPostgresURL(d.URL) {
			d.Driver = DatabasePostgres
		} else {
			d.Driver = DatabaseSQLite
		}
	default:
		d.Driver = DatabaseSQLite
	}
	return d
}

// IsPostgres reports whether these (effective) settings select PostgreSQL.
func (d Database) IsPostgres() bool { return d.Effective(nil).Driver == DatabasePostgres }

// Redacted returns a copy safe to log or display: the URL's password is
// masked.
func (d Database) Redacted() Database {
	d.URL = RedactDatabaseURL(d.URL)
	return d
}

// RedactDatabaseURL masks the password in a connection URL.
func RedactDatabaseURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		if i := strings.Index(raw, "://"); i >= 0 {
			rest := raw[i+3:]
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				if c := strings.Index(rest[:at], ":"); c >= 0 {
					return raw[:i+3] + rest[:c] + ":xxxxx" + rest[at:]
				}
			}
		}
		return "[unparseable database URL]"
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	q := u.Query()
	changed := false
	for k := range q {
		if strings.Contains(strings.ToLower(k), "password") {
			q.Set(k, "xxxxx")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func isPostgresURL(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(v, "postgres://") || strings.HasPrefix(v, "postgresql://")
}
