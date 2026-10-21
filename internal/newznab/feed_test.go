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
