package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestPruneExpiresBadMarksAndStaleRows(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := &fakeClock{t: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	idx := &fakeIndexer{releases: movieReleases()}
	svc := New(idx, fakeHealth{}, db, testCfg(), Options{Now: clock.now})
	if err := svc.MarkBad(ctx, "old", "broken"); err != nil {
		t.Fatal(err)
	}
	svc.setAvailability(ctx, 1, true, time.Hour)
	if _, err := db.ExecContext(ctx, `INSERT INTO resolver_verified VALUES('stale', ?)`, clock.now().Unix()); err != nil {
		t.Fatal(err)
	}

	clock.add(badRetention - time.Hour)
	if err := svc.MarkBad(ctx, "recent", "broken"); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Expired availability and the stale health pass go; bad marks stay.
	if n != 2 {
		t.Fatalf("first prune removed %d rows, want 2", n)
	}
	if !svc.isBad("old") || !svc.isBad("recent") {
		t.Fatal("bad marks inside retention must survive")
	}

	clock.add(2 * time.Hour)
	if svc.isBad("old") {
		t.Fatal("bad mark past retention still honoured")
	}
	if n, err = svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("second prune removed %d rows, want 1 (the old bad mark)", n)
	}
	var ids []string
	rows, err := db.QueryContext(ctx, `SELECT release_id FROM resolver_bad`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 || ids[0] != "recent" {
		t.Fatalf("resolver_bad = %v, want [recent]", ids)
	}
	svc.mu.RLock()
	nb, na := len(svc.bad), len(svc.avail)
	svc.mu.RUnlock()
	if nb != 1 || na != 0 {
		t.Fatalf("in-memory bad=%d avail=%d, want 1 and 0", nb, na)
	}

	// A restart only loads marks inside retention.
	clock.add(badRetention)
	svc2 := New(idx, fakeHealth{}, db, testCfg(), Options{Now: clock.now})
	if svc2.isBad("recent") || len(svc2.bad) != 0 {
		t.Fatal("expired marks loaded at startup")
	}
}

func TestRemarkingBadRefreshesRetention(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := &fakeClock{t: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc := New(&fakeIndexer{}, fakeHealth{}, db, testCfg(), Options{Now: clock.now})
	_ = svc.MarkBad(ctx, "r", "a")
	clock.add(badRetention - time.Hour)
	_ = svc.MarkBad(ctx, "r", "b")
	clock.add(2 * time.Hour)
	if _, err := svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.isBad("r") {
		t.Fatal("re-marked release lost its mark")
	}
	var n int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resolver_bad`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestBoundMapEvictsExpiredThenArbitrary(t *testing.T) {
	m := map[int]int{}
	for i := 0; i < 10; i++ {
		m[i] = i
	}
	boundMap(m, 20, func(v int) bool { return true })
	if len(m) != 10 {
		t.Fatal("below cap must not evict")
	}
	boundMap(m, 10, func(v int) bool { return v < 4 })
	if len(m) != 6 {
		t.Fatalf("len = %d, want 6 after dropping expired", len(m))
	}
	for i := 0; i < 4; i++ {
		if _, ok := m[i]; ok {
			t.Fatal("expired entry survived")
		}
	}
	for i := 10; i < 14; i++ {
		m[i] = i
	}
	boundMap(m, 10, func(int) bool { return false })
	if len(m) != 7 {
		t.Fatalf("len = %d, want 7 (3/4 of cap)", len(m))
	}
}

func TestSearchCacheIsBounded(t *testing.T) {
	ctx := context.Background()
	svc := New(&fakeIndexer{}, fakeHealth{}, nil, testCfg(), Options{})
	for i := 0; i < maxSearchCache+50; i++ {
		if _, err := svc.search(ctx, core.SearchRequest{Title: "t", Year: i}); err != nil {
			t.Fatal(err)
		}
	}
	svc.mu.RLock()
	n := len(svc.searchCache)
	svc.mu.RUnlock()
	if n > maxSearchCache {
		t.Fatalf("search cache holds %d entries, cap %d", n, maxSearchCache)
	}
}
