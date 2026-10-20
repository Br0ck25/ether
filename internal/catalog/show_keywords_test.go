package catalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Seasons and episodes inherit the show's keywords, as they do its genres
// and content rating, so keyword-based policies and rules see them.
func TestChildrenInheritShowKeywords(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, nil)
	aired := time.Date(2020, 1, 10, 0, 0, 0, 0, time.UTC)
	show := core.Title{Key: core.ShowKey(900), Type: core.TypeShow, IDs: core.IDs{TMDB: 900}, Title: "Dark Show", Released: aired, Keywords: []string{"gore", "slasher"}}
	season := core.Title{Key: core.SeasonKey(900, 1), Type: core.TypeSeason, ShowKey: show.Key, Season: 1, Title: "Season 1", Released: aired}
	ep := core.Title{Key: core.EpisodeKey(900, 1, 1), Type: core.TypeEpisode, ShowKey: show.Key, Season: 1, Episode: 1, Title: "Pilot", Released: aired}
	for _, ti := range []core.Title{show, season, ep} {
		if err := c.UpsertTitle(ctx, ti, ti.Type == core.TypeShow); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []core.RatingKey{season.Key, ep.Key} {
		got, err := c.CachedTitle(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Keywords) != 2 || got.Keywords[0] != "gore" || got.Keywords[1] != "slasher" {
			t.Fatalf("%v keywords = %v, want the show's", k, got.Keywords)
		}
	}
}

// Fetching an episode's external ids re-saves the episode; the keywords it
// inherited from its show must not be persisted as its own, or later
// changes to the show's keywords never reach it.
func TestEpisodeDetailUpsertDoesNotPersistInheritedKeywords(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	show := core.Title{Key: core.ShowKey(100), Type: core.TypeShow, IDs: core.IDs{TMDB: 100}, Title: "Space Show",
		Released: time.Date(2020, 1, 10, 0, 0, 0, 0, time.UTC), Keywords: []string{"gore"}}
	if err := c.UpsertTitle(ctx, show, true); err != nil {
		t.Fatal(err)
	}
	ep, err := c.Get(ctx, core.EpisodeKey(100, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if ep.IDs.IMDB != "tt5001001" || len(ep.Keywords) != 1 || ep.Keywords[0] != "gore" {
		t.Fatalf("episode = %+v", ep)
	}
	show.Keywords = []string{"calm"}
	if err := c.UpsertTitle(ctx, show, true); err != nil {
		t.Fatal(err)
	}
	got, err := c.CachedTitle(ctx, core.EpisodeKey(100, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Keywords) != 1 || got.Keywords[0] != "calm" {
		t.Fatalf("episode keywords = %v, want the show's current [calm]", got.Keywords)
	}
}
