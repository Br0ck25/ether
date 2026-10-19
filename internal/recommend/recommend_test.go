package recommend

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
)

// ---- fakes -----------------------------------------------------------------

type fakeCatalog struct {
	mu       sync.Mutex
	filters  []core.Filter // Browse filters seen
	byKey    map[core.RatingKey]core.Title
	similar  map[core.RatingKey][]core.RatingKey
	lists    map[string][]core.RatingKey // "trending_week:movie"
	colls    map[int64][]core.RatingKey
	collName map[int64]string
}

func newFake() *fakeCatalog {
	return &fakeCatalog{byKey: map[core.RatingKey]core.Title{}, similar: map[core.RatingKey][]core.RatingKey{},
		lists: map[string][]core.RatingKey{}, colls: map[int64][]core.RatingKey{}, collName: map[int64]string{}}
}

func (f *fakeCatalog) add(t core.Title) core.Title { f.byKey[t.Key] = t; return t }
func (f *fakeCatalog) resolve(keys []core.RatingKey) []core.Title {
	var out []core.Title
	for _, k := range keys {
		if t, ok := f.byKey[k]; ok {
			out = append(out, t)
		}
	}
	return out
}
func (f *fakeCatalog) Get(_ context.Context, k core.RatingKey) (*core.Title, error) {
	if t, ok := f.byKey[k]; ok {
		return &t, nil
	}
	return nil, core.ErrNotFound
}
func (f *fakeCatalog) Children(context.Context, core.RatingKey) ([]core.Title, error) {
	return nil, nil
}
func (f *fakeCatalog) Grandchildren(context.Context, core.RatingKey) ([]core.Title, error) {
	return nil, nil
}
func (f *fakeCatalog) Browse(_ context.Context, q core.BrowseQuery) (core.Page, error) {
	f.mu.Lock()
	f.filters = append(f.filters, q.Filter)
	f.mu.Unlock()
	var items []core.Title
	for _, t := range f.byKey {
		if q.Filter.Type != "" && t.Type != q.Filter.Type {
			continue
		}
		if q.Filter.Person != 0 {
			found := false
			for _, p := range append(append([]core.Person{}, t.Cast...), t.Directors...) {
				if p.TMDB == q.Filter.Person {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		items = append(items, t)
	}
	switch q.Sort {
	case core.SortReleased:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Released.After(items[j].Released) })
	default:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Popularity > items[j].Popularity })
	}
	if q.Limit > 0 && len(items) > q.Limit {
		items = items[:q.Limit]
	}
	return core.Page{Items: items, Total: len(items)}, nil
}
func (f *fakeCatalog) Search(context.Context, string, []core.MediaType, int) ([]core.Title, error) {
	return nil, nil
}
func (f *fakeCatalog) List(_ context.Context, name string, t core.MediaType, limit int) ([]core.Title, error) {
	items := f.resolve(f.lists[name+":"+string(t)])
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
func (f *fakeCatalog) Similar(_ context.Context, k core.RatingKey, _ int) ([]core.Title, error) {
	return f.resolve(f.similar[k]), nil
}
func (f *fakeCatalog) Collection(_ context.Context, id int64) (string, []core.Title, error) {
	return f.collName[id], f.resolve(f.colls[id]), nil
}
func (f *fakeCatalog) Facet(context.Context, core.MediaType, string) ([]core.FacetValue, error) {
	return nil, nil
}
func (f *fakeCatalog) NextEpisode(_ context.Context, ep core.RatingKey) (*core.Title, error) {
	next := core.EpisodeKey(ep.TMDB(), ep.Season(), ep.Episode()+1)
	if t, ok := f.byKey[next]; ok {
		return &t, nil
	}
	return nil, core.ErrNotFound
}
func (f *fakeCatalog) ByExternalID(context.Context, string, string) (*core.Title, error) {
	return nil, core.ErrNotFound
}

type fakeCollections struct {
	items map[core.RatingKey][]core.Title
	list  []core.Collection
}

func (f fakeCollections) List(_ context.Context, _ *core.User, t core.MediaType) ([]core.Collection, error) {
	var out []core.Collection
	for _, c := range f.list {
		if c.Type == t {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f fakeCollections) Get(context.Context, *core.User, core.RatingKey) (*core.Collection, error) {
	return nil, core.ErrNotFound
}
func (f fakeCollections) Items(_ context.Context, _ *core.User, k core.RatingKey, _, _ int) (core.Page, error) {
	it := f.items[k]
	return core.Page{Items: it, Total: len(it)}, nil
}
func (f fakeCollections) CreateSmart(context.Context, core.Collection) (*core.Collection, error) {
	return nil, nil
}
func (f fakeCollections) DeleteSmart(context.Context, core.RatingKey) error { return nil }

// ---- fixture ---------------------------------------------------------------

var (
	arrival  = core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, Title: "Arrival", Year: 2016, Genres: []string{"Sci-Fi", "Drama"}, Directors: []core.Person{{TMDB: 100, Name: "Villeneuve"}}, Language: "en", Rating: 7.6, Popularity: 50, Released: time.Date(2016, 11, 1, 0, 0, 0, 0, time.UTC)}
	dune     = core.Title{Key: core.MovieKey(2), Type: core.TypeMovie, Title: "Dune", Year: 2021, Genres: []string{"Sci-Fi", "Adventure"}, Directors: []core.Person{{TMDB: 100, Name: "Villeneuve"}}, Language: "en", Rating: 8.0, Popularity: 90, CollectionID: 55, CollectionName: "Dune Collection", Released: time.Date(2021, 10, 1, 0, 0, 0, 0, time.UTC)}
	dune2    = core.Title{Key: core.MovieKey(3), Type: core.TypeMovie, Title: "Dune: Part Two", Year: 2024, Genres: []string{"Sci-Fi", "Adventure"}, Directors: []core.Person{{TMDB: 100, Name: "Villeneuve"}}, Language: "en", Rating: 8.3, Popularity: 95, CollectionID: 55, CollectionName: "Dune Collection", Released: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)}
	comedy   = core.Title{Key: core.MovieKey(4), Type: core.TypeMovie, Title: "Silly Comedy", Year: 2023, Genres: []string{"Comedy"}, Language: "en", Rating: 6.0, Popularity: 80, Released: time.Date(2023, 5, 1, 0, 0, 0, 0, time.UTC)}
	goryR    = core.Title{Key: core.MovieKey(5), Type: core.TypeMovie, Title: "Gory Sci-Fi", Year: 2022, Genres: []string{"Sci-Fi", "Horror"}, Directors: []core.Person{{TMDB: 100, Name: "Villeneuve"}}, Language: "en", Rating: 8.5, Popularity: 70, ContentRating: "R"}
	sever    = core.Title{Key: core.ShowKey(9), Type: core.TypeShow, Title: "Severance", Year: 2022, Genres: []string{"Sci-Fi", "Thriller"}, Language: "en", Rating: 8.4, Popularity: 85}
	ep1      = core.Title{Key: core.EpisodeKey(9, 1, 1), Type: core.TypeEpisode, Title: "Ep1", ShowTitle: "Severance", Season: 1, Episode: 1}
	ep2      = core.Title{Key: core.EpisodeKey(9, 1, 2), Type: core.TypeEpisode, Title: "Ep2", ShowTitle: "Severance", Season: 1, Episode: 2}
	ep3      = core.Title{Key: core.EpisodeKey(9, 1, 3), Type: core.TypeEpisode, Title: "Ep3", ShowTitle: "Severance", Season: 1, Episode: 3}
	inProg   = core.Title{Key: core.MovieKey(6), Type: core.TypeMovie, Title: "Half Watched", Year: 2020, Genres: []string{"Drama"}, Popularity: 10}
	newMovie = core.Title{Key: core.MovieKey(7), Type: core.TypeMovie, Title: "Brand New", Year: 2026, Genres: []string{"Drama"}, Popularity: 5, Released: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	sicario  = core.Title{Key: core.MovieKey(8), Type: core.TypeMovie, Title: "Sicario", Year: 2015, Genres: []string{"Thriller", "Drama"}, Directors: []core.Person{{TMDB: 100, Name: "Villeneuve"}}, Language: "en", Rating: 7.4, Popularity: 40, Released: time.Date(2015, 9, 1, 0, 0, 0, 0, time.UTC)}
)

func fixture(t *testing.T) (*Recommender, *state.State, *core.User) {
	t.Helper()
	r, st, user, _ := fixtureWith(t, nil)
	return r, st, user
}

// fixtureWith builds the standard scenario; tweak adjusts Options before
// the Recommender is constructed.
func fixtureWith(t *testing.T, tweak func(*Options)) (*Recommender, *state.State, *core.User, *fakeCatalog) {
	t.Helper()
	ctx := context.Background()
	cat := newFake()
	for _, title := range []core.Title{arrival, dune, dune2, comedy, goryR, sever, ep1, ep2, ep3, inProg, newMovie, sicario} {
		cat.add(title)
	}
	cat.similar[arrival.Key] = []core.RatingKey{dune.Key, dune2.Key, goryR.Key, comedy.Key, sicario.Key}
	cat.similar[dune.Key] = []core.RatingKey{dune2.Key, arrival.Key}
	cat.lists["trending_week:movie"] = []core.RatingKey{comedy.Key, dune2.Key}
	cat.lists["trending_week:show"] = []core.RatingKey{sever.Key}
	cat.lists["top_rated:movie"] = []core.RatingKey{dune2.Key}
	cat.lists["popular:movie"] = []core.RatingKey{comedy.Key}
	cat.colls[55] = []core.RatingKey{dune.Key, dune2.Key}
	cat.collName[55] = "Dune Collection"

	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := state.New(db, state.Options{})
	// Watched Arrival (rated 9) and Dune; finished Severance E1; half-way through inProg.
	_ = st.Scrobble(ctx, 1, arrival.Key)
	_ = st.Rate(ctx, 1, arrival.Key, 9)
	_ = st.Scrobble(ctx, 1, dune.Key)
	_ = st.Scrobble(ctx, 1, ep1.Key)
	_ = st.Progress(ctx, 1, inProg.Key, 50*time.Minute, 100*time.Minute, "paused")

	colls := fakeCollections{
		list:  []core.Collection{{Key: core.SmartKey(1), Title: "80s Sci-Fi Gems", Kind: "smart", Type: core.TypeMovie}},
		items: map[core.RatingKey][]core.Title{core.SmartKey(1): {newMovie}},
	}
	fixedNow := time.Now()
	opts := Options{Catalog: cat, State: st, Collections: colls, Events: st, Now: func() time.Time { return fixedNow }}
	if tweak != nil {
		tweak(&opts)
	}
	r := New(opts)
	t.Cleanup(r.Close)
	user := &core.User{ID: 1, MaxContentRating: "PG-13"}
	return r, st, user, cat
}

func hubIDs(hubs []core.Hub) []string {
	out := make([]string, len(hubs))
	for i, h := range hubs {
		out[i] = h.ID
	}
	return out
}

func findHub(hubs []core.Hub, idPrefix string) *core.Hub {
	for i := range hubs {
		if hubs[i].ID == idPrefix || strings.HasPrefix(hubs[i].ID, idPrefix) {
			return &hubs[i]
		}
	}
	return nil
}

// ---- tests -----------------------------------------------------------------

func TestTopPicksRankingExclusionsDeterminism(t *testing.T) {
	ctx := context.Background()
	r, _, user := fixture(t)

	picks, err := r.TopPicks(ctx, user, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(picks) == 0 {
		t.Fatal("no picks")
	}
	for _, p := range picks {
		switch p.Title.Key {
		case arrival.Key, dune.Key, inProg.Key:
			t.Fatalf("watched/in-progress title recommended: %s", p.Title.Title)
		case goryR.Key:
			t.Fatal("R-rated title passed PG-13 limit")
		}
	}
	// Dune: Part Two (Sci-Fi + Villeneuve + franchise) must outrank the comedy.
	pos := map[string]int{}
	for i, p := range picks {
		pos[p.Title.Title] = i
	}
	if pos["Dune: Part Two"] > pos["Silly Comedy"] {
		t.Fatalf("expected Dune 2 before comedy: %+v", picks)
	}
	var topWhy string
	for _, p := range picks {
		if p.Title.Key == dune2.Key {
			topWhy = p.Why
		}
	}
	if !strings.Contains(topWhy, "Villeneuve") && !strings.Contains(topWhy, "Sci-Fi") {
		t.Fatalf("Why not explainable: %q", topWhy)
	}

	// Deterministic across a cold rebuild.
	r.Invalidate(1)
	again, err := r.TopPicks(ctx, user, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(picks) {
		t.Fatalf("nondeterministic count %d vs %d", len(again), len(picks))
	}
	for i := range picks {
		if again[i].Title.Key != picks[i].Title.Key || again[i].Score != picks[i].Score {
			t.Fatalf("nondeterministic at %d: %+v vs %+v", i, again[i], picks[i])
		}
	}
}

func TestEventInvalidation(t *testing.T) {
	ctx := context.Background()
	r, st, user := fixture(t)
	if _, err := r.TopPicks(ctx, user, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.Scrobble(ctx, 1, comedy.Key); err != nil {
		t.Fatal(err)
	}
	picks, _ := r.TopPicks(ctx, user, 10)
	for _, p := range picks {
		if p.Title.Key == comedy.Key {
			t.Fatal("cache not invalidated by scrobble event")
		}
	}
}

func TestHomeHubsOrderAndDedupe(t *testing.T) {
	ctx := context.Background()
	r, _, user := fixture(t)
	hubs, err := r.HomeHubs(ctx, user, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := hubIDs(hubs)
	if len(ids) < 4 || ids[0] != "home.continue" || ids[1] != "home.ondeck" || ids[2] != "home.trending.week" {
		t.Fatalf("hub order: %v", ids)
	}
	if h := findHub(hubs, "home.continue"); h == nil || len(h.Items) != 1 || h.Items[0].Key != inProg.Key {
		t.Fatalf("continue hub: %+v", h)
	}
	if h := findHub(hubs, "home.ondeck"); h == nil || len(h.Items) != 1 || h.Items[0].Key != ep2.Key {
		t.Fatalf("ondeck should be next episode E2: %+v", h)
	}
	if findHub(hubs, "because.") == nil {
		t.Fatalf("missing Because hub: %v", ids)
	}
	if findHub(hubs, "home.toppicks") == nil {
		t.Fatalf("missing Top Picks: %v", ids)
	}
	if findHub(hubs, "home.newreleases") == nil {
		t.Fatalf("missing New Releases: %v", ids)
	}
	if findHub(hubs, "franchise.55") == nil {
		t.Fatalf("missing partially-watched franchise (Dune watched, Dune 2 not): %v", ids)
	}
	if findHub(hubs, "collection.") == nil {
		t.Fatalf("missing smart collection hub: %v", ids)
	}
	// Cross-hub dedupe: no title twice outside curated franchise/collection rows.
	counts := map[core.RatingKey]int{}
	for _, h := range hubs {
		if strings.HasPrefix(h.ID, "franchise.") || strings.HasPrefix(h.ID, "collection.") {
			continue
		}
		for _, it := range h.Items {
			counts[it.Key]++
			if it.Key == goryR.Key {
				t.Fatalf("R-rated title leaked into hub %s", h.ID)
			}
		}
	}
	for k, n := range counts {
		if n > 1 {
			t.Fatalf("title %v appears %d times across home hubs", k, n)
		}
	}
	// Every hub has context + items.
	for _, h := range hubs {
		if h.Context == "" || len(h.Items) == 0 || h.Title == "" {
			t.Fatalf("incomplete hub: %+v", h)
		}
	}
}

func TestSectionAndRelatedHubs(t *testing.T) {
	ctx := context.Background()
	r, _, user := fixture(t)

	hubs, err := r.SectionHubs(ctx, user, core.TypeMovie, 2)
	if err != nil {
		t.Fatal(err)
	}
	if findHub(hubs, "movie.inprogress") == nil || findHub(hubs, "movie.recentlyreleased") == nil || findHub(hubs, "movie.trending") == nil {
		t.Fatalf("movie section hubs: %v", hubIDs(hubs))
	}
	tvHubs, err := r.SectionHubs(ctx, user, core.TypeShow, 10)
	if err != nil {
		t.Fatal(err)
	}
	if findHub(tvHubs, "tv.ondeck") == nil {
		t.Fatalf("tv section hubs: %v", hubIDs(tvHubs))
	}

	rel, err := r.Related(ctx, user, dune.Key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if findHub(rel, "movie.similar") == nil {
		t.Fatalf("related: %v", hubIDs(rel))
	}
	fr := findHub(rel, "movie.collection")
	if fr == nil || len(fr.Items) != 1 || fr.Items[0].Key != dune2.Key {
		t.Fatalf("franchise related (self excluded): %+v", fr)
	}
	if findHub(rel, "movie.by.actor.or.director.100") == nil {
		t.Fatalf("director related missing: %v", hubIDs(rel))
	}
	for _, h := range rel {
		for _, it := range h.Items {
			if it.Key == dune.Key {
				t.Fatal("related hub contains the title itself")
			}
		}
	}
}

func TestDiversifyMMR(t *testing.T) {
	mk := func(id int64, score float64, coll int64, genres ...string) ScoredTitle {
		return ScoredTitle{Title: core.Title{Key: core.MovieKey(id), Type: core.TypeMovie, Genres: genres, CollectionID: coll}, Score: score}
	}
	// Four same-franchise sci-fi sequels outscore one drama; with diversity
	// the drama must break into the top 2.
	in := []ScoredTitle{
		mk(1, 10, 9, "Sci-Fi"), mk(2, 9.9, 9, "Sci-Fi"), mk(3, 9.8, 9, "Sci-Fi"), mk(4, 9.7, 9, "Sci-Fi"),
		mk(5, 8, 0, "Drama"),
	}
	out := Diversify(in, 2, 0.5)
	if len(out) != 2 || out[0].Title.Key != core.MovieKey(1) || out[1].Title.Key != core.MovieKey(5) {
		t.Fatalf("MMR did not diversify: %+v", out)
	}
	// Pure-score mode keeps ranking.
	out = Diversify(in, 2, 1.0)
	if out[1].Title.Key != core.MovieKey(2) {
		t.Fatalf("lambda=1 should be pure score order: %+v", out)
	}
}

func TestContentRatingAllowed(t *testing.T) {
	cases := []struct {
		rating, max string
		want        bool
	}{
		{"R", "PG-13", false}, {"PG", "PG-13", true}, {"TV-MA", "TV-14", false},
		{"TV-14", "PG-13", true}, {"", "PG-13", true}, {"R", "", true}, {"NR", "G", true}, {"G", "G", true}, {"NC-17", "R", false},
	}
	for _, tc := range cases {
		if got := ContentRatingAllowed(tc.rating, tc.max); got != tc.want {
			t.Errorf("ContentRatingAllowed(%q,%q)=%v want %v", tc.rating, tc.max, got, tc.want)
		}
	}
}
