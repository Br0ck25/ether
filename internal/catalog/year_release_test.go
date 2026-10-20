package catalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// A summary (TMDB's primary date, here a festival premiere) arriving after
// the details must not move the year away from the theatrical release date
// the details set: year filters and facets would disagree with released.
func TestSummaryUpsertKeepsYearWithDetailedReleaseDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/9" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "title": "Premiere", "release_date": "2019-09-01", "release_dates": map[string]any{"results": []any{map[string]any{"iso_3166_1": "US", "release_dates": []any{
			map[string]any{"type": 1, "release_date": "2019-09-01T00:00:00Z"},
			map[string]any{"type": 3, "release_date": "2020-02-01T00:00:00Z"},
		}}}}})
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	c := newTestCatalog(t, srv)
	got, err := c.Get(ctx, core.MovieKey(9))
	if err != nil {
		t.Fatal(err)
	}
	if got.Year != 2020 || got.Released.Year() != 2020 {
		t.Fatalf("detail: year %d released %v", got.Year, got.Released)
	}
	summary := core.Title{Key: core.MovieKey(9), Type: core.TypeMovie, IDs: core.IDs{TMDB: 9}, Title: "Premiere", Released: tmdb.ParseDate("2019-09-01")}
	if err := c.UpsertTitle(ctx, summary, true); err != nil {
		t.Fatal(err)
	}
	got, err = c.CachedTitle(ctx, core.MovieKey(9))
	if err != nil {
		t.Fatal(err)
	}
	if got.Released.Year() != 2020 || got.Year != got.Released.Year() {
		t.Fatalf("after summary: year %d released %v", got.Year, got.Released.Format(time.DateOnly))
	}
}
