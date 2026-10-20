package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// Episode rating keys hold seasons below 1000 and episodes below 10000.
// Year-numbered seasons (season 2023) must not be stored under keys that
// decode as another show's episode.
func TestOutOfRangeEpisodeNumbersDoNotCollide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tv/300":
			fmt.Fprint(w, `{"id":300,"name":"Annual Contest","first_air_date":"2000-05-01","number_of_seasons":2,"seasons":[{"id":1,"name":"2023","season_number":2023,"episode_count":1,"air_date":"2023-05-01"},{"id":2,"name":"Season 1","season_number":1,"episode_count":1,"air_date":"2000-05-01"}]}`)
		case "/tv/300/season/2023":
			fmt.Fprint(w, `{"id":1,"name":"2023","season_number":2023,"air_date":"2023-05-01","episodes":[{"id":9,"name":"Final","episode_number":1,"season_number":2023,"air_date":"2023-05-13"}]}`)
		case "/tv/300/season/1":
			fmt.Fprint(w, `{"id":2,"name":"Season 1","season_number":1,"air_date":"2000-05-01","episodes":[{"id":8,"name":"First","episode_number":1,"season_number":1,"air_date":"2000-05-01"},{"id":7,"name":"Huge","episode_number":12345,"season_number":1,"air_date":"2000-05-02"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	c := newTestCatalog(t, srv)
	if _, err := c.Children(ctx, core.SeasonKey(300, 2023)); err != nil && !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	eps, err := c.Children(ctx, core.SeasonKey(300, 1))
	if err != nil {
		t.Fatal(err)
	}
	// EpisodeKey(300, 2023, 1) decodes as show 302, season 23, episode 1;
	// EpisodeKey(300, 1, 12345) as show 300, season 2, episode 2345.
	for _, k := range []core.RatingKey{core.EpisodeKey(302, 23, 1), core.EpisodeKey(300, 2, 2345)} {
		if got, err := c.CachedTitle(ctx, k); err == nil {
			t.Fatalf("out-of-range episode stored under %v (%+v)", k, got)
		}
	}
	if len(eps) != 1 || eps[0].Key != core.EpisodeKey(300, 1, 1) {
		t.Fatalf("season 1 episodes = %+v", eps)
	}
}
