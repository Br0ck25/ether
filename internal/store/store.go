// Package store opens Ether's database and applies each component's
// migrations. Components own their tables: they register migrations under
// their own name (table names prefixed with it) and receive *sql.DB.
//
//	func init() {
//		store.Register("catalog", 1, `CREATE TABLE catalog_titles (...)`)
//	}
//
// A standalone server uses SQLite in its data directory. A clustered node
// uses an owner-supplied PostgreSQL database; component SQL is written once
// with `?` placeholders (rebound to $n by the "ether-pgx" driver) and
// migrations are translated from SQLite DDL (see TranslateDDL). DialectOf
// tells a component which engine it is talking to for the few constructs
// that differ.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"

	_ "modernc.org/sqlite"
)

type migration struct {
	component string
	version   int
	sql       string
	overrides map[Dialect]string
}

var (
	regMu      sync.Mutex
	migrations []migration
)

// Register adds a migration written in SQLite's dialect. Versions are per
// component and start at 1. Call from init(); a migration must never change
// after release. On PostgreSQL the DDL is translated by TranslateDDL unless
// RegisterDialect supplied an override.
func Register(component string, version int, sql string) {
	regMu.Lock()
	defer regMu.Unlock()
	for i := range migrations {
		// Fill a placeholder left by an earlier RegisterDialect.
		if migrations[i].component == component && migrations[i].version == version && migrations[i].sql == "" {
			migrations[i].sql = sql
			return
		}
	}
	migrations = append(migrations, migration{component: component, version: version, sql: sql})
}

// RegisterDialect overrides one registered migration for dialect d, for DDL
// the mechanical translation cannot express. It may be called before or
// after the matching Register (both from init()).
func RegisterDialect(component string, version int, d Dialect, sql string) {
	regMu.Lock()
	defer regMu.Unlock()
	for i := range migrations {
		if migrations[i].component == component && migrations[i].version == version {
			if migrations[i].overrides == nil {
				migrations[i].overrides = map[Dialect]string{}
			}
			migrations[i].overrides[d] = sql
			return
		}
	}
	migrations = append(migrations, migration{component: component, version: version, overrides: map[Dialect]string{d: sql}})
}

// sqlFor returns the migration text to run on dialect d.
func (m migration) sqlFor(d Dialect) string {
	if s, ok := m.overrides[d]; ok {
		return s
	}
	if d == Postgres {
		return TranslateDDL(m.sql)
	}
	return m.sql
}

// Options selects and configures the database backend.
type Options struct {
	// DataDir holds ether.db for SQLite.
	DataDir string
	// Driver is "sqlite" (default) or "postgres".
	Driver string
	// URL is the PostgreSQL connection URL (postgres://...). A postgres://
	// or postgresql:// URL implies Driver "postgres".
	URL string
	// Logger receives open-time warnings; nil uses slog.Default().
	Logger *slog.Logger
}

// Dialect is the backend these options select.
func (o Options) Dialect() Dialect {
	if isPostgresURL(o.URL) {
		return Postgres
	}
	return ParseDialect(o.Driver)
}

// OpenWith opens and migrates the database the options select and reports
// its dialect.
func OpenWith(ctx context.Context, o Options) (*sql.DB, Dialect, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	switch d := o.Dialect(); d {
	case Postgres:
		if o.URL == "" {
			return nil, d, errors.New("store: postgres requires a database URL")
		}
		db, err := openPostgres(ctx, o.URL, log)
		return db, d, err
	default:
		if o.DataDir == "" {
			return nil, d, errors.New("store: sqlite requires a data directory")
		}
		db, err := OpenPath(ctx, filepath.Join(o.DataDir, "ether.db"))
		return db, d, err
	}
}

// Open opens (creating if needed) ether.db in dataDir and migrates it.
func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	return OpenPath(ctx, filepath.Join(dataDir, "ether.db"))
}

// OpenPath opens a database file; ":memory:" works for tests (single conn).
func OpenPath(ctx context.Context, path string) (*sql.DB, error) {
	// journal_size_limit caps the WAL left on disk after a checkpoint; without
	// it one large transaction leaves a WAL file that never shrinks.
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)" +
		fmt.Sprintf("&_pragma=journal_size_limit(%d)", journalSizeLimit)
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	// Recommended at open by the SQLite docs: analyse tables whose stats are
	// missing or stale, bounded by the 0x10000 analysis limit so it is cheap.
	_, _ = db.ExecContext(ctx, `PRAGMA optimize=0x10002`)
	return db, nil
}

// registered returns the migrations sorted by component then version.
func registered() []migration {
	regMu.Lock()
	ms := append([]migration(nil), migrations...)
	regMu.Unlock()
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].component != ms[j].component {
			return ms[i].component < ms[j].component
		}
		return ms[i].version < ms[j].version
	})
	return ms
}

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		component TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY (component, version))`

// Migrate applies every registered migration db has not yet recorded.
func Migrate(ctx context.Context, db *sql.DB) error {
	if DialectOf(db) == Postgres {
		return migratePostgres(ctx, db)
	}
	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		return err
	}
	for _, m := range registered() {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE component=? AND version=?`,
			m.component, m.version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sqlFor(SQLite)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s/%d: %w", m.component, m.version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(component, version) VALUES (?, ?)`,
			m.component, m.version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// AppliedMigrations returns the recorded (component -> highest version).
func AppliedMigrations(ctx context.Context, db *sql.DB) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT component, MAX(version) FROM schema_migrations GROUP BY component`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var c string
		var v int
		if err := rows.Scan(&c, &v); err != nil {
			return nil, err
		}
		out[c] = v
	}
	return out, rows.Err()
}
