package ratings

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestPruneDropsRowsPastMaximumCacheAge(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1_800_000_000, 0)
	s := New(db, Options{Now: func() time.Time { return now }})
	for key, at := range map[int64]time.Time{1: now, 2: now.Add(-maxCacheAge + time.Hour), 3: now.Add(-maxCacheAge - time.Hour)} {
		if _, err := db.ExecContext(ctx, `INSERT INTO ratings_cache VALUES(?,'omdb','[]',?)`, key, at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Prune(ctx)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	var left int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ratings_cache WHERE item_key=3`).Scan(&left)
	if left != 0 {
		t.Fatal("stale row survived")
	}
}
