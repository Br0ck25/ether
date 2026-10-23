package newznab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestRecentOneRequestNoCapsAndSharedAccountBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("t") != "search" || q.Get("q") != "" || q.Get("offset") != "100" || q.Get("limit") != "100" || q.Get("cat") != "2040,5000" {
			t.Errorf("unexpected request: %v", q)
		}
		fmt.Fprint(w, `<rss version="2.0"><channel><item><title>Film.2020.1080p.WEB-DL.x264</title><guid>one</guid></item></channel></rss>`)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.DailyLimit = 10
	cfg.MovieCategories = []int{2040}
	db := store.OpenTest(t)
	m := NewManager(db, []config.Indexer{cfg}, Options{})
	var charged atomic.Int32
	bg := core.WithForegroundReserve(core.WithBackgroundBudget(t.Context(), func(_ context.Context, kind, source string, n int64) error {
		if kind != "hit" {
			t.Errorf("unexpected cost %s", kind)
		}
		charged.Add(1)
		return nil
	}), 80)
	for i := 0; i < 2; i++ {
		rs, e := m.Recent(bg, cfg.ID, 100, 100)
		if e != nil || len(rs) != 1 {
			t.Fatal(rs, e)
		}
	}
	if _, e := m.Recent(bg, cfg.ID, 100, 100); !errors.Is(e, core.ErrBudgetExhausted) {
		t.Fatal("background invaded reserve", e)
	}
	if calls.Load() != 2 {
		t.Fatal("denied work hit network")
	}
	// Foreground still uses the account's remaining allowance.
	if _, e := m.Recent(t.Context(), cfg.ID, 100, 100); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 3 || charged.Load() != 3 {
		t.Fatal("request accounting", calls.Load(), charged.Load())
	}
}
func TestRecentDeniedBeforeNetwork(t *testing.T) {
	f := newFakeIndexer(t, "")
	cfg := testConfig(f.server.URL)
	m := NewManager(nil, []config.Indexer{cfg}, Options{})
	ctx := core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error { return core.ErrBudgetExhausted })
	if _, e := m.Recent(ctx, cfg.ID, 0, 100); !errors.Is(e, core.ErrBudgetExhausted) {
		t.Fatal(e)
	}
	if f.hitsFor("search")+f.hitsFor("caps") != 0 {
		t.Fatal("spent denied allowance")
	}
}

func TestBackgroundCannotConsumeTinyForegroundReserve(t *testing.T) {
	f := newFakeIndexer(t, "")
	cfg := testConfig(f.server.URL)
	cfg.DailyLimit = 1
	m := NewManager(nil, []config.Indexer{cfg}, Options{})
	ctx := core.WithForegroundReserve(core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error { return nil }), 80)
	if _, e := m.Recent(ctx, cfg.ID, 0, 100); !errors.Is(e, core.ErrBudgetExhausted) {
		t.Fatal("rounded away reserved capacity", e)
	}
	if f.hitsFor("search") != 0 {
		t.Fatal("spent foreground reserve")
	}
}

func TestRecentLearnsCategoryFallbackAndPersists(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("cat"))
		if r.URL.Query().Get("cat") != "" {
			fmt.Fprint(w, `<rss><channel/></rss>`)
			return
		}
		fmt.Fprint(w, `<rss><channel><item><title>Film.2026.1080p.WEB-DL</title><guid>new</guid></item></channel></rss>`)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	db := store.OpenTest(t)
	m := NewManager(db, []config.Indexer{cfg}, Options{})
	var costs atomic.Int32
	ctx := core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error { costs.Add(1); return nil })
	rs, err := m.Recent(ctx, cfg.ID, 0, 100)
	if err != nil || len(rs) != 1 {
		t.Fatal(rs, err)
	}
	restarted := NewManager(db, []config.Indexer{cfg}, Options{})
	if _, err = restarted.Recent(ctx, cfg.ID, 100, 100); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 3 || queries[0] != "2000,5000" || queries[1] != "" || queries[2] != "" || costs.Load() != 3 {
		t.Fatalf("requests=%v costs=%d", queries, costs.Load())
	}
	cfg.APIKey = "changed-account"
	changed := NewManager(db, []config.Indexer{cfg}, Options{})
	if _, err = changed.Recent(ctx, cfg.ID, 0, 100); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 5 || queries[3] != "2000,5000" {
		t.Fatalf("reused other account's mode: %v", queries)
	}
}

func TestRecentEmptyFeedProbeIsBounded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `<rss><channel/></rss>`) }))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	m := NewManager(store.OpenTest(t), []config.Indexer{cfg}, Options{})
	for i := 0; i < 3; i++ {
		if _, err := m.Recent(t.Context(), cfg.ID, 0, 100); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("repeated discovery probes on an empty feed: %d", calls.Load())
	}
}
func TestRecentFallbackDeniedDoesNotClaimEmptySuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `<rss><channel/></rss>`) }))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	m := NewManager(nil, []config.Indexer{cfg}, Options{})
	var reservations atomic.Int32
	ctx := core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error {
		if reservations.Add(1) > 1 {
			return core.ErrBudgetExhausted
		}
		return nil
	})
	if _, err := m.Recent(ctx, cfg.ID, 0, 100); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("expected budget denial: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("denied fallback reached provider")
	}
}
