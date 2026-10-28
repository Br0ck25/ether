package newznab

import (
	"context"
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestManagerReusesSearchAndNZBAfterBudgetsExhausted(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f := newFakeIndexer(t, "")
	cfg := testConfig(f.server.URL)
	cfg.DailyLimit, cfg.DailyGrabs = 2, 1 // caps + search, one NZB
	opts := Options{NZBDir: t.TempDir()}
	m := NewManager(db, []config.Indexer{cfg}, opts)
	req := core.SearchRequest{Type: core.TypeMovie, Title: "The Signal", Year: 1999}
	results, err := m.Search(ctx, req)
	if err != nil || len(results) == 0 {
		t.Fatalf("initial search: %v %v", results, err)
	}
	if _, err = m.FetchNZB(ctx, results[0]); err != nil {
		t.Fatal(err)
	}
	if !m.Clients()[0].Exhausted(ctx) {
		t.Fatal("test did not exhaust limits")
	}
	// Recreate the manager to prove on-disk cached titles remain playable
	// after a server restart, with neither search nor download allowance left.
	m = NewManager(db, []config.Indexer{cfg}, opts)
	results, err = m.Search(ctx, req)
	if err != nil || len(results) == 0 {
		t.Fatalf("cached search was blocked by budgets: %v", err)
	}
	if b, err := m.FetchNZB(ctx, results[0]); err != nil || string(b) != fakeNZB {
		t.Fatalf("cached NZB: %v", err)
	}
	if _, err := m.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Uncached"}); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("uncached search bypassed allowance: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits["caps"] != 1 || f.hits["search"] != 1 || f.nzbHits != 1 {
		t.Fatalf("exhausted cache reuse made network calls: hits=%v grabs=%d", f.hits, f.nzbHits)
	}
}
