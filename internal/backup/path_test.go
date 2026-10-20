package backup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestSnapshotRestoreWithURICharactersInPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("question marks are not valid Windows path characters")
	}
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conf := filepath.Join(root, "config.json")
	if err := os.WriteFile(conf, []byte(`{"server":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "backups?#%")
	service := New(db, conf, directory, 2)
	snapshot, err := service.Create(ctx)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	file, err := os.Open(filepath.Join(directory, snapshot.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	target := filepath.Join(root, "restore?#%", "data")
	if err := RestoreBundle(ctx, file, target); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	if err := validateDB(ctx, filepath.Join(target, "ether.db")); err != nil {
		t.Fatal(err)
	}
}
