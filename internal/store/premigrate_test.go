package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotBeforeMigrate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "ether.db")
	snap := path + PreMigrateSuffix

	// A brand-new database has nothing to protect.
	fresh, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := snapshotBeforeMigrate(ctx, fresh, path, registered()); ok || err != nil {
		t.Fatalf("fresh database: ok=%v err=%v", ok, err)
	}
	fresh.Close()

	db, err := OpenPath(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot written for a fresh database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE premig_t (v TEXT); INSERT INTO premig_t VALUES ('before')`); err != nil {
		t.Fatal(err)
	}

	// Fully migrated: no snapshot.
	if ok, err := snapshotBeforeMigrate(ctx, db, path, registered()); ok || err != nil {
		t.Fatalf("nothing pending: ok=%v err=%v", ok, err)
	}

	// A newer binary with one more migration snapshots the old state.
	next := append(registered(), migration{component: "premig", version: 1, sql: `SELECT 1`})
	if ok, err := snapshotBeforeMigrate(ctx, db, path, next); !ok || err != nil {
		t.Fatalf("pending migration: ok=%v err=%v", ok, err)
	}
	if v := snapshotValue(t, snap); v != "before" {
		t.Fatalf("snapshot holds %q", v)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(snap); err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("snapshot mode %v err=%v, want owner-only", fi.Mode(), err)
		}
	}

	// The migration run failed (marker still present): a retry must keep
	// the snapshot from before the upgrade.
	if _, err := db.ExecContext(ctx, `UPDATE premig_t SET v='partly migrated'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := snapshotBeforeMigrate(ctx, db, path, next); ok || err != nil {
		t.Fatalf("retry after failure: ok=%v err=%v", ok, err)
	}
	if v := snapshotValue(t, snap); v != "before" {
		t.Fatalf("snapshot replaced after a failed run: %q", v)
	}

	// Once a run completes, the next upgrade replaces the snapshot; only
	// one is kept.
	finishPreMigrate(path)
	if ok, err := snapshotBeforeMigrate(ctx, db, path, next); !ok || err != nil {
		t.Fatalf("next upgrade: ok=%v err=%v", ok, err)
	}
	if v := snapshotValue(t, snap); v != "partly migrated" {
		t.Fatalf("snapshot not refreshed: %q", v)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestOpenRemovesIncompleteMarkerAfterMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ether.db")
	marker := path + PreMigrateSuffix + preMigrateIncomplete
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker survived a successful migration: %v", err)
	}
}

func snapshotValue(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT v FROM premig_t`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
