package trakt

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestPublicLists(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.Header.Get("trakt-api-key") != "test-client" || r.Header.Get("Authorization") != "" {
			http.Error(w, "bad headers", 401)
			return
		}
		if r.URL.Query().Get("limit") != "40" {
			http.Error(w, "limit", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movies/trending":
			fmt.Fprint(w, `[{"watchers":120,"movie":{"title":"Now","year":2026,"ids":{"trakt":1,"tmdb":11}}},{"watchers":80,"movie":{"title":"No TMDB","year":2026,"ids":{"trakt":2}}},{"watchers":60,"movie":{"title":"Later","year":2025,"ids":{"tmdb":12,"imdb":"tt12"}}}]`)
		case "/shows/watched/weekly":
			fmt.Fprint(w, `[{"watcher_count":900,"play_count":5000,"show":{"title":"Weekly","year":2024,"ids":{"tmdb":21,"tvdb":7}}}]`)
		case "/movies/popular":
			fmt.Fprint(w, `[{"title":"Bare","year":2010,"ids":{"tmdb":31}},{"title":"Bare","year":2010,"ids":{"tmdb":31}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	off := New(Options{Config: config.Trakt{}, BaseURL: srv.URL, Logger: quietLog()})
	if got, err := off.PublicList(ctx, PublicTrending, core.TypeMovie, 40); got != nil || err != nil {
		t.Fatalf("unconfigured Trakt = %v %v", got, err)
	}
	disabled := New(Options{Config: config.Trakt{ClientID: "test-client"}, BaseURL: srv.URL, Logger: quietLog()})
	if got, _ := disabled.PublicList(ctx, PublicTrending, core.TypeMovie, 40); got != nil {
		t.Fatal("a disabled Trakt integration must not be used")
	}

	// A client id alone (no secret, nobody signed in) is enough.
	c := New(Options{Config: config.Trakt{Enabled: true, ClientID: "test-client"}, BaseURL: srv.URL, Logger: quietLog()})
	got, err := c.PublicList(ctx, PublicTrending, core.TypeMovie, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != core.MovieKey(11) || got[1].Key != core.MovieKey(12) || got[1].IDs.IMDB != "tt12" || got[0].Type != core.TypeMovie {
		t.Fatalf("trending = %+v", got)
	}
	if _, err := c.PublicList(ctx, PublicTrending, core.TypeMovie, 40); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := hits["/movies/trending"]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("trending fetched %d times; want cached", n)
	}
	shows, err := c.PublicList(ctx, PublicWatchedWeekly, core.TypeShow, 40)
	if err != nil || len(shows) != 1 || shows[0].Key != core.ShowKey(21) || shows[0].Type != core.TypeShow {
		t.Fatalf("watched weekly = %+v %v", shows, err)
	}
	pop, err := c.PublicList(ctx, PublicPopular, core.TypeMovie, 40)
	if err != nil || len(pop) != 1 || pop[0].Key != core.MovieKey(31) {
		t.Fatalf("popular = %+v %v", pop, err)
	}
	if _, err := c.PublicList(ctx, PublicAnticipated, core.TypeShow, 40); err == nil {
		t.Fatal("HTTP 404 should be an error")
	}
	if _, err := c.PublicList(ctx, "bogus", core.TypeMovie, 40); err == nil {
		t.Fatal("unknown list accepted")
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
