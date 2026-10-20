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
