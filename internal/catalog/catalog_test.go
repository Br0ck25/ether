package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestGetMovieOnDemand(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	m, err := c.Get(ctx, core.MovieKey(550))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Fight Club" || m.IDs.IMDB != "tt0137523" || m.ContentRating != "R" || m.Runtime != 139*time.Minute {
		t.Fatalf("movie: %+v", m)
	}
	if m.CollectionID != 10 || m.CollectionName != "Saga Collection" || m.LogoPath != "/logo.png" {
		t.Fatalf("collection/logo: %+v", m)
	}
	if len(m.Cast) != 1 || m.Cast[0].Name != "Brad Pitt" || len(m.Directors) != 1 || m.Directors[0].Name != "David Fincher" || len(m.Writers) != 1 {
		t.Fatalf("credits: cast=%v dir=%v wr=%v", m.Cast, m.Directors, m.Writers)
	}
	if len(m.Genres) != 1 || m.Genres[0] != "Drama" || len(m.Studios) != 1 || len(m.Countries) != 1 {
		t.Fatalf("facets: %+v", m)
	}
	if len(m.Keywords) != 2 || m.Keywords[0] != "dual identity" {
		t.Fatalf("keywords: %v", m.Keywords)
	}
	if m.SortTitle != "fight club" {
		t.Fatalf("sort title %q", m.SortTitle)
	}
	// Cached in mirror: browsable because it meets thresholds.
	page, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}})
	if err != nil || page.Total != 1 {
		t.Fatalf("browse after get: %+v %v", page, err)
	}
}

func TestShowSeasonsEpisodes(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	show, err := c.Get(ctx, core.ShowKey(100))
	if err != nil {
		t.Fatal(err)
	}
	if show.ContentRating != "TV-MA" || show.IDs.TVDB != 777 || show.ChildCount != 2 || show.LeafCount != 3 {
		t.Fatalf("show: %+v", show)
	}
	seasons, err := c.Children(ctx, core.ShowKey(100))
	if err != nil || len(seasons) != 3 {
		t.Fatalf("seasons: %v %v", seasons, err)
	}
	if seasons[0].Season != 0 || seasons[1].Key != core.SeasonKey(100, 1) {
		t.Fatalf("season keys: %+v", seasons)
	}
	eps, err := c.Children(ctx, core.SeasonKey(100, 1))
	if err != nil || len(eps) != 2 {
		t.Fatalf("episodes: %v %v", eps, err)
	}
	ep := eps[0]
	if ep.Key != core.EpisodeKey(100, 1, 1) || ep.StillPath != "/pilot.jpg" || ep.Runtime != 45*time.Minute || ep.ShowIDs.TVDB != 777 || ep.ShowTitle != "Space Show" {
		t.Fatalf("episode: %+v", ep)
	}
	// Episode external ids enrich on Get.
	got, err := c.Get(ctx, core.EpisodeKey(100, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got.IDs.IMDB != "tt5001001" {
		t.Fatalf("episode imdb: %+v", got.IDs)
	}
	all, err := c.Grandchildren(ctx, core.ShowKey(100))
	if err != nil || len(all) != 4 { // specials 1 + s1 2 + s2 1
		t.Fatalf("grandchildren: %d %v", len(all), err)
	}
}

func TestNextEpisode(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	if _, err := c.Get(ctx, core.ShowKey(100)); err != nil {
		t.Fatal(err)
	}
	next, err := c.NextEpisode(ctx, core.EpisodeKey(100, 1, 1))
	if err != nil || next.Key != core.EpisodeKey(100, 1, 2) {
		t.Fatalf("next same season: %+v %v", next, err)
	}
	next, err = c.NextEpisode(ctx, core.EpisodeKey(100, 1, 2)) // crosses season boundary
	if err != nil || next.Key != core.EpisodeKey(100, 2, 1) {
		t.Fatalf("next cross season: %+v %v", next, err)
	}
	next, err = c.NextEpisode(ctx, core.EpisodeKey(100, 0, 1)) // special -> S1E1, never into specials
	if err != nil || next.Key != core.EpisodeKey(100, 1, 1) {
		t.Fatalf("next after special: %+v %v", next, err)
	}
	if _, err := c.NextEpisode(ctx, core.EpisodeKey(100, 2, 1)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("last episode: want ErrNotFound got %v", err)
	}
	if _, err := c.NextEpisode(ctx, core.MovieKey(550)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("movie key: want ErrNotFound got %v", err)
	}
}

func TestSyncBuildsBrowsableSet(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := c.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Movies: Fight Club (pop+votes), Family Fun (pop), Old Classic (votes). Not Obscure Nothing.
	if stats.BrowsableMovies != 3 || stats.BrowsableShows != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	if stats.LastSync.IsZero() {
		t.Fatal("last sync not recorded")
	}
	page, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Query: "Obscure"}})
	if err != nil || page.Total != 0 {
		t.Fatalf("obscure should not be browsable: %+v", page)
	}
}

func TestBrowseFiltersSortPaging(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// Enrich the two mains so runtime/rating-cert/studio facets exist.
	if _, err := c.Get(ctx, core.MovieKey(550)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, core.MovieKey(551)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		q    core.BrowseQuery
		want []string
	}{
		{"genre", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Genres: []string{"Family"}}}, []string{"Family Fun"}},
		{"year", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, YearFrom: 1990, YearTo: 1999}}, []string{"Fight Club"}},
		{"minRating", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, MinRating: 8.5}, Sort: core.SortRating, Desc: true}, []string{"Old Classic"}},
		{"maxRuntime", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, MaxRuntime: 2 * time.Hour}}, []string{"Family Fun"}},
		{"person", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Person: 287}}, []string{"Fight Club"}},
		{"studio", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Studio: "Fox 2000"}}, []string{"Fight Club"}},
		{"country", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Countries: []string{"United States of America"}}}, []string{"Fight Club"}},
		{"contentRatings", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, ContentRatings: []string{"PG"}}}, []string{"Family Fun"}},
		{"maxContentRating PG excludes R", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, MaxContentRating: "PG"}}, []string{"Family Fun", "Old Classic"}}, // Old Classic unrated -> allowed
		{"collection", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Collection: 10}}, []string{"Fight Club"}},
		{"query", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Query: "fight"}}, []string{"Fight Club"}},
		{"language", core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Languages: []string{"fr"}}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := c.Browse(ctx, tc.q)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range page.Items {
				got = append(got, it.Title)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for _, w := range tc.want {
				found := false
				for _, g := range got {
					if g == w {
						found = true
					}
				}
				if !found {
					t.Fatalf("got %v want containing %q", got, w)
				}
			}
			if page.Total != len(tc.want) {
				t.Fatalf("total %d want %d", page.Total, len(tc.want))
			}
		})
	}
	// Sort by title ascending + paging.
	page, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}, Sort: core.SortTitle, Limit: 2, Offset: 0})
	if err != nil || page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("page1: %+v %v", page, err)
	}
	if page.Items[0].Title != "Family Fun" || page.Items[1].Title != "Fight Club" {
		t.Fatalf("title sort: %v", []string{page.Items[0].Title, page.Items[1].Title})
	}
	page2, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}, Sort: core.SortTitle, Limit: 2, Offset: 2})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].Title != "Old Classic" {
		t.Fatalf("page2: %+v %v", page2, err)
	}
	// Popularity desc.
	pop, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}, Sort: core.SortPopularity, Desc: true})
	if err != nil || pop.Items[0].Title != "Fight Club" {
		t.Fatalf("pop sort: %+v %v", pop, err)
	}
}

func TestFacets(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, core.MovieKey(550)); err != nil {
		t.Fatal(err)
	}
	genres, err := c.Facet(ctx, core.TypeMovie, "genre")
	if err != nil || len(genres) == 0 {
		t.Fatalf("genre facet: %v %v", genres, err)
	}
	foundDrama := false
	for _, g := range genres {
		if g.Key == "Drama" && g.Count >= 2 {
			foundDrama = true
		}
	}
	if !foundDrama {
		t.Fatalf("drama facet: %v", genres)
	}
	decades, err := c.Facet(ctx, core.TypeMovie, "decade")
	if err != nil || len(decades) == 0 || decades[0].Title == decades[0].Key {
		t.Fatalf("decade facet: %v %v", decades, err) // Title should be "2020s" style
	}
	years, err := c.Facet(ctx, core.TypeMovie, "year")
	if err != nil || len(years) != 3 {
		t.Fatalf("year facet: %v %v", years, err)
	}
	ratings, err := c.Facet(ctx, core.TypeMovie, "contentRating")
	if err != nil || len(ratings) != 1 || ratings[0].Key != "R" {
		t.Fatalf("rating facet: %v %v", ratings, err)
	}
	if _, err := c.Facet(ctx, core.TypeMovie, "bogus"); err == nil {
		t.Fatal("bogus facet should error")
	}
}

func TestSearchJoinsBrowsable(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	res, err := c.Search(ctx, "Hidden", nil, 10)
	if err != nil || len(res) != 1 || res[0].Title != "Hidden Gem" {
		t.Fatalf("search: %v %v", res, err)
	}
	// Hidden Gem is below thresholds but must now be browsable (added_at set -> SortAdded works).
	page, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Query: "Hidden"}})
	if err != nil || page.Total != 1 {
		t.Fatalf("searched title not browsable: %+v %v", page, err)
	}
}

func TestListSimilarCollectionByExternal(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, fakeTMDB(t))
	items, err := c.List(ctx, "trending_day", core.TypeMovie, 10)
	if err != nil || len(items) != 1 || items[0].Title != "Fight Club" {
		t.Fatalf("list: %v %v", items, err)
	}
	all, err := c.List(ctx, "trending_week", "", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v %v", all, err)
	}
	sim, err := c.Similar(ctx, core.MovieKey(550), 10)
	if err != nil || len(sim) != 2 { // Family Fun (both, deduped) + Saga Two
		t.Fatalf("similar: %v %v", sim, err)
	}
	if sim[0].Title != "Family Fun" {
		t.Fatalf("recommendations first: %v", sim)
	}
	name, parts, err := c.Collection(ctx, 10)
	if err != nil || name != "Saga Collection" || len(parts) != 2 || parts[0].Title != "Fight Club" {
		t.Fatalf("collection: %q %v %v", name, parts, err) // sorted by release
	}
	byIMDB, err := c.ByExternalID(ctx, "imdb", "tt0137523")
	if err != nil || byIMDB.Key != core.MovieKey(550) {
		t.Fatalf("by imdb: %+v %v", byIMDB, err)
	}
	byTVDB, err := c.ByExternalID(ctx, "tvdb", "777")
	if err != nil || byTVDB.Key != core.ShowKey(100) {
		t.Fatalf("by tvdb: %+v %v", byTVDB, err)
	}
	if _, err := c.ByExternalID(ctx, "imdb", "tt0000000"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing ext: %v", err)
	}
}

func TestRatingRankOrdering(t *testing.T) {
	cases := []struct {
		rating, max string
		exceeds     bool
	}{
		{"G", "PG", false}, {"PG-13", "PG", true}, {"R", "PG-13", true}, {"NC-17", "R", true},
		{"TV-MA", "TV-14", true}, {"TV-PG", "PG-13", false}, {"TV-14", "PG", true},
		{"R", "TV-MA", false}, {"TV-MA", "R", false}, // R ~ TV-MA same rank
		{"18", "R", true}, {"12", "PG", true}, {"U", "G", false},
		{"", "G", false}, {"R", "", false}, {"NR", "G", false},
	}
	for _, tc := range cases {
		if got := catalog.RatingExceeds(tc.rating, tc.max); got != tc.exceeds {
			t.Errorf("RatingExceeds(%q,%q)=%v want %v (ranks %d vs %d)", tc.rating, tc.max, got, tc.exceeds, catalog.RatingRank(tc.rating), catalog.RatingRank(tc.max))
		}
	}
	if !(catalog.RatingRank("G") < catalog.RatingRank("PG") && catalog.RatingRank("PG") < catalog.RatingRank("PG-13") && catalog.RatingRank("PG-13") < catalog.RatingRank("R") && catalog.RatingRank("R") < catalog.RatingRank("NC-17")) {
		t.Fatal("MPAA ordering broken")
	}
	if !(catalog.RatingRank("TV-Y") <= catalog.RatingRank("TV-G") && catalog.RatingRank("TV-PG") < catalog.RatingRank("TV-14") && catalog.RatingRank("TV-14") < catalog.RatingRank("TV-MA")) {
		t.Fatal("TV ordering broken")
	}
}

func TestUnreleasedVisibility(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = now
	seed := func(c *catalog.Catalog) {
		future := core.Title{Key: core.MovieKey(9001), Type: core.TypeMovie, IDs: core.IDs{TMDB: 9001},
			Title: "Future Film", Released: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Popularity: 50, VoteCount: 10}
		if err := c.UpsertTitle(ctx, future, true); err != nil {
			t.Fatal(err)
		}
	}
	shown := newTestCatalog(t, fakeTMDB(t)) // ShowUnreleased true
	seed(shown)
	page, err := shown.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}})
	if err != nil || page.Total != 1 {
		t.Fatalf("unreleased shown: %+v %v", page, err)
	}
	hidden := newTestCatalogCfg(t, fakeTMDB(t), config.Catalog{Language: "en-US", Region: "US", MinPopularity: 5, MinVotes: 100, ShowUnreleased: false})
	seed(hidden)
	page, err = hidden.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}})
	if err != nil || page.Total != 0 {
		t.Fatalf("unreleased hidden: %+v %v", page, err)
	}
	page, err = hidden.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, IncludeUnreleased: true}})
	if err != nil || page.Total != 1 {
		t.Fatalf("includeUnreleased override: %+v %v", page, err)
	}
	// Threshold logic agrees.
	future := core.Title{Type: core.TypeMovie, Released: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Popularity: 50}
	if catalog.MeetsThreshold(config.Catalog{MinPopularity: 5, ShowUnreleased: false}, future, time.Now()) {
		t.Fatal("future title must not meet threshold when unreleased hidden")
	}
	if !catalog.MeetsThreshold(config.Catalog{MinPopularity: 5, ShowUnreleased: true}, future, time.Now()) {
		t.Fatal("future title should meet pop threshold when unreleased shown")
	}
}

func TestUpsertPreservesBrowsableAndDetails(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, nil)
	detail := core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, IDs: core.IDs{TMDB: 1, IMDB: "tt1"},
		Title: "Detailed", Tagline: "tag", Genres: []string{"Drama"}, ContentRating: "R", Popularity: 10,
		Released: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := c.UpsertTitle(ctx, detail, true); err != nil {
		t.Fatal(err)
	}
	summary := core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, IDs: core.IDs{TMDB: 1}, Title: "Detailed", Popularity: 11}
	if err := c.UpsertTitle(ctx, summary, false); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, core.MovieKey(1))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tagline != "tag" || got.IDs.IMDB != "tt1" || len(got.Genres) != 1 || got.ContentRating != "R" {
		t.Fatalf("summary upsert wiped details: %+v", got)
	}
	page, err := c.Browse(ctx, core.BrowseQuery{})
	if err != nil || page.Total != 1 {
		t.Fatalf("browsable lost: %+v %v", page, err)
	}
}

func TestGetOfflineAndNotFound(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalog(t, nil)
	if _, err := c.Get(ctx, core.MovieKey(424242)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("offline missing: %v", err)
	}
	if _, err := c.Get(ctx, core.CollectionKey(10)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("collection key via Get: %v", err)
	}
	items, err := c.List(ctx, "top_rated", core.TypeMovie, 5)
	if err != nil || len(items) != 0 {
		t.Fatalf("offline list: %v %v", items, err)
	}
}

func TestEstimateMonotonic(t *testing.T) {
	strict := catalog.EstimateCatalogSize(20, 1000)
	defaultEst := catalog.EstimateCatalogSize(5, 100)
	loose := catalog.EstimateCatalogSize(1, 10)
	if !(strict.Total < defaultEst.Total && defaultEst.Total < loose.Total) {
		t.Fatalf("estimates not monotonic: strict=%+v default=%+v loose=%+v", strict, defaultEst, loose)
	}
	if defaultEst.Total < 10000 || defaultEst.Total > 500000 {
		t.Fatalf("default estimate out of plausible range: %+v", defaultEst)
	}
	if got := catalog.EstimateTitles(0, 0); got <= 0 {
		t.Fatalf("zero thresholds estimate %d", got)
	}
}
