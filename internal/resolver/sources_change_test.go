package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

// Changing where releases come from (here debrid's cached-only switch)
// forgets "not available" answers and cached searches, so the next play
// searches the new sources instead of answering from the old ones.
func TestSourcesChangeForgetsUnavailableTitles(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	cfg := testCfg()
	cfg.Debrid.CachedOnly = true
	s := New(&fakeIndexer{}, nil, db, cfg, Options{})
	defer s.Close()

	s.setAvailability(ctx, 7, false, time.Hour)
	s.setAvailability(ctx, 8, true, time.Hour)
	s.mu.Lock()
	s.searchCache["k"] = searchCacheEntry{expires: time.Now().Add(time.Hour)}
	s.mu.Unlock()

	// An unrelated change keeps them.
	same := cfg
	same.Resolver.MaxCandidates = 3
	s.UpdateConfig(same)
	if e, ok := s.getAvailability(ctx, 7); !ok || e.available {
		t.Fatal("an unrelated settings change dropped the unavailable answer")
	}

	off := same
	off.Debrid.CachedOnly = false
	s.UpdateConfig(off)
	if _, ok := s.getAvailability(ctx, 7); ok {
		t.Fatal("unavailable answer survived turning cached-only off")
	}
	if e, ok := s.getAvailability(ctx, 8); !ok || !e.available {
		t.Fatal("an available answer was dropped")
	}
	s.mu.RLock()
	n := len(s.searchCache)
	s.mu.RUnlock()
	if n != 0 {
		t.Fatalf("%d cached searches kept", n)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resolver_availability WHERE available = 0`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("persisted unavailable rows: %d (%v)", rows, err)
	}
}
