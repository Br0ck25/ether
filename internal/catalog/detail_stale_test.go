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

func TestSettledMovieAndEndedShowDetailsKeptForever(t *testing.T) {
	ctx := context.Background()
	inner := fakeTMDB(t)
	target, _ := url.Parse(inner.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var movieDetails, endedShowDetails atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/550":
			movieDetails.Add(1)
		case "/tv/200":
			endedShowDetails.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":200,"name":"Finished Masterpiece","first_air_date":"2015-01-10","status":"Ended","episode_run_time":[55],"number_of_seasons":2,"number_of_episodes":20,"vote_average":9.1,"vote_count":8000,"popularity":45.0,"original_language":"en","genres":[{"id":18,"name":"Drama"}],"created_by":[{"id":901,"name":"Creator One","profile_path":"/c1.jpg"}],"credits":{"cast":[{"id":287,"name":"Theo Varga","character":"Lead"}],"crew":[{"id":902,"name":"Roger Lens","job":"Director of Photography"},{"id":903,"name":"Score Master","job":"Original Music Composer"}]},"seasons":[{"id":2001,"name":"Season 1","season_number":1,"episode_count":10,"air_date":"2015-01-10","vote_average":8.8},{"id":2002,"name":"Season 2","season_number":2,"episode_count":10,"air_date":"2016-01-10","vote_average":9.4}]}`))
			return
		case "/person/999/combined_credits":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":999,"cast":[],"crew":[{"id":554,"media_type":"movie","title":"Old Classic","release_date":"1975-01-01","popularity":10.0,"vote_average":9.0,"vote_count":8000,"original_language":"en","job":"Director"}]}`))
			return
		case "/person/999":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":999,"name":"Legend Director","profile_path":"/ld.jpg"}`))
			return
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
	if _, err := c.Get(ctx, core.MovieKey(550)); err != nil {
		t.Fatal(err)
	}
	show, err := c.Get(ctx, core.ShowKey(200))
	if err != nil {
		t.Fatal(err)
	}
	if len(show.Crew) < 3 {
		t.Fatalf("expected Creator, Cinematographer and Composer in Crew, got %+v", show.Crew)
	}
	// Advance 30 days: neither the 1999 movie nor the Ended 2015 show should ever re-query TMDB.
	now = now.Add(30 * 24 * time.Hour)
	if _, err := c.Get(ctx, core.MovieKey(550)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, core.ShowKey(200)); err != nil {
		t.Fatal(err)
	}
	if movieDetails.Load() != 1 || endedShowDetails.Load() != 1 {
		t.Fatalf("settled titles refetched: movie=%d show=%d, want 1, 1", movieDetails.Load(), endedShowDetails.Load())
	}

	// Test WarmDetails warms remaining detail=0 titles once and never again.
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	warmed, err := c.WarmDetails(ctx, 10)
	if err != nil || warmed == 0 {
		t.Fatalf("WarmDetails first pass: warmed=%d err=%v", warmed, err)
	}
	// Test firstCharacter facet.
	fc, err := c.Facet(ctx, core.TypeMovie, "firstCharacter")
	if err != nil || len(fc) == 0 {
		t.Fatalf("firstCharacter facet: %+v %v", fc, err)
	}
	// Test ShowTrajectories on show 200 ("Sticks the Landing").
	trajs, err := c.ShowTrajectories(ctx, []core.RatingKey{core.ShowKey(200)})
	if err != nil || !trajs[core.ShowKey(200)].SticksLanding {
		t.Fatalf("ShowTrajectories: %+v %v", trajs, err)
	}
	// Test on-demand person filmography hydration via /person/999/combined_credits.
	page, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Person: 999}})
	if err != nil || page.Total != 1 || page.Items[0].Title != "Old Classic" {
		t.Fatalf("on-demand person credits browse: %+v %v", page, err)
	}
}
