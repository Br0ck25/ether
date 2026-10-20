package catalog_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// Summary upserts (sync, trending lists) must not reset the staleness clock
// of a title's details; otherwise a popular show's details — and with them
// newly announced seasons — are never refetched.
func TestSummaryUpsertDoesNotKeepDetailsFresh(t *testing.T) {
	ctx := context.Background()
	inner := fakeTMDB(t)
	target, _ := url.Parse(inner.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var showDetails atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tv/100" {
			showDetails.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	client := tmdb.New("testkey", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithCacheTTL(0))
	t.Cleanup(client.Close)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c, err := catalog.New(db, client, config.Catalog{Language: "en-US", Region: "US", MinPopularity: 5, MinVotes: 100, RefreshInterval: config.Duration(24 * time.Hour)},
		slog.Default(), catalog.WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, core.ShowKey(100)); err != nil {
		t.Fatal(err)
	}
	if n := showDetails.Load(); n != 1 {
		t.Fatalf("detail fetches = %d, want 1", n)
	}
	// A day later the show reappears in the trending list (summary upsert).
	now = now.Add(25 * time.Hour)
	if _, err := c.List(ctx, "trending_day", core.TypeShow, 20); err != nil {
		t.Fatalf("trending: %v", err)
	}
	if _, err := c.Get(ctx, core.ShowKey(100)); err != nil {
		t.Fatal(err)
	}
	if n := showDetails.Load(); n != 2 {
		t.Fatalf("stale details were not refetched after a summary upsert (detail fetches = %d)", n)
	}
	// Freshly fetched details are served from the mirror.
	if _, err := c.Get(ctx, core.ShowKey(100)); err != nil || showDetails.Load() != 2 {
		t.Fatalf("fresh details refetched: %v n=%d", err, showDetails.Load())
	}
}
