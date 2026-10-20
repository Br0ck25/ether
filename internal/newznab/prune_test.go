package newznab

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestManagerPruneDropsOldBudgetDays(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	m := NewManager(db, nil, Options{Now: func() time.Time { return now }})
	for _, day := range []string{"2026-03-10", "2026-02-03", "2026-02-02", "2025-01-01"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO newznab_budget(indexer_id, day, hits) VALUES ('a', ?, 1)`, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO newznab_cache VALUES ('old','a','[]',?), ('new','a','[]',?)`, now.Add(-time.Minute).Unix(), now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	n, err := m.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-02-02 and 2025-01-01 are older than 35 days; plus one cache row.
	if n != 3 {
		t.Fatalf("pruned %d, want 3", n)
	}
	var days int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM newznab_budget`).Scan(&days)
	if days != 2 {
		t.Fatalf("budget rows = %d, want 2", days)
	}
}
