package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// PreMigrateSuffix names the snapshot taken next to a SQLite database before
// pending migrations change its schema (ether.db.pre-migrate.bak). Only the
// latest one is kept: each upgrade that migrates replaces it.
const PreMigrateSuffix = ".pre-migrate.bak"

// pendingMigrations counts the migrations in ms a SQLite database has not
// recorded yet. A database without schema_migrations is new (or empty) and
// reports none: there is nothing in it to protect.
func pendingMigrations(ctx context.Context, db *sql.DB, ms []migration) (int, error) {
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&tables); err != nil {
		return 0, err
	}
	if tables == 0 {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT component, version FROM schema_migrations`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var c string
		var v int
		if err := rows.Scan(&c, &v); err != nil {
			return 0, err
		}
		applied[fmt.Sprintf("%s/%d", c, v)] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(applied) == 0 {
		return 0, nil
	}
	n := 0
	for _, m := range ms {
		if !applied[fmt.Sprintf("%s/%d", m.component, m.version)] {
			n++
		}
	}
	return n, nil
}

// preMigrateIncomplete marks a snapshot whose migration run has not finished
// yet. While it exists the snapshot is not retaken: after a failed migration
// the database is partly migrated (each migration commits on its own), and
// the copy from before the upgrade is the one worth keeping.
const preMigrateIncomplete = ".incomplete"

// snapshotBeforeMigrate writes a consistent copy of the SQLite database at
// path (VACUUM INTO) to path+PreMigrateSuffix when an existing database has
// migrations pending, so an upgrade whose migration goes wrong can be rolled
// back by hand. The copy is written to a temporary file and renamed over the
// previous snapshot; it is owner-only like the database. It reports whether
// a snapshot was written. Callers treat errors as non-fatal and call
// finishPreMigrate once Migrate succeeds.
func snapshotBeforeMigrate(ctx context.Context, db *sql.DB, path string, ms []migration) (bool, error) {
	pending, err := pendingMigrations(ctx, db, ms)
	if err != nil || pending == 0 {
		return false, err
	}
	dst := path + PreMigrateSuffix
	if _, err := os.Stat(dst + preMigrateIncomplete); err == nil {
		if _, err := os.Stat(dst); err == nil {
			return false, nil // keep the snapshot from before the failed run
		}
	}
	tmp := dst + ".tmp"
	// VACUUM INTO needs an absent or empty target; creating it empty first
	// fixes its mode (the copy holds the same credentials as ether.db).
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	f.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("vacuum into %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return false, err
	}
	// Nothing has migrated yet, so a crash before this marker exists only
	// means the next start snapshots the same, still unmigrated database.
	if err := os.WriteFile(dst+preMigrateIncomplete, nil, 0o600); err != nil {
		return true, err
	}
	return true, nil
}

// finishPreMigrate records that migrations for the database at path
// completed, so the next upgrade takes a fresh snapshot.
func finishPreMigrate(path string) {
	_ = os.Remove(path + PreMigrateSuffix + preMigrateIncomplete)
}
