//go:build unix

package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func withUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func assertOwnerOnly(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
}

func TestOpenCreatesOwnerOnlyDatabaseAndSidecars(t *testing.T) {
	withUmask(t)
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Hold a connection so the WAL and shared-memory files stay on disk.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE perm_probe (v TEXT); INSERT INTO perm_probe VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "ether.db")
	assertOwnerOnly(t, p, p+"-wal", p+"-shm")
}

func TestOpenTightensExistingDatabaseAndSidecars(t *testing.T) {
	withUmask(t)
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	conn, err := first.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE perm_probe (v TEXT); INSERT INTO perm_probe VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	// Files left world-readable by an earlier release.
	p := filepath.Join(dir, "ether.db")
	files := []string{p, p + "-wal", p + "-shm", p + "-journal"}
	if err := os.WriteFile(p+"-journal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.Chmod(f, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	assertOwnerOnly(t, files...)
}
