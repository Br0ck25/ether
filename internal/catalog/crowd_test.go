package catalog_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// crowdTMDB serves two pages of recommendations for movie 700 and show 300,
// /similar lists that must never be used, and movie details carrying a
// first page of recommendations. It counts requests per path.
type crowdTMDB struct {
	mu    sync.Mutex
	calls map[string]int
	fail  bool
}

func (f *crowdTMDB) count(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[p]
}

func (f *crowdTMDB) server(t *testing.T) *httptest.Server {
	f.calls = map[string]int{}
	movie := func(id int) string {
		return fmt.Sprintf(`{"id":%d,"title":"Rec %d","release_date":"2015-01-01","genre_ids":[18],"popularity":30,"vote_average":7.5,"vote_count":4000,"original_language":"en"}`, id, id)
	}
	page := func(ids []int, p, total int) string {
		var parts []string
		for _, id := range ids {
			parts = append(parts, movie(id))
		}
		return fmt.Sprintf(`{"page":%d,"results":[%s],"total_pages":%d,"total_results":40}`, p, strings.Join(parts, ","), total)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if p := r.URL.Query().Get("page"); p != "" && p != "1" {
			key += "?page=" + p
		}
		f.mu.Lock()
		f.calls[key]++
		fail := f.fail
		f.mu.Unlock()
		if fail {
			w.WriteHeader(404) // not retried, unlike 5xx
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/genre/movie/list", r.URL.Path == "/genre/tv/list":
			fmt.Fprint(w, `{"genres":[{"id":18,"name":"Drama"}]}`)
		case r.URL.Path == "/movie/700/recommendations":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, page([]int{711, 712}, 2, 2))
				return
			}
			fmt.Fprint(w, page([]int{701, 702, 703, 700}, 1, 2)) // a self-reference is dropped
		case r.URL.Path == "/movie/800":
			fmt.Fprint(w, `{"id":800,"title":"Detailed","release_date":"2010-01-01","vote_average":7,"vote_count":3000,"popularity":20,"original_language":"en","genres":[{"id":18,"name":"Drama"}],"credits":{"cast":[],"crew":[]},"recommendations":`+page([]int{801, 802}, 1, 1)+`}`)
		case r.URL.Path == "/tv/300/recommendations":
			fmt.Fprint(w, `{"page":1,"results":[{"id":301,"name":"Show Rec","first_air_date":"2019-01-01","popularity":40,"vote_average":8,"vote_count":900,"original_language":"en"}],"total_pages":1}`)
		case strings.HasSuffix(r.URL.Path, "/similar"):
			fmt.Fprint(w, page([]int{999}, 1, 1))
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"status_message":"not found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCrowdCatalog(t *testing.T, srv *httptest.Server, now func() time.Time) *catalog.Catalog {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	client := tmdb.New("testkey", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithCacheTTL(0))
	t.Cleanup(client.Close)
	c, err := catalog.New(db, client, config.Catalog{Language: "en-US", MinPopularity: 5, MinVotes: 100}, slog.Default(), catalog.WithNow(now))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func keysOf(ts []core.Title) []int64 {
	var out []int64
	for _, t := range ts {
		out = append(out, t.Key.TMDB())
	}
	return out
}

func TestCrowdRecommendationsCachedForDays(t *testing.T) {
	f := &crowdTMDB{}
	srv := f.server(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := newCrowdCatalog(t, srv, clock)
	ctx := context.Background()

	// Not stored yet and fetching disallowed: nothing, no network.
	got, fetched, err := c.CrowdRecommendations(ctx, core.MovieKey(700), 2, false)
	if err != nil || got != nil || fetched || f.count("/movie/700/recommendations") != 0 {
		t.Fatalf("cache-only miss = %v %v %v", got, fetched, err)
	}
	got, fetched, err = c.CrowdRecommendations(ctx, core.MovieKey(700), 2, true)
	if err != nil || !fetched {
		t.Fatalf("fetch = %v %v", fetched, err)
	}
	if fmt.Sprint(keysOf(got)) != "[701 702 703 711 712]" {
		t.Fatalf("recommendations = %v", keysOf(got))
	}
	if f.count("/movie/700/similar") != 0 {
		t.Fatal("crowd lists must never use /similar")
	}
	// Two days later: served from the store.
	now = now.Add(48 * time.Hour)
	got, fetched, err = c.CrowdRecommendations(ctx, core.MovieKey(700), 2, true)
	if err != nil || fetched || len(got) != 5 || f.count("/movie/700/recommendations") != 1 {
		t.Fatalf("cached = %v %v %v calls=%d", keysOf(got), fetched, err, f.count("/movie/700/recommendations"))
	}
	// Stale after the TTL: served stale without fetch, refetched with it.
	now = now.Add(48 * time.Hour)
	if got, fetched, _ = c.CrowdRecommendations(ctx, core.MovieKey(700), 2, false); fetched || len(got) != 5 {
		t.Fatalf("stale serve = %v %v", keysOf(got), fetched)
	}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	if got, fetched, err = c.CrowdRecommendations(ctx, core.MovieKey(700), 2, true); err != nil || !fetched || len(got) != 5 {
		t.Fatalf("TMDB down should serve the stale list: %v %v %v", keysOf(got), fetched, err)
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	if _, fetched, _ = c.CrowdRecommendations(ctx, core.MovieKey(700), 2, true); !fetched {
		t.Fatal("stale list should be refetched")
	}

	// Episodes resolve to their show.
	got, _, err = c.CrowdRecommendations(ctx, core.EpisodeKey(300, 1, 2), 1, true)
	if err != nil || len(got) != 1 || got[0].Key != core.ShowKey(301) || got[0].Type != core.TypeShow {
		t.Fatalf("show recommendations = %v %v", got, err)
	}
}

func TestCrowdRecommendationsHarvestedFromDetails(t *testing.T) {
	f := &crowdTMDB{}
	srv := f.server(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	c := newCrowdCatalog(t, srv, func() time.Time { return now })
	ctx := context.Background()
	if _, err := c.Get(ctx, core.MovieKey(800)); err != nil {
		t.Fatal(err)
	}
	got, fetched, err := c.CrowdRecommendations(ctx, core.MovieKey(800), 1, true)
	if err != nil || fetched || fmt.Sprint(keysOf(got)) != "[801 802]" {
		t.Fatalf("harvested = %v fetched=%v err=%v", keysOf(got), fetched, err)
	}
	if f.count("/movie/800/recommendations") != 0 {
		t.Fatal("the detail fetch's recommendations should be reused")
	}
	// Two pages wanted: the harvested single page is not enough.
	if _, fetched, _ := c.CrowdRecommendations(ctx, core.MovieKey(800), 2, false); fetched {
		t.Fatal("cache-only call fetched")
	}
}
