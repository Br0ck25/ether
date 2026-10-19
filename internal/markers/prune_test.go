package markers

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestPruneDeletesOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1_800_000_000, 0)
	s := New(Options{DB: db, Now: func() time.Time { return now }})
	past, future := now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix()
	// Key 1 is expired, key 2 still valid, in both tables.
	for key, exp := range map[int64]int64{1: past, 2: future} {
		if _, err := db.ExecContext(ctx, `INSERT INTO markers_cache VALUES(?,1000,'[]',?)`, key, exp); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO markers_detected VALUES(?,1000,?)`, key, exp); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d rows, want 2", n)
	}
	for _, table := range []string{"markers_cache", "markers_detected"} {
		var key int64
		if err := db.QueryRowContext(ctx, `SELECT rating_key FROM `+table).Scan(&key); err != nil || key != 2 {
			t.Fatalf("%s survivor = %d, %v; want 2", table, key, err)
		}
	}
}
