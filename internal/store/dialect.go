package store

import (
	"database/sql"
	"strings"
)

// Dialect names the SQL engine behind a *sql.DB. Components write portable
// SQL with `?` placeholders; the few constructs that differ between engines
// go through the helpers below.
type Dialect string

const (
	// SQLite is the standalone backend (modernc.org/sqlite).
	SQLite Dialect = "sqlite"
	// Postgres is the clustered backend (owner-supplied PostgreSQL).
	Postgres Dialect = "postgres"
)

func (d Dialect) String() string { return string(d) }

// ParseDialect maps a configured driver name to a Dialect; anything that is
// not recognisably PostgreSQL is SQLite.
func ParseDialect(name string) Dialect {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "postgres", "postgresql", "pg", "pgx":
		return Postgres
	}
	return SQLite
}

// DialectOf reports which engine db talks to. Databases opened through this
// package's PostgreSQL connector report Postgres; everything else SQLite.
func DialectOf(db *sql.DB) Dialect {
	if db == nil {
		return SQLite
	}
	if _, ok := db.Driver().(*pgDriver); ok {
		return Postgres
	}
	return SQLite
}

// Like is the case-insensitive pattern operator for user-facing search:
// SQLite's LIKE already ignores ASCII case, PostgreSQL needs ILIKE.
func Like(d Dialect) string {
	if d == Postgres {
		return "ILIKE"
	}
	return "LIKE"
}

// UnixDay formats the Unix-seconds expression expr as a UTC 'YYYY-MM-DD'
// text value.
func UnixDay(d Dialect, expr string) string {
	if d == Postgres {
		return "to_char(to_timestamp(" + expr + ") AT TIME ZONE 'UTC','YYYY-MM-DD')"
	}
	return "strftime('%Y-%m-%d'," + expr + ",'unixepoch')"
}

// UnixHour formats the Unix-seconds expression expr as a zero-padded UTC
// hour of day ('00'..'23').
func UnixHour(d Dialect, expr string) string {
	if d == Postgres {
		return "to_char(to_timestamp(" + expr + ") AT TIME ZONE 'UTC','HH24')"
	}
	return "strftime('%H'," + expr + ",'unixepoch')"
}

// JSONText extracts the top-level key from the JSON text column col as text.
// key must be a constant identifier-like name; it is not escaped.
func JSONText(d Dialect, col, key string) string {
	if d == Postgres {
		return "(" + col + "::jsonb->>'" + key + "')"
	}
	return "json_extract(" + col + ",'$." + key + "')"
}

// JSONInt extracts the top-level key from the JSON text column col as an
// integer (NULL when absent). key is not escaped.
func JSONInt(d Dialect, col, key string) string {
	if d == Postgres {
		return "CAST(" + col + "::jsonb->>'" + key + "' AS BIGINT)"
	}
	return "json_extract(" + col + ",'$." + key + "')"
}

// JSONRemove is the JSON text column col without the top-level key, as
// text. key is not escaped.
func JSONRemove(d Dialect, col, key string) string {
	if d == Postgres {
		return "((" + col + "::jsonb) - '" + key + "')::text"
	}
	return "json_remove(" + col + ",'$." + key + "')"
}

// Contains is a literal (non-pattern) substring test of needle in haystack,
// both SQL expressions.
func Contains(d Dialect, haystack, needle string) string {
	if d == Postgres {
		return "strpos(" + haystack + "," + needle + ")>0"
	}
	return "instr(" + haystack + "," + needle + ")>0"
}
