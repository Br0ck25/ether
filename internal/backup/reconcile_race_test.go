package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestSharedBundleRecordedOnceAcrossNodes(t *testing.T) {
	db := store.OpenTest(t)
	shared := t.TempDir()
	const name = "ether-shared.tar.gz"
	if err := os.WriteFile(filepath.Join(shared, name), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	const nodes = 16
	start := make(chan struct{})
	errs := make(chan error, nodes)
	for i := 0; i < nodes; i++ {
		go func(i int) {
			s := New(db, "", "", 1)
			<-start
			errs <- s.record(context.Background(), Node{ID: fmt.Sprint(i)}, shared, true)
		}(i)
	}
	close(start)
	for i := 0; i < nodes; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_files WHERE dir=? AND name=?`, shared, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("one shared bundle was recorded %d times", count)
	}
}
