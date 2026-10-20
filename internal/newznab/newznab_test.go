package newznab

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

const fakeNZB = `<?xml version="1.0"?><nzb><file poster="a" date="1" subject="x"><groups><group>alt.binaries.test</group></groups><segments><segment bytes="10" number="1">m1@test</segment></segments></file></nzb>`

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time      { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) add(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }

type fakeIndexer struct {
	mu      sync.Mutex
	mode    string // "", "badkey", "limit", "json", "emptyimdb"
	hits    map[string]int
	nzbHits int
	gzipNZB bool
	server  *httptest.Server
}

func loadFixture(t *testing.T, name, base string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "{{BASE}}", base)
}

func newFakeIndexer(t *testing.T, mode string) *fakeIndexer {
	t.Helper()
	f := &fakeIndexer{mode: mode, hits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.hits[q.Get("t")]++
		f.mu.Unlock()
		base := "http://" + r.Host
		switch f.mode {
		case "badkey":
			fmt.Fprint(w, `<?xml version="1.0"?><error code="100" description="Incorrect user credentials"/>`)
			return
		case "limit":
			fmt.Fprint(w, `<?xml version="1.0"?><error code="500" description="Request limit reached"/>`)
			return
		}
		switch q.Get("t") {
		case "caps":
			fmt.Fprint(w, loadFixture(t, "caps.xml", base))
		case "movie":
			if f.mode == "emptyimdb" && q.Get("imdbid") != "" {
				fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
				return
			}
			fmt.Fprint(w, loadFixture(t, "movie.xml", base))
		case "tvsearch":
			fmt.Fprint(w, loadFixture(t, "tv.xml", base))
		case "search":
			if f.mode == "json" {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"rss":{"channel":{"item":[{"title":"The.Signal.1999.1080p.BluRay.x264-GRP","guid":"json-1","link":%q,"pubDate":"Mon, 02 Jan 2023 15:04:05 +0000","enclosure":{"@attributes":{"url":%q,"length":"8000000000"}},"attr":[{"@attributes":{"name":"size","value":"8000000000"}},{"@attributes":{"name":"grabs","value":"9"}}]}]}}}`, base+"/nzb?id=json-1", base+"/nzb?id=json-1")
				return
			}
			fmt.Fprint(w, loadFixture(t, "movie.xml", base))
		case "get":
			fmt.Fprint(w, fakeNZB)
		default:
			fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
		}
	})
	mux.HandleFunc("/nzb", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.nzbHits++
		gz := f.gzipNZB
		f.mu.Unlock()
		if gz {
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			gw.Write([]byte(fakeNZB))
			gw.Close()
			w.Write(buf.Bytes())
			return
		}
		fmt.Fprint(w, fakeNZB)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIndexer) hitsFor(t string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[t]
}

func testConfig(url string) config.Indexer {
	return config.Indexer{ID: "idx1", Name: "Fake", URL: url, APIKey: "secret", Enabled: true}
}

func TestSearchMovieByIMDB(t *testing.T) {
	f := newFakeIndexer(t, "")
	c := New(testConfig(f.server.URL), nil, Options{})
	res, err := c.Search(context.Background(), core.SearchRequest{
		Type: core.TypeMovie, IDs: core.IDs{IMDB: "tt0133093"}, Title: "The Signal", Year: 1999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results %+v", len(res), res)
	}
	var main *core.Release
	for i := range res {
		if res[i].GUID == "guid-matrix-1080" {
			main = &res[i]
		}
		if res[i].GUID == "guid-matrix-720" && !res[i].Password {
			t.Error("password flag not parsed")
		}
	}
	if main == nil || main.Size != 8000000000 || main.Grabs != 42 || main.Category != 2040 || main.Info.Height != 1080 || main.ID == "" {
		t.Fatalf("bad release %+v", main)
	}
	if f.hitsFor("movie") != 1 {
		t.Errorf("movie hits = %d", f.hitsFor("movie"))
	}
}

func TestSearchFallbackToTMDB(t *testing.T) {
	f := newFakeIndexer(t, "emptyimdb")
	c := New(testConfig(f.server.URL), nil, Options{})
	res, err := c.Search(context.Background(), core.SearchRequest{
		Type: core.TypeMovie, IDs: core.IDs{IMDB: "tt0133093", TMDB: 603}, Title: "The Signal", Year: 1999,
	})
	if err != nil || len(res) != 2 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	if f.hitsFor("movie") != 2 {
		t.Errorf("expected imdb+tmdb attempts, movie hits=%d", f.hitsFor("movie"))
	}
}

func TestSearchTV(t *testing.T) {
	f := newFakeIndexer(t, "")
	c := New(testConfig(f.server.URL), nil, Options{})
	res, err := c.Search(context.Background(), core.SearchRequest{
		Type: core.TypeEpisode, IDs: core.IDs{TVDB: 81189}, Title: "Boundary Bay", Season: 1, Episode: 1,
	})
	if err != nil || len(res) != 1 || res[0].Info.Episodes[0] != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSearchJSON(t *testing.T) {
	f := newFakeIndexer(t, "json")
	c := New(testConfig(f.server.URL), nil, Options{})
	res, err := c.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "The Signal"})
	if err != nil || len(res) != 1 || res[0].Grabs != 9 || res[0].Size != 8000000000 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestErrorMapping(t *testing.T) {
	f := newFakeIndexer(t, "limit")
	c := New(testConfig(f.server.URL), nil, Options{})
	_, err := c.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "X"})
	if !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("err=%v want budget", err)
	}
	f2 := newFakeIndexer(t, "badkey")
	err = Test(context.Background(), testConfig(f2.server.URL))
	if err == nil || !strings.Contains(err.Error(), "incorrect API key") {
		t.Fatalf("Test err=%v", err)
	}
}

func TestCapsAndTestOK(t *testing.T) {
	f := newFakeIndexer(t, "")
	if err := Test(context.Background(), testConfig(f.server.URL)); err != nil {
		t.Fatal(err)
	}
	c := New(testConfig(f.server.URL), nil, Options{})
	caps, err := c.Caps(context.Background())
	if err != nil || !caps.MovieAvail || !caps.TVAvail || caps.LimitMax != 100 {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}
	// Second call served from cache: only one caps hit for this client
	// (plus one from Test above = 2 total).
	if _, err := c.Caps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.hitsFor("caps") != 2 {
		t.Errorf("caps hits=%d want 2", f.hitsFor("caps"))
	}
}

func TestBudgetsDayBoundary(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := &fakeClock{t: time.Date(2024, 5, 1, 23, 0, 0, 0, time.UTC)}
	f := newFakeIndexer(t, "")
	cfg := testConfig(f.server.URL)
	cfg.DailyLimit = 3 // caps(1) + search(1) + search(1)
	c := New(cfg, db, Options{Now: clock.now, SearchCacheTTL: -1})
	for _, title := range []string{"One", "Two"} {
		if _, err := c.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: title}); err != nil {
			t.Fatalf("search %s: %v", title, err)
		}
	}
	_, err = c.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Three"})
	if !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("third err=%v want budget", err)
	}
	rem, err := c.Remaining(ctx)
	if err != nil || rem.HitsRemaining != 0 || rem.HitsUsed != 3 {
		t.Fatalf("remaining %+v err=%v", rem, err)
	}
	// Cross UTC midnight: budget resets (caps may be re-fetched from DB cache).
	clock.add(2 * time.Hour)
	rem, _ = c.Remaining(ctx)
	if rem.HitsUsed != 0 || rem.HitsRemaining != 3 {
		t.Fatalf("after midnight %+v", rem)
	}
	if _, err := c.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Three"}); err != nil {
		t.Fatalf("search after midnight: %v", err)
	}
}

func TestResultCacheTTL(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := &fakeClock{t: time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)}
	f := newFakeIndexer(t, "")
	c := New(testConfig(f.server.URL), db, Options{Now: clock.now, SearchCacheTTL: time.Hour})
	req := core.SearchRequest{Type: core.TypeMovie, Title: "The Signal", Year: 1999}
	if _, err := c.Search(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.hitsFor("search") != 1 {
		t.Fatalf("search hits=%d want 1 (cached)", f.hitsFor("search"))
	}
	clock.add(2 * time.Hour)
	if _, err := c.Search(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.hitsFor("search") != 2 {
		t.Fatalf("search hits=%d want 2 after TTL", f.hitsFor("search"))
	}
}

func TestFetchNZBGzipAndDiskReuse(t *testing.T) {
	f := newFakeIndexer(t, "")
	f.gzipNZB = true
	cfg := testConfig(f.server.URL)
	cfg.DailyGrabs = 1
	c := New(cfg, nil, Options{NZBDir: t.TempDir()})
	r := core.Release{ID: "abc123", GUID: "g1", Indexer: "Fake", Name: "X", NZBURL: f.server.URL + "/nzb?id=g1"}
	b, err := c.FetchNZB(context.Background(), r)
	if err != nil || string(b) != fakeNZB {
		t.Fatalf("b=%q err=%v", b, err)
	}
	// Second fetch hits disk, not the grab budget or the server.
	b, err = c.FetchNZB(context.Background(), r)
	if err != nil || string(b) != fakeNZB {
		t.Fatalf("second b=%q err=%v", b, err)
	}
	f.mu.Lock()
	n := f.nzbHits
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("nzb server hits=%d want 1", n)
	}
}

func TestFetchNZBTooLarge(t *testing.T) {
	f := newFakeIndexer(t, "")
	c := New(testConfig(f.server.URL), nil, Options{MaxNZBBytes: 10})
	r := core.Release{ID: "x", NZBURL: f.server.URL + "/nzb?id=1"}
	if _, err := c.FetchNZB(context.Background(), r); err == nil {
		t.Fatal("expected size error")
	}
}

func TestManagerMergeDedupeSkipExhausted(t *testing.T) {
	ctx := context.Background()
	f1 := newFakeIndexer(t, "")
	f2 := newFakeIndexer(t, "")
	cfg1 := testConfig(f1.server.URL)
	cfg1.ID, cfg1.Name, cfg1.Priority = "a", "A", 1
	cfg2 := testConfig(f2.server.URL)
	cfg2.ID, cfg2.Name, cfg2.Priority, cfg2.DailyLimit = "b", "B", 2, 1
	m := NewManager(nil, []config.Indexer{cfg1, cfg2}, Options{SearchCacheTTL: -1})
	// Exhaust B.
	for _, cl := range m.Clients() {
		if cl.Name() == "B" {
			if err := cl.reserve(ctx, "hit"); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, err := m.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "The Signal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("merged=%d want 2", len(res))
	}
	if f2.hitsFor("search") != 0 {
		t.Error("exhausted indexer was queried")
	}
	// Re-enable B with no limit via Reload; both return same releases,
	// dedupe keeps 2.
	cfg2.DailyLimit = 0
	m.Reload([]config.Indexer{cfg1, cfg2})
	// Fresh clients cached searches under -1 TTL? cache disabled, fine,
	// but A kept its caps cache. New search with a different title text path.
	res, err = m.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "The Signal"})
	if err != nil || len(res) != 2 {
		t.Fatalf("after reload res=%d err=%v", len(res), err)
	}
	// FetchNZB routes by indexer name.
	r := res[0]
	b, err := m.FetchNZB(ctx, r)
	if err != nil || string(b) != fakeNZB {
		t.Fatalf("manager fetch err=%v", err)
	}
}
