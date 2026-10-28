package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

// TestLogicalSnapshotRestoreAndImport covers the PostgreSQL bundle: a
// logical dump with a dialect-marked manifest, validated on restore and
// imported into an empty database on the next start. It runs against
// PostgreSQL when ETHER_TEST_POSTGRES is set and is skipped otherwise.
func TestLogicalSnapshotRestoreAndImport(t *testing.T) {
	if os.Getenv(store.TestPostgresEnv) == "" {
		t.Skip("set " + store.TestPostgresEnv + " to run PostgreSQL tests")
	}
	ctx := context.Background()
	dir := t.TempDir()
	db := store.OpenTest(t)
	conf := filepath.Join(dir, "config.json")
	if e := os.WriteFile(conf, []byte(`{"server":{"friendlyName":"test"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	s := New(db, conf, filepath.Join(dir, "backups"), 3)
	if e := s.Configure(ctx, Settings{Retain: 3, IntervalHours: 6}); e != nil {
		t.Fatal(e)
	}
	snap, e := s.Create(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(filepath.Join(s.dir, snap.Name))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	target := filepath.Join(dir, "restored")
	if e = RestoreBundle(ctx, f, target); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(target, "ether.db")); e == nil {
		t.Fatal("postgres bundle should not contain ether.db")
	}
	raw, e := os.ReadFile(filepath.Join(target, "manifest.json"))
	if e != nil || !strings.Contains(string(raw), `"dialect":"postgres"`) {
		t.Fatalf("manifest %s %v", raw, e)
	}

	fresh := store.OpenTest(t)
	imported, e := ImportPending(ctx, fresh, target)
	if e != nil || !imported {
		t.Fatalf("import: %v %v", imported, e)
	}
	got, e := New(fresh, conf, s.dir, 3).Settings(ctx)
	if e != nil || got.IntervalHours != 6 || got.Retain != 3 {
		t.Fatalf("restored settings %+v %v", got, e)
	}
	if _, e = os.Stat(filepath.Join(target, LogicalDumpFile+".imported")); e != nil {
		t.Fatal("dump not marked imported")
	}
	if imported, e = ImportPending(ctx, fresh, target); e != nil || imported {
		t.Fatalf("second start re-imported: %v %v", imported, e)
	}
}

func TestImportPendingSQLiteNoFile(t *testing.T) {
	db := store.OpenTest(t)
	if imported, e := ImportPending(context.Background(), db, t.TempDir()); e != nil || imported {
		t.Fatalf("%v %v", imported, e)
	}
}
