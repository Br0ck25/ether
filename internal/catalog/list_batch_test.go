package catalog

import (
	"context"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
	"testing"
	"time"
)

type summaryListClient struct{ TMDB }

func (summaryListClient) TrendingMovies(context.Context, string, int) (*tmdb.Paged[tmdb.Movie], error) {
	return &tmdb.Paged[tmdb.Movie]{TotalPages: 1, Results: []tmdb.Movie{
		{ID: 2, Title: "Second", ReleaseDate: "2000-01-01", Popularity: 1},
		{ID: 1, Title: "First", ReleaseDate: "2000-01-01", Popularity: 50},
	}}, nil
}
func TestListBatchPreservesOrderDetailsAndThreshold(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := New(db, summaryListClient{}, config.Catalog{MinPopularity: 10}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, Title: "First", Released: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Genres: []string{"Drama"}, Runtime: 90 * time.Minute}
	if err := c.upsert(ctx, first, upsertOpts{detail: true, browsable: true}); err != nil {
		t.Fatal(err)
	}
	got, err := c.List(ctx, "trending_day", core.TypeMovie, 20)
	if err != nil || len(got) != 2 || got[0].Key != core.MovieKey(2) || got[1].Key != first.Key {
		t.Fatalf("list=%+v err=%v", got, err)
	}
	if got[1].Runtime != 90*time.Minute || len(got[1].Genres) != 1 || got[1].Genres[0] != "Drama" {
		t.Fatalf("detail lost: %+v", got[1])
	}
	for key, want := range map[core.RatingKey]int{first.Key: 1, core.MovieKey(2): 0} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT browsable FROM catalog_titles WHERE key=?", key).Scan(&n); err != nil || n != want {
			t.Fatalf("key=%d browsable=%d err=%v", key, n, err)
		}
	}
	// A bad summary must not leave a partially persisted list.
	third := first
	third.Key = core.MovieKey(3)
	if err := c.upsertSummaries(ctx, []core.Title{third, {}}, false); err == nil {
		t.Fatal("invalid title accepted")
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM catalog_titles WHERE key=?", third.Key).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial list persisted: %d %v", n, err)
	}
}
