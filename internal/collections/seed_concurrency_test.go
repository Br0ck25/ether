package collections_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// Cluster nodes starting together on a fresh database must not fail on the
// built-ins' ids or seed the starter collections more than once.
func TestConcurrentStartupSeedsOnce(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	cat, err := catalog.New(db, nil, config.Catalog{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const nodes = 6
	var wg sync.WaitGroup
	errs := make([]error, nodes)
	for i := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = collections.New(db, cat, slog.Default())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("node %d: %v", i, err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections_specs WHERE builtin=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 19 {
		t.Fatalf("starter collections = %d, want 19", n)
	}
}
