package config

import (
	"os"
	"strings"
	"testing"
)

func TestDatabaseEffective(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == DatabaseURLEnv {
				return v
			}
			return ""
		}
	}
	cases := []struct {
		name string
		in   Database
		env  string
		want Database
	}{
		{"default", Database{}, "", Database{Driver: DatabaseSQLite}},
		{"sqlite", Database{Driver: "SQLite"}, "", Database{Driver: DatabaseSQLite}},
		{"postgres alias", Database{Driver: "postgresql", URL: "postgres://h/db"}, "", Database{Driver: DatabasePostgres, URL: "postgres://h/db"}},
		{"url implies postgres", Database{URL: "postgresql://h/db"}, "", Database{Driver: DatabasePostgres, URL: "postgresql://h/db"}},
		{"env wins", Database{Driver: "sqlite"}, "postgres://u:p@h/db", Database{Driver: DatabasePostgres, URL: "postgres://u:p@h/db"}},
		{"non-postgres env ignored", Database{Driver: "sqlite"}, "mysql://h/db", Database{Driver: DatabaseSQLite}},
	}
	for _, c := range cases {
		if got := c.in.Effective(env(c.env)); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if !(Database{URL: "postgres://h/db"}).IsPostgres() || (Database{}).IsPostgres() {
		t.Fatal("IsPostgres")
	}
}

func TestDatabaseRedacted(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "",
		"postgres://ether:s3cret@db:5432/e": "postgres://ether:xxxxx@db:5432/e",
		"postgres://db/e?password=s3cret":   "postgres://db/e?password=xxxxx",
		"postgres://ether@db/e":             "postgres://ether@db/e",
		"postgres://ether:s3cret@db:nope/e": "postgres://ether:xxxxx@db:nope/e",
	} {
		got := Database{URL: in}.Redacted().URL
		if got != want || strings.Contains(got, "s3cret") {
			t.Errorf("Redacted(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDatabaseURLSealedAtRest(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().Database.Driver != DatabaseSQLite {
		t.Fatalf("default driver = %q", s.Get().Database.Driver)
	}
	const u = "postgres://ether:test-db-password@db.invalid:5432/ether"
	if err := s.Update(func(c *Config) error {
		c.Database = Database{Driver: DatabasePostgres, URL: u}
		c.Indexers = []Indexer{{ID: "x", URL: "https://indexer.invalid"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "test-db-password") {
		t.Fatal("database URL persisted in plaintext")
	}
	if !strings.Contains(string(raw), "https://indexer.invalid") {
		t.Fatal("non-secret url fields must stay readable")
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().Database.URL != u {
		t.Fatalf("round trip URL = %q", s.Get().Database.URL)
	}
}
