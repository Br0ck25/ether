// Package store opens Ether's SQLite database and applies each component's
// migrations. Components own their tables: they register migrations under
// their own name (table names prefixed with it) and receive *sql.DB.
//
//	func init() {
//		store.Register("catalog", 1, `CREATE TABLE catalog_titles (...)`)
//	}
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	_ "modernc.org/sqlite"
)

type migration struct {
	component string
	version   int
	sql       string
}

var (
	regMu      sync.Mutex
	migrations []migration
)

// Register adds a migration. Versions are per component and start at 1.
// Call from init(); a migration must never change after release.
func Register(component string, version int, sql string) {
	regMu.Lock()
	defer regMu.Unlock()
	migrations = append(migrations, migration{component, version, sql})
}

// Open opens (creating if needed) ether.db in dataDir and migrates it.
func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	return OpenPath(ctx, filepath.Join(dataDir, "ether.db"))
}

// OpenPath opens a database file; ":memory:" works for tests (single conn).
func OpenPath(ctx context.Context, path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
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
	return db, nil
}

// OpenMemory is a migrated in-memory database for tests.
func OpenMemory(ctx context.Context) (*sql.DB, error) { return OpenPath(ctx, ":memory:") }

func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		component TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY (component, version))`); err != nil {
		return err
	}
	regMu.Lock()
	ms := append([]migration(nil), migrations...)
	regMu.Unlock()
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].component != ms[j].component {
			return ms[i].component < ms[j].component
		}
		return ms[i].version < ms[j].version
	})
	for _, m := range ms {
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
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
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
