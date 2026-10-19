package collections_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestTemplatesAreCompleteAndPreviewNonEmpty(t *testing.T) {
	e := newEnv(t)
	ts := collections.Templates()
	if len(ts) < 40 {
		t.Fatalf("want at least 40 templates, have %d", len(ts))
	}
	ids := map[string]bool{}
	cats := map[string]int{}
	for _, tpl := range ts {
		if ids[tpl.ID] {
			t.Fatalf("duplicate template id %q", tpl.ID)
		}
		ids[tpl.ID] = true
		cats[tpl.Category]++
		if tpl.Name == "" || tpl.Description == "" || tpl.Category == "" {
			t.Errorf("%s: incomplete metadata %+v", tpl.ID, tpl)
		}
		sp := tpl.Spec
		if sp.TemplateID != tpl.ID {
			t.Errorf("%s: spec.templateId = %q", tpl.ID, sp.TemplateID)
		}
		if err := collections.Validate(&sp); err != nil {
			t.Errorf("%s: invalid: %v", tpl.ID, err)
			continue
		}
		p, err := e.svc.Preview(e.ctx, sp, 60)
		if err != nil {
			t.Errorf("%s: preview: %v", tpl.ID, err)
			continue
		}
		if p.Total == 0 || len(p.Items) == 0 {
			t.Errorf("%s: empty preview (warnings %v)", tpl.ID, p.Warnings)
		}
	}
	for _, want := range []string{"Franchises & universes", "Directors", "Actors", "Decades", "Critically acclaimed", "Moods",
		"Streaming services", "Seasonal", "World cinema", "Family", "TV", "New & upcoming"} {
		if cats[want] == 0 {
			t.Errorf("no templates in category %q", want)
		}
	}
}

func TestCatalogRulesAllAnyExclude(t *testing.T) {
	e := newEnv(t)
	// 80s sci-fi.
	p, err := e.svc.Preview(e.ctx, spec("80s sci-fi", "movie", catalogSource(),
		rl("genre", "is", "Science Fiction"), rl("decade", "is", 1980)), 60)
	if err != nil {
		t.Fatal(err)
	}
	got := titlesOf(p)
	if len(got) != 2 || !contains(got, "Time Car") || !contains(got, "Cyborg Hunter") {
		t.Fatalf("80s sci-fi: %v", got)
	}
	// any: Korean or Japanese.
	sp := spec("asia", "movie", catalogSource(), rl("language", "is", "ko"), rl("language", "is", "ja"))
	sp.Match = "any"
	p, err = e.svc.Preview(e.ctx, sp, 60)
	if err != nil {
		t.Fatal(err)
	}
	got = titlesOf(p)
	for _, want := range []string{"Seoul Story", "Spirit Valley", "Samurai Seven"} {
		if !contains(got, want) {
			t.Fatalf("any: missing %s in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("any: %v", got)
	}
	// exclude animation.
	sp.Exclude = []core.CollectionRule{rl("genre", "is", "Animation")}
	p, _ = e.svc.Preview(e.ctx, sp, 60)
	if contains(titlesOf(p), "Spirit Valley") || p.Total != 2 {
		t.Fatalf("exclude: %v", titlesOf(p))
	}
	// is with a list means all of; in means any of; notIn none of.
	p, _ = e.svc.Preview(e.ctx, spec("all of", "movie", catalogSource(), rl("genre", "is", []string{"Animation", "Family"})), 60)
	for _, it := range p.Items {
		if !contains(it.Genres, "Animation") || !contains(it.Genres, "Family") {
			t.Fatalf("is-list must require all genres: %+v", it)
		}
	}
	p, _ = e.svc.Preview(e.ctx, spec("none", "movie", catalogSource(), rl("genre", "notIn", []string{"Drama", "Action", "Comedy"})), 60)
	for _, it := range p.Items {
		for _, g := range it.Genres {
			if g == "Drama" || g == "Action" || g == "Comedy" {
				t.Fatalf("notIn leaked %+v", it)
			}
		}
	}
}

func TestPushdownFindsDetailOnlyMatchesAndJoinsCatalog(t *testing.T) {
	e := newEnv(t)
	p, err := e.svc.Preview(e.ctx, spec("short", "movie", catalogSource(), rl("runtime", "lte", 90), rl("votes", "gte", 500)), 60)
	if err != nil {
		t.Fatal(err)
	}
	got := titlesOf(p)
	// Laugh Riot is in the catalogue with a runtime; Short and Sweet is only
	// on TMDB and must be found by pushdown and joined.
	if !contains(got, "Laugh Riot") || !contains(got, "Short and Sweet") {
		t.Fatalf("runtime pushdown: %v (warnings %v)", got, p.Warnings)
	}
	if contains(got, "Time Car") {
		t.Fatalf("116-minute film leaked into under-90: %v", got)
	}
	reqs := e.tmdb.seen("/discover/movie")
	found := false
	for _, r := range reqs {
		if strings.Contains(r, "with_runtime.lte=90") && strings.Contains(r, "vote_count.gte=500") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pushdown discover request: %v", reqs)
	}
	// The joined title is now part of the browsable catalogue.
	page, err := e.cat.Browse(e.ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, Query: "Short and Sweet"}})
	if err != nil || page.Total != 1 {
		t.Fatalf("joined title not browsable: %+v %v", page, err)
	}
}

func TestSourceRulesArePushedIntoDiscover(t *testing.T) {
	e := newEnv(t)
	p, err := e.svc.Preview(e.ctx, spec("twists", "movie",
		[]core.CollectionSource{{Kind: "tmdbKeyword", Label: "plot twist"}}, rl("votes", "gte", 30000)), 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := titlesOf(p); len(got) != 1 || got[0] != "Dream Heist" {
		t.Fatalf("keyword+votes: %v", got)
	}
	ok := false
	for _, r := range e.tmdb.seen("/discover/movie") {
		if strings.Contains(r, "with_keywords=1001") && strings.Contains(r, "vote_count.gte=30000") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("rules not pushed into the keyword source: %v", e.tmdb.seen("/discover/movie"))
	}
}

func TestDiscoverParamsResolveNames(t *testing.T) {
	e := newEnv(t)
	src := core.CollectionSource{Kind: "tmdbDiscover", Params: map[string]string{"with_keywords": "plot twist|dream", "with_genres": "Action"}}
	p, err := e.svc.Preview(e.ctx, spec("discover", "movie", []core.CollectionSource{src}), 60)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(titlesOf(p), "Dream Heist") {
		t.Fatalf("discover names: %v %v", titlesOf(p), p.Warnings)
	}
	ok := false
	for _, r := range e.tmdb.seen("/discover/movie") {
		if strings.Contains(r, "with_keywords=1001%7C1005") && strings.Contains(r, "with_genres=28") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("names not resolved: %v", e.tmdb.seen("/discover/movie"))
	}
	// An unknown keyword name is a warning, not a crash.
	src.Params = map[string]string{"with_keywords": "no such keyword at all"}
	p, err = e.svc.Preview(e.ctx, spec("discover", "movie", []core.CollectionSource{src}), 60)
	if err != nil || len(p.Warnings) == 0 {
		t.Fatalf("unknown keyword: %v %v", p, err)
	}
}

func TestPersonSourceRoles(t *testing.T) {
	e := newEnv(t)
	directed, err := e.svc.Preview(e.ctx, spec("dir", "movie", []core.CollectionSource{{Kind: "tmdbPerson", Label: "Some Director", Role: "director"}}), 60)
	if err != nil {
		t.Fatal(err)
	}
	got := titlesOf(directed)
	for _, want := range []string{"Dream Heist", "Spirit Valley", "Twisty Tale"} {
		if !contains(got, want) {
			t.Fatalf("director credits missing %s: %v", want, got)
		}
	}
	if contains(got, "Knight Returns") {
		t.Fatalf("cast credit in director source: %v", got)
	}
	cast, _ := e.svc.Preview(e.ctx, spec("cast", "mixed", []core.CollectionSource{{Kind: "tmdbPerson", ID: 31, Role: "cast"}}), 60)
	got = titlesOf(cast)
	if !contains(got, "Knight Returns") || contains(got, "Late Night Talk") {
		t.Fatalf("cast credits (talk-show self appearances must be skipped): %v", got)
	}
}

func TestWatchProviderSourceAndRule(t *testing.T) {
	e := newEnv(t)
	p, err := e.svc.Preview(e.ctx, spec("netflix", "mixed", []core.CollectionSource{{Kind: "watchProvider", Label: "Netflix"}}), 60)
	if err != nil {
		t.Fatal(err)
	}
	got := titlesOf(p)
	for _, want := range []string{"Chemistry Teacher", "Joined From TMDB", "Late Night Talk"} {
		if !contains(got, want) {
			t.Fatalf("provider source missing %s: %v", want, got)
		}
	}
	ok := false
	for _, r := range e.tmdb.seen("/discover/") {
		if strings.Contains(r, "with_watch_providers=8") && strings.Contains(r, "watch_region=US") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("provider query: %v", e.tmdb.seen("/discover/"))
	}
	// As a rule over the catalogue.
	p, err = e.svc.Preview(e.ctx, spec("on max", "show", catalogSource(), rl("watchProvider", "is", map[string]any{"id": 1899, "name": "HBO Max"})), 60)
	if err != nil {
		t.Fatal(err)
	}
	got = titlesOf(p)
	if !contains(got, "Office Life") || !contains(got, "Space Show") || contains(got, "Chemistry Teacher") {
		t.Fatalf("provider rule: %v", got)
	}
}

func TestUnreleasedOnlyWhenAskedFor(t *testing.T) {
	e := newEnv(t)
	p, _ := e.svc.Preview(e.ctx, spec("pop", "movie", []core.CollectionSource{{Kind: "tmdbList", List: "popular"}, {Kind: "catalog"}}), 200)
	if contains(titlesOf(p), "Future Blockbuster") {
		t.Fatalf("unreleased title in a normal collection: %v", titlesOf(p))
	}
	soon := spec("soon", "movie", []core.CollectionSource{{Kind: "tmdbList", List: "upcoming", MediaType: "movie"}}, rl("released", "gte", "now"))
	p, err := e.svc.Preview(e.ctx, soon, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := titlesOf(p); len(got) != 1 || got[0] != "Future Blockbuster" {
		t.Fatalf("coming soon: %v %v", got, p.Warnings)
	}
}

func TestSortsAndLimit(t *testing.T) {
	e := newEnv(t)
	keys := []string{core.MovieKey(111).String(), core.MovieKey(101).String(), core.MovieKey(105).String()}
	picked := spec("picked", "movie", []core.CollectionSource{{Kind: "titles", Keys: keys}})
	order := func(sort string, desc bool, limit int) []string {
		sp := picked
		sp.Sort, sp.SortDesc, sp.Limit = sort, desc, limit
		p, err := e.svc.Preview(e.ctx, sp, 60)
		if err != nil {
			t.Fatal(err)
		}
		return titlesOf(p)
	}
	if got := order("source", false, 0); strings.Join(got, ",") != "Quiet Gem,Space Saga,Prison Hope" {
		t.Fatalf("source order: %v", got)
	}
	if got := order("rating", true, 0); got[0] != "Prison Hope" || got[2] != "Quiet Gem" {
		t.Fatalf("rating desc: %v", got)
	}
	if got := order("released", false, 0); got[0] != "Space Saga" {
		t.Fatalf("released asc: %v", got)
	}
	if got := order("title", false, 0); strings.Join(got, ",") != "Prison Hope,Quiet Gem,Space Saga" {
		t.Fatalf("title asc: %v", got)
	}
	if got := order("popularity", true, 2); len(got) != 2 || got[0] != "Space Saga" {
		t.Fatalf("popularity limit: %v", got)
	}
	r1, r2 := order("random", false, 0), order("random", false, 0)
	if strings.Join(r1, ",") != strings.Join(r2, ",") || len(r1) != 3 {
		t.Fatalf("random must be stable within a day: %v vs %v", r1, r2)
	}
}

func TestCollectionLabelResolvesAndPersists(t *testing.T) {
	e := newEnv(t)
	created, err := e.svc.CreateSpec(e.ctx, spec("saga", "movie", []core.CollectionSource{{Kind: "tmdbCollection", Label: "Space Saga"}}))
	if err != nil {
		t.Fatal(err)
	}
	if created.ItemCount != 2 || created.LastBuiltAt == nil {
		t.Fatalf("built: %+v", created)
	}
	if created.Sources[0].ID != 10 {
		t.Fatalf("label not resolved to an id: %+v", created.Sources[0])
	}
	got, err := e.svc.GetSpec(e.ctx, created.ID)
	if err != nil || got.Sources[0].ID != 10 || got.RatingKey != core.SmartKey(mustInt(t, created.ID)).String() {
		t.Fatalf("persisted: %+v %v", got, err)
	}
}

func TestValidationErrors(t *testing.T) {
	e := newEnv(t)
	cases := map[string]struct {
		sp    core.CollectionSpec
		field string
	}{
		"name":     {spec("", "movie", catalogSource()), "name"},
		"media":    {spec("x", "music", catalogSource()), "mediaType"},
		"source":   {spec("x", "movie", []core.CollectionSource{{Kind: "netflixScrape"}}), "sources[0].kind"},
		"list":     {spec("x", "movie", []core.CollectionSource{{Kind: "tmdbList", List: "best"}}), "sources[0].list"},
		"trakt":    {spec("x", "movie", []core.CollectionSource{{Kind: "traktList", URL: "https://example.com/list"}}), "sources[0].url"},
		"field":    {spec("x", "movie", catalogSource(), rl("vibes", "is", "good")), "rules[0]"},
		"op":       {spec("x", "movie", catalogSource(), rl("genre", "gte", "Drama")), "rules[0]"},
		"between":  {spec("x", "movie", catalogSource(), rl("year", "between", 1990)), "rules[0]"},
		"date":     {spec("x", "movie", catalogSource(), rl("released", "gte", "last tuesday")), "rules[0]"},
		"rating":   {spec("x", "movie", catalogSource(), rl("contentRating", "lte", "Banana")), "rules[0]"},
		"provider": {spec("x", "movie", []core.CollectionSource{{Kind: "watchProvider", ID: 8, Region: "USA"}}), "sources[0].region"},
	}
	for name, c := range cases {
		_, err := e.svc.CreateSpec(e.ctx, c.sp)
		var ve *collections.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: want ValidationError, got %v", name, err)
			continue
		}
		if _, ok := ve.InvalidFields()[c.field]; !ok {
			t.Errorf("%s: want field %q, got %v", name, c.field, ve.Fields)
		}
	}
	sp := spec("x", "movie", catalogSource())
	sp.Artwork = core.CollectionArtwork{Mode: "custom", URL: "javascript:alert(1)"}
	if _, err := e.svc.CreateSpec(e.ctx, sp); err == nil {
		t.Fatal("custom artwork URL must be http(s) or a TMDB path")
	}
	sp.Artwork = core.CollectionArtwork{Mode: "auto", Accent: "red"}
	if _, err := e.svc.CreateSpec(e.ctx, sp); err == nil {
		t.Fatal("accent must be a hex colour")
	}
}

func TestCRUDAndBuiltIns(t *testing.T) {
	e := newEnv(t)
	all, err := e.svc.ListSpecs(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	builtins := 0
	for _, s := range all {
		if s.BuiltIn {
			builtins++
		}
	}
	if builtins < 10 {
		t.Fatalf("built-ins missing: %d", builtins)
	}
	created, err := e.svc.CreateSpec(e.ctx, spec("Mine", "movie", catalogSource(), rl("genre", "is", "Drama")))
	if err != nil {
		t.Fatal(err)
	}
	created.Name = "Mine, renamed"
	created.Rules = append(created.Rules, rl("rating", "gte", 8.6))
	updated, err := e.svc.UpdateSpec(e.ctx, created.ID, *created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Mine, renamed" || updated.ItemCount != 2 { // Family Business 8.7, Prison Hope 8.7
		t.Fatalf("update: %+v", updated)
	}
	// Built-ins: only targets, artwork, rotation and enabled change.
	b, err := e.svc.GetSpec(e.ctx, "top-rated-movie")
	if err != nil {
		t.Fatal(err)
	}
	b.Name = "Hacked"
	b.Enabled = false
	b.Targets.HomeRow = true
	b2, err := e.svc.UpdateSpec(e.ctx, "top-rated-movie", *b)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Name != "Top Rated Movies" || b2.Enabled || !b2.Targets.HomeRow {
		t.Fatalf("built-in update: %+v", b2)
	}
	if err := e.svc.DeleteSpec(e.ctx, "top-rated-movie"); err == nil {
		t.Fatal("built-ins cannot be deleted")
	} else if c, ok := err.(interface{ Conflict() bool }); !ok || !c.Conflict() {
		t.Fatalf("delete built-in: want a conflict, got %v", err)
	}
	rebuilt, err := e.svc.Rebuild(e.ctx, created.ID)
	if err != nil || rebuilt.LastBuiltAt == nil {
		t.Fatalf("rebuild: %+v %v", rebuilt, err)
	}
	if err := e.svc.DeleteSpec(e.ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetSpec(e.ctx, created.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	// Ids are never reused, so stale Plex rating keys never point at a new collection.
	again, err := e.svc.CreateSpec(e.ctx, spec("Next", "movie", catalogSource()))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == created.ID {
		t.Fatalf("id %s reused", again.ID)
	}
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("id %q is not numeric", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestPreviewSpeedOnWarmIndex(t *testing.T) {
	e := newEnv(t)
	sp := spec("speed", "mixed", catalogSource(), rl("rating", "gte", 7))
	if _, err := e.svc.Preview(e.ctx, sp, 60); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p, err := e.svc.Preview(e.ctx, sp, 60)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("warm preview took %v", d)
	}
	if p.TookMs < 0 || p.Total == 0 {
		t.Fatalf("preview: %+v", p)
	}
}
