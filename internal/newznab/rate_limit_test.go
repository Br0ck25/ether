package newznab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestServiceThrottleSurvivesRestartAndRetriesAutomatically(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter, body string
		status                 int
		pause                  time.Duration
	}{
		{"HTTP delta", "120", "", 429, 2 * time.Minute},
		{"HTTP date", "Mon, 19 Oct 2026 12:03:00 GMT", "", 429, 3 * time.Minute},
		{"XML daily API", "", `<error code="500" description="Allowance exhausted"/>`, 200, defaultRateLimitPause},
		{"JSON daily grabs", "", `{"error":{"code":501,"description":"Allowance exhausted"}}`, 200, defaultRateLimitPause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.OpenMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			clock := &fakeClock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.Header().Set("Retry-After", tc.retryAfter)
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				fmt.Fprint(w, fakeNZB)
			}))
			defer srv.Close()
			cfg := testConfig(srv.URL) // no personal cap
			opts := Options{Now: clock.now, NZBDir: t.TempDir()}
			c := New(cfg, db, opts)
			rel := core.Release{ID: "test", GUID: "test", Indexer: c.Name()}
			if _, err := c.FetchNZB(ctx, rel); !errors.Is(err, core.ErrBudgetExhausted) {
				t.Fatalf("throttle: %v", err)
			}
			c = New(cfg, db, opts)
			r, err := c.Remaining(ctx)
			if err != nil || r.RetryAt != clock.now().Add(tc.pause).Unix() || r.HitsLimit != 0 || r.GrabsLimit != 0 {
				t.Fatalf("status: %+v %v", r, err)
			}
			if _, err := c.FetchNZB(ctx, rel); !errors.Is(err, core.ErrBudgetExhausted) {
				t.Fatalf("persisted pause: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("retried during service pause")
			}
			clock.add(tc.pause + time.Second)
			if b, err := c.FetchNZB(ctx, rel); err != nil || string(b) != fakeNZB {
				t.Fatalf("automatic recovery: %v", err)
			}
			// Even a new throttle cannot block an NZB already cached on disk.
			c.recordRateLimit(ctx, "120", apiError(429, "limited"))
			if _, err := c.FetchNZB(ctx, rel); err != nil {
				t.Fatalf("cached NZB blocked: %v", err)
			}
			if requests.Load() != 2 {
				t.Fatal("cached NZB made another request")
			}
		})
	}
}

func TestServiceThrottleFallsBackAndKeepsCachedSearches(t *testing.T) {
	ctx := context.Background()
	f := newFakeIndexer(t, "")
	cfg := testConfig(f.server.URL)
	m := NewManager(nil, []config.Indexer{cfg}, Options{})
	req := core.SearchRequest{Type: core.TypeMovie, Title: "The Signal", Year: 1999}
	if _, err := m.Search(ctx, req); err != nil {
		t.Fatal(err)
	}
	primary := m.Clients()[0]
	primary.recordRateLimit(ctx, "120", apiError(429, "limited"))
	if res, err := m.Search(ctx, req); err != nil || len(res) == 0 {
		t.Fatalf("cached search: %v", err)
	}
	f.mu.Lock()
	before := f.hits["search"]
	f.mu.Unlock()
	if _, err := m.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "New title"}); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("uncached throttle: %v", err)
	}
	f.mu.Lock()
	after := f.hits["search"]
	f.mu.Unlock()
	if after != before {
		t.Fatal("uncached search hit throttled service")
	}
	backup := cfg
	backup.ID, backup.Name = "backup", "Backup"
	m.Reload([]config.Indexer{cfg, backup})
	if res, err := m.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "New title"}); err != nil || len(res) == 0 || res[0].Indexer != "Backup" {
		t.Fatalf("fallback: %v %v", res, err)
	}
}

func TestHTTPServerFailureDoesNotPauseAsQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "temporary server error")
	}))
	defer srv.Close()
	c := New(testConfig(srv.URL), nil, Options{})
	_, err := c.FetchNZB(context.Background(), core.Release{GUID: "test"})
	if err == nil || errors.Is(err, core.ErrBudgetExhausted) || c.Exhausted(context.Background()) {
		t.Fatalf("server failure treated as quota: %v", err)
	}
}

func TestClearingPersonalCapsPreservesUsage(t *testing.T) {
	ctx := context.Background()
	f := newFakeIndexer(t, "")
	cfg := testConfig(f.server.URL)
	cfg.DailyGrabs = 1
	c := New(cfg, nil, Options{})
	if _, err := c.FetchNZB(ctx, core.Release{GUID: "first"}); err != nil {
		t.Fatal(err)
	}
	if !c.Exhausted(ctx) {
		t.Fatal("personal cap not enforced")
	}
	cfg.DailyGrabs = 0
	c.UpdateConfig(cfg)
	if _, err := c.FetchNZB(ctx, core.Release{GUID: "second"}); err != nil {
		t.Fatal(err)
	}
	r, err := c.Remaining(ctx)
	if err != nil || r.GrabsUsed != 2 || r.GrabsLimit != 0 {
		t.Fatalf("usage lost: %+v %v", r, err)
	}
}
