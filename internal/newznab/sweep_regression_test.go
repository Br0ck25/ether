package newznab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestMalformedNZBURLDoesNotLeakAPIKey(t *testing.T) {
	c := New(testConfig("https://indexer.invalid"), nil, Options{})
	for _, link := range []string{
		"https://indexer.invalid/get%zz?apikey=very-secret-key",
		"/get%zz?apikey=very-secret-key",
	} {
		_, err := c.FetchNZB(context.Background(), core.Release{Name: "x", NZBURL: link})
		if err == nil || strings.Contains(err.Error(), "very-secret-key") {
			t.Fatalf("%s: err %v", link, err)
		}
		rel := core.Release{Name: "x", Protocol: core.ProtocolTorrent, NZBURL: link}
		if _, err := c.ResolveTorrent(context.Background(), rel); err == nil || strings.Contains(err.Error(), "very-secret-key") {
			t.Fatalf("torrent %s: err %v", link, err)
		}
	}
}

func TestRetryAfterIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if got := retryTime("31536000", now); got.After(now.Add((24 * time.Hour))) {
		t.Fatalf("seconds pause until %v", got)
	}
	if got := retryTime(now.AddDate(1, 0, 0).Format(http.TimeFormat), now); got.After(now.Add((24 * time.Hour))) {
		t.Fatalf("date pause until %v", got)
	}
	if got := retryTime("600", now); !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("ordinary pause %v", got)
	}
}

func TestRateLimitPauseSeenByOtherNodes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	var nodes []*Client
	for i := 0; i < 2; i++ {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		nodes = append(nodes, New(testConfig("http://127.0.0.1:1"), db, Options{}))
	}
	if err := nodes[0].reserve(ctx, "hit"); err != nil {
		t.Fatal(err)
	}
	_ = nodes[1].recordRateLimit(ctx, "600", errors.New("throttled"))
	if err := nodes[0].reserve(ctx, "hit"); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("node 0 ignored node 1's pause: %v", err)
	}
}

func TestFailedCapsNotRefetchedEverySearch(t *testing.T) {
	var caps atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			caps.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	defer srv.Close()
	c := New(testConfig(srv.URL), nil, Options{SearchCacheTTL: -1})
	for i := 0; i < 3; i++ {
		if _, err := c.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "The Signal", Year: 1999}); err != nil {
			t.Fatal(err)
		}
	}
	if caps.Load() != 1 {
		t.Fatalf("caps requested %d times", caps.Load())
	}
	r, _ := c.Remaining(context.Background())
	if r.HitsUsed != 4 {
		t.Fatalf("hits used %d, want 1 caps + 3 searches", r.HitsUsed)
	}
}

func TestPubDateSingleDigitDay(t *testing.T) {
	if parseDate("Mon, 5 Oct 2026 10:00:00 +0000").IsZero() {
		t.Fatal("single-digit day not parsed")
	}
}

func TestCapsRetriedAfterCredentialsChange(t *testing.T) {
	var caps atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			caps.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	c := New(cfg, nil, Options{SearchCacheTTL: -1})
	_, _ = c.Caps(context.Background())
	cfg.APIKey = "fixed"
	c.UpdateConfig(cfg)
	_, _ = c.Caps(context.Background())
	if caps.Load() != 2 {
		t.Fatalf("caps requested %d times", caps.Load())
	}
}

func TestFeedWithUnescapedAmpersandParses(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?><rss><channel><item><title>Tom.and.Jerry.1992.1080p.WEB-DL-GRP</title><guid>g1</guid><link>https://x/api?t=get&id=1&apikey=k</link><description>Tom & Jerry&nbsp;</description><enclosure url="https://x/api?t=get&amp;id=1" length="100" type="application/x-nzb"/></item></channel></rss>`
	got, err := parseFeed([]byte(body), "idx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GUID != "g1" || got[0].NZBURL != "https://x/api?t=get&id=1&apikey=k" {
		t.Fatalf("got %+v", got)
	}
}

// Caps belong to the endpoint they were fetched from: an indexer edited to
// a new URL or key fetches the new server's caps, in memory and from a
// fresh client sharing the database.
func TestCapsFollowEndpointChange(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, b := newFakeIndexer(t, ""), newFakeIndexer(t, "")
	cfg := testConfig(a.server.URL)
	c := New(cfg, db, Options{SearchCacheTTL: -1})
	if _, err := c.Caps(ctx); err != nil {
		t.Fatal(err)
	}
	cfg.URL = b.server.URL
	c.UpdateConfig(cfg)
	if _, err := c.Caps(ctx); err != nil {
		t.Fatal(err)
	}
	if b.hitsFor("caps") != 1 {
		t.Fatalf("edited client: new endpoint caps hits = %d, want 1", b.hitsFor("caps"))
	}
	// A fresh client for the same indexer id at a third endpoint must not
	// load the stored caps of the previous one.
	c3 := newFakeIndexer(t, "")
	cfg.URL = c3.server.URL
	if _, err := New(cfg, db, Options{SearchCacheTTL: -1}).Caps(ctx); err != nil {
		t.Fatal(err)
	}
	if c3.hitsFor("caps") != 1 {
		t.Fatalf("fresh client: caps hits = %d, want 1", c3.hitsFor("caps"))
	}
	// The same endpoint again is served from the database.
	if _, err := New(cfg, db, Options{SearchCacheTTL: -1}).Caps(ctx); err != nil {
		t.Fatal(err)
	}
	if c3.hitsFor("caps") != 1 {
		t.Fatalf("same endpoint refetched: caps hits = %d", c3.hitsFor("caps"))
	}
}
