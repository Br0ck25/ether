package livetv

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// tvCatalog has two movies, a show with three episodes and one with none.
type tvCatalog struct{ core.Catalog }

var (
	tvMovies = []core.Title{
		{Key: core.MovieKey(1), Type: core.TypeMovie, Title: "M1", Runtime: time.Hour},
		{Key: core.MovieKey(2), Type: core.TypeMovie, Title: "M2", Runtime: time.Hour},
	}
	tvShows = []core.Title{
		{Key: core.ShowKey(10), Type: core.TypeShow, Title: "S1"},
		{Key: core.ShowKey(11), Type: core.TypeShow, Title: "S2"},
	}
)

func (tvCatalog) Browse(_ context.Context, q core.BrowseQuery) (core.Page, error) {
	switch q.Filter.Type {
	case core.TypeMovie:
		return core.Page{Items: tvMovies, Total: len(tvMovies)}, nil
	case core.TypeShow:
		return core.Page{Items: tvShows, Total: len(tvShows)}, nil
	}
	return core.Page{}, nil
}

func (tvCatalog) Grandchildren(_ context.Context, k core.RatingKey) ([]core.Title, error) {
	if k != core.ShowKey(10) {
		return nil, nil
	}
	var eps []core.Title
	for i := int64(1); i <= 3; i++ {
		eps = append(eps, core.Title{Key: core.EpisodeKey(10, 1, int(i)), Type: core.TypeEpisode, Title: "E", Runtime: 30 * time.Minute})
	}
	return eps, nil
}

func keysOf(items []Item) []core.RatingKey {
	var out []core.RatingKey
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

// A hand-picked show becomes its episodes, in place; movies stay as picked.
func TestHandPickedShowsExpandToEpisodes(t *testing.T) {
	s := testService(t, Options{Catalog: tvCatalog{}})
	c := Channel{Kind: "virtual", Items: []Item{
		{Key: core.MovieKey(1), DurationMS: 3600000},
		{Key: core.ShowKey(10)},
		{Key: core.MovieKey(2), DurationMS: 3600000},
	}}
	if err := s.expand(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	got := keysOf(c.Items)
	want := []core.RatingKey{core.MovieKey(1), core.EpisodeKey(10, 1, 1), core.EpisodeKey(10, 1, 2), core.EpisodeKey(10, 1, 3), core.MovieKey(2)}
	if len(got) != len(want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items %v, want %v", got, want)
		}
	}
}

// A mixed channel alternates movies and shows rather than playing one kind first.
func TestMixedChannelAlternatesMoviesAndShows(t *testing.T) {
	s := testService(t, Options{Catalog: tvCatalog{}})
	c := Channel{Kind: "virtual", Filter: &core.Filter{Type: MixedType}}
	if err := s.expand(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	got := keysOf(c.Items)
	// M1, then S1's three episodes (S2 has none), then M2.
	if len(got) != 5 || got[0] != core.MovieKey(1) || got[4] != core.MovieKey(2) || got[1].Kind() != core.KindEpisode {
		t.Fatalf("mixed order: %v", got)
	}
}
