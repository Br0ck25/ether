package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestAirDateGateIncludesTodayAndHidesFutureSeasons(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/tv/100":
			body = map[string]any{"id": 100, "name": "Landman fixture", "first_air_date": "2024-11-17", "seasons": []any{
				map[string]any{"season_number": 1, "air_date": "2024-11-17", "episode_count": 3},
				map[string]any{"season_number": 3, "air_date": tomorrow, "episode_count": 1},
				map[string]any{"season_number": 4, "episode_count": 1},
			}}
		case "/tv/100/season/1":
			body = map[string]any{"id": 1, "season_number": 1, "air_date": "2024-11-17", "episodes": []any{
				map[string]any{"id": 1, "season_number": 1, "episode_number": 1, "air_date": today},
				map[string]any{"id": 2, "season_number": 1, "episode_number": 2, "air_date": tomorrow},
				map[string]any{"id": 3, "season_number": 1, "episode_number": 3},
			}}
		case "/tv/100/season/3":
			body = map[string]any{"id": 3, "season_number": 3, "air_date": tomorrow}
		case "/tv/100/season/1/episode/1/external_ids":
			body = map[string]any{}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	c := newTestCatalog(t, srv)
	ctx := context.Background()
	seasons, err := c.Children(ctx, core.ShowKey(100))
	if err != nil || len(seasons) != 1 || seasons[0].Season != 1 {
		t.Fatalf("seasons: %+v %v", seasons, err)
	}
	episodes, err := c.Children(ctx, core.SeasonKey(100, 1))
	if err != nil || len(episodes) != 1 || episodes[0].Episode != 1 {
		t.Fatalf("episodes: %+v %v", episodes, err)
	}
	all, err := c.Grandchildren(ctx, core.ShowKey(100))
	if err != nil || len(all) != 1 {
		t.Fatalf("grandchildren: %+v %v", all, err)
	}
	if _, err = c.Get(ctx, core.SeasonKey(100, 3)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("future season accessible: %v", err)
	}
	for _, ep := range []int{2, 3} {
		if _, err = c.Get(ctx, core.EpisodeKey(100, 1, ep)); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("unaired episode accessible: %v", err)
		}
	}
	if _, err = c.NextEpisode(ctx, core.EpisodeKey(100, 1, 1)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("up next returned unaired content: %v", err)
	}
}

func TestMovieTheatricalDateAndRemoteLists(t *testing.T) {
	future := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		released := map[string]any{"id": 1, "title": "Released today", "release_date": today}
		upcoming := map[string]any{"id": 2, "title": "Future Film", "release_date": future}
		unknown := map[string]any{"id": 3, "title": "Unknown date"}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/9":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "title": "Premiere before cinemas", "release_date": "2020-01-01", "release_dates": map[string]any{"results": []any{map[string]any{"iso_3166_1": "US", "release_dates": []any{
				map[string]any{"type": 1, "release_date": "2020-01-01T00:00:00Z"},
				map[string]any{"type": 3, "release_date": future + "T00:00:00Z"},
				map[string]any{"type": 4, "release_date": "2099-01-01T00:00:00Z"},
			}}}}})
		case "/movie/popular", "/movie/1/recommendations", "/movie/1/similar", "/search/movie":
			_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "total_pages": 1, "results": []any{released, upcoming, unknown}})
		case "/collection/1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": "A franchise", "parts": []any{released, upcoming, unknown}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := newTestCatalog(t, srv)
	ctx := context.Background()
	if _, err := c.Get(ctx, core.MovieKey(9)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("festival premiere exposed film before theatrical release: %v", err)
	}
	check := func(name string, ts []core.Title, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, v := range ts {
			if !v.IsReleased(time.Now()) {
				t.Fatalf("%s exposed %+v", name, v)
			}
		}
	}
	items, err := c.List(ctx, "popular", core.TypeMovie, 20)
	check("list", items, err)
	if len(items) != 1 {
		t.Fatalf("expected today's movie: %+v", items)
	}
	items, err = c.List(ctx, "popular", core.TypeMovie, 20)
	check("cached list", items, err)
	items, err = c.Search(ctx, "Film", []core.MediaType{core.TypeMovie}, 20)
	check("search", items, err)
	items, err = c.Similar(ctx, core.MovieKey(1), 20)
	check("similar", items, err)
	_, items, err = c.Collection(ctx, 1)
	check("franchise", items, err)
}
