package personalize

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
)

// testNow is a Wednesday evening.
var testNow = time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *clock { return &clock{t: t} }
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

var testGenres = []string{"Science Fiction", "Drama", "Comedy", "Horror", "Animation", "Family", "Thriller", "Romance", "Documentary", "Crime"}

// fakeCatalog is a synthetic, deterministic catalogue.
type fakeCatalog struct {
	mu     sync.Mutex
	titles map[core.RatingKey]core.Title
	order  []core.RatingKey
	colls  map[int64][]core.RatingKey
	lists  map[string][]core.RatingKey
	calls  map[string]int
}

// movieKey / showKey address the synthetic titles: genre index gi, item j.
func movieKey(gi, j int) core.RatingKey { return core.MovieKey(int64(1000*(gi+1) + j + 1)) }
func showKey(gi, j int) core.RatingKey  { return core.ShowKey(int64(1000*(gi+1) + j + 1)) }

const (
	sagaCollection = 9001
	sciFiDirector  = 500 // directs Science Fiction movies 0-3
)

func newFakeCatalog(now time.Time) *fakeCatalog {
	c := &fakeCatalog{titles: map[core.RatingKey]core.Title{}, colls: map[int64][]core.RatingKey{}, lists: map[string][]core.RatingKey{}, calls: map[string]int{}}
	for gi, g := range testGenres {
		for j := 0; j < 48; j++ {
			rating := "PG-13"
			switch {
			case g == "Horror" || g == "Crime" || g == "Thriller":
				rating = "R"
			case g == "Family" || g == "Animation":
				rating = "G"
			case j%2 == 0:
				rating = "PG"
			}
			year, released := 1975+(j%24)*2, time.Date(1975+(j%24)*2, 6, 1, 0, 0, 0, 0, time.UTC)
			if j == 0 { // brand new
				released = now.AddDate(0, 0, -2)
				year = released.Year()
			}
			kws := []string{genreSlug(g) + "-theme", fmt.Sprintf("kw-%d-%d", gi, j%3)}
			if j%7 == 0 {
				kws = append(kws, "shared-motif")
			}
			t := core.Title{
				Key: movieKey(gi, j), Type: core.TypeMovie, IDs: core.IDs{TMDB: int64(1000*(gi+1) + j + 1)},
				Title: fmt.Sprintf("%s Movie %d", g, j), Year: year, Genres: []string{g}, Keywords: kws,
				Runtime: time.Duration(80+(j%14)*6) * time.Minute, Language: "en", ContentRating: rating,
				Rating: 6.0 + float64(j%6)*0.5, VoteCount: 80 + j*400, Popularity: 4 + float64((j*37)%90),
				Released: released,
				Cast: []core.Person{
					{TMDB: int64(600 + gi*10 + j%3), Name: fmt.Sprintf("%s Actor %d", g, j%3), ThumbPath: fmt.Sprintf("/p%d.jpg", 600+gi*10+j%3)},
					{TMDB: int64(5000 + gi*10 + j%4), Name: fmt.Sprintf("%s Support %d", g, j%4)},
				},
			}
			if j%5 == 0 {
				t.Genres = append(t.Genres, testGenres[(gi+1)%len(testGenres)])
			}
			if g == "Science Fiction" && (j < 4 || (j >= 14 && j < 18) || (j >= 24 && j < 32)) {
				t.Directors = []core.Person{{TMDB: sciFiDirector, Name: "Vera Villeneuve", ThumbPath: "/vera.jpg"}}
			} else {
				t.Directors = []core.Person{{TMDB: int64(10000 + gi*100 + j), Name: fmt.Sprintf("Director %d-%d", gi, j)}}
			}
			if g == "Science Fiction" && j >= 4 && j < 8 {
				t.CollectionID, t.CollectionName = sagaCollection, "Star Saga Collection"
				c.colls[sagaCollection] = append(c.colls[sagaCollection], t.Key)
			}
			if j == 0 { // a talked-about new release
				t.Popularity, t.VoteCount = 60+float64(gi), 150
			}
			c.add(t)
		}
		rating := "TV-14"
		switch g {
		case "Horror", "Crime", "Thriller":
			rating = "TV-MA"
		case "Family", "Animation":
			rating = "TV-G"
		case "Comedy", "Documentary":
			rating = "TV-PG"
		}
		for j := 0; j < 5; j++ {
			rt := 50
			if g == "Comedy" || g == "Animation" {
				rt = 24
			}
			s := core.Title{
				Key: showKey(gi, j), Type: core.TypeShow, IDs: core.IDs{TMDB: int64(1000*(gi+1) + j + 1)},
				Title: fmt.Sprintf("%s Show %d", g, j), Year: 2010 + j, Genres: []string{g}, Keywords: []string{genreSlug(g) + "-theme"},
				Runtime: time.Duration(rt) * time.Minute, Language: "en", ContentRating: rating,
				Rating: 7.0 + float64(j)*0.3, VoteCount: 300 + j*100, Popularity: 10 + float64(j*15), Released: now.AddDate(-1, 0, -j*30),
				ChildCount: 1, LeafCount: 8,
			}
			c.add(s)
			for e := 1; e <= 8; e++ {
				ep := core.Title{Key: core.EpisodeKey(s.IDs.TMDB, 1, e), Type: core.TypeEpisode, ShowKey: s.Key, ShowTitle: s.Title,
					Season: 1, Episode: e, Title: fmt.Sprintf("%s E%d", s.Title, e), Runtime: s.Runtime, Genres: s.Genres,
					ContentRating: s.ContentRating, Released: s.Released.AddDate(0, 0, 7*e)}
				c.titles[ep.Key] = ep
			}
		}
	}
	// Lists: trending = the most popular of each genre, interleaved.
	for _, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		var all []core.Title
		for _, k := range c.order {
			if t := c.titles[k]; t.Type == typ {
				all = append(all, t)
			}
		}
		byPop := append([]core.Title(nil), all...)
		sort.SliceStable(byPop, func(i, j int) bool { return byPop[i].Popularity > byPop[j].Popularity })
		byRating := append([]core.Title(nil), all...)
		sort.SliceStable(byRating, func(i, j int) bool { return byRating[i].Rating > byRating[j].Rating })
		for i, t := range byPop {
			if i < 30 {
				c.lists["trending_week:"+string(typ)] = append(c.lists["trending_week:"+string(typ)], t.Key)
				c.lists["popular:"+string(typ)] = append(c.lists["popular:"+string(typ)], t.Key)
			}
			if i < 10 {
				c.lists["trending_day:"+string(typ)] = append(c.lists["trending_day:"+string(typ)], t.Key)
			}
		}
		for i, t := range byRating {
			if i < 30 {
				c.lists["top_rated:"+string(typ)] = append(c.lists["top_rated:"+string(typ)], t.Key)
			}
		}
	}
	for j := 0; j < 5; j++ {
		c.lists["airing_today:show"] = append(c.lists["airing_today:show"], showKey(j, j%5))
	}
	return c
}

func (c *fakeCatalog) add(t core.Title) {
	c.titles[t.Key] = t
	c.order = append(c.order, t.Key)
}

func (c *fakeCatalog) count(name string) {
	c.mu.Lock()
	c.calls[name]++
	c.mu.Unlock()
}

func (c *fakeCatalog) callCount(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *fakeCatalog) resolve(keys []core.RatingKey) []core.Title {
	var out []core.Title
	for _, k := range keys {
		if t, ok := c.titles[k]; ok {
			out = append(out, t)
		}
	}
	return out
}

func (c *fakeCatalog) Get(_ context.Context, k core.RatingKey) (*core.Title, error) {
	c.count("get")
	if t, ok := c.titles[k]; ok {
		return &t, nil
	}
	return nil, core.ErrNotFound
}
func (c *fakeCatalog) Children(context.Context, core.RatingKey) ([]core.Title, error) {
	return nil, nil
}
func (c *fakeCatalog) Grandchildren(context.Context, core.RatingKey) ([]core.Title, error) {
	return nil, nil
}

func (c *fakeCatalog) Browse(_ context.Context, q core.BrowseQuery) (core.Page, error) {
	c.count("browse")
	f := q.Filter
	var items []core.Title
	for _, k := range c.order {
		t := c.titles[k]
		if f.Type != "" && t.Type != f.Type {
			continue
		}
		if len(f.Genres) > 0 && !overlap(t.Genres, f.Genres) {
			continue
		}
		if f.MinRating > 0 && t.Rating < f.MinRating {
			continue
		}
		if f.MaxRuntime > 0 && t.Runtime > f.MaxRuntime {
			continue
		}
		if f.MinRuntime > 0 && t.Runtime < f.MinRuntime {
			continue
		}
		if f.Person != 0 && !hasPerson(&t, f.Person) {
			continue
		}
		items = append(items, t)
	}
	switch q.Sort {
	case core.SortReleased:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Released.After(items[j].Released) })
	case core.SortRating:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Rating > items[j].Rating })
	case core.SortAdded:
		// insertion order, newest last → reverse
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	default:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Popularity > items[j].Popularity })
	}
	total := len(items)
	if q.Limit > 0 && len(items) > q.Limit {
		items = items[:q.Limit]
	}
	return core.Page{Items: items, Total: total}, nil
}

func overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (c *fakeCatalog) Search(context.Context, string, []core.MediaType, int) ([]core.Title, error) {
	return nil, nil
}
func (c *fakeCatalog) List(_ context.Context, name string, t core.MediaType, limit int) ([]core.Title, error) {
	c.count("list")
	items := c.resolve(c.lists[name+":"+string(t)])
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Similar: same primary genre, by popularity.
func (c *fakeCatalog) Similar(_ context.Context, k core.RatingKey, limit int) ([]core.Title, error) {
	c.count("similar")
	src, ok := c.titles[k]
	if !ok || len(src.Genres) == 0 {
		return nil, nil
	}
	var out []core.Title
	for _, key := range c.order {
		t := c.titles[key]
		if key != k && (t.Type == core.TypeMovie || t.Type == core.TypeShow) && len(t.Genres) > 0 && t.Genres[0] == src.Genres[0] {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Popularity > out[j].Popularity })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (c *fakeCatalog) Collection(_ context.Context, id int64) (string, []core.Title, error) {
	if id == sagaCollection {
		return "Star Saga Collection", c.resolve(c.colls[id]), nil
	}
	return "", nil, core.ErrNotFound
}
func (c *fakeCatalog) Facet(context.Context, core.MediaType, string) ([]core.FacetValue, error) {
	return nil, nil
}
func (c *fakeCatalog) NextEpisode(_ context.Context, ep core.RatingKey) (*core.Title, error) {
	next := core.EpisodeKey(ep.TMDB(), ep.Season(), ep.Episode()+1)
	if t, ok := c.titles[next]; ok {
		return &t, nil
	}
	return nil, core.ErrNotFound
}
func (c *fakeCatalog) ByExternalID(context.Context, string, string) (*core.Title, error) {
	return nil, core.ErrNotFound
}

// fakeUsers is a static user list.
type fakeUsers struct{ users []core.User }

func (f *fakeUsers) Users(context.Context) ([]core.User, error) { return f.users, nil }

// fakeCollections returns fixed owner rows.
type fakeCollections struct {
	mu    sync.Mutex
	rows  []core.CollectionRow
	calls int
}

func (f *fakeCollections) HomeRowsFor(context.Context, *core.User, core.HubContext) ([]core.CollectionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rows, nil
}

// harness wires an engine to a real in-memory state store.
type harness struct {
	t     *testing.T
	db    *sql.DB
	clock *clock
	cat   *fakeCatalog
	st    *state.State
	e     *Engine
	users *fakeUsers
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clk := newClock(testNow)
	h := &harness{t: t, db: db, clock: clk, cat: newFakeCatalog(testNow), users: &fakeUsers{}}
	h.st = state.New(db, state.Options{Now: clk.Now, Logger: quietLogger()})
	opts := Options{
		DB: db, Catalog: h.cat, State: h.st, Events: h.st, Users: h.users,
		Logger: quietLogger(), Now: clk.Now, Location: time.UTC,
		RefreshEvery: -1, RebuildDebounce: time.Hour, ColdWait: 5 * time.Second, OwnerRowsWait: 2 * time.Second,
		FlushEvery: time.Hour, ServerName: func() string { return "Test Server" },
	}
	if mutate != nil {
		mutate(&opts)
	}
	e, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	h.e = e
	return h
}

// at runs fn with the clock set to t (state writes are stamped with it).
func (h *harness) at(t time.Time, fn func()) {
	h.t.Helper()
	prev := h.clock.Now()
	h.clock.Set(t)
	fn()
	h.clock.Set(prev)
}

// finish records a completed viewing of key at t.
func (h *harness) finish(uid int64, key core.RatingKey, at time.Time) {
	h.t.Helper()
	ctx := context.Background()
	rt := 100 * time.Minute
	if t, ok := h.cat.titles[key]; ok && t.Runtime > 0 {
		rt = t.Runtime
	}
	for off := rt / 5; off < rt*9/10; off += 10 * time.Minute { // timeline ticks
		h.at(at.Add(off-rt/5), func() {
			if err := h.st.Progress(ctx, uid, key, off, rt, "playing"); err != nil {
				h.t.Fatal(err)
			}
		})
	}
	h.at(at.Add(rt*9/10), func() {
		if err := h.st.Progress(ctx, uid, key, rt*95/100, rt, "stopped"); err != nil {
			h.t.Fatal(err)
		}
	})
}

// start records a viewing stopped at fraction f.
func (h *harness) start(uid int64, key core.RatingKey, f float64, at time.Time) {
	h.t.Helper()
	rt := 100 * time.Minute
	if t, ok := h.cat.titles[key]; ok && t.Runtime > 0 {
		rt = t.Runtime
	}
	h.at(at, func() {
		if err := h.st.Progress(context.Background(), uid, key, time.Duration(float64(rt)*f), rt, "stopped"); err != nil {
			h.t.Fatal(err)
		}
	})
}

func (h *harness) rebuild(u core.User) *userModel {
	h.t.Helper()
	if err := h.e.Rebuild(context.Background(), &u); err != nil {
		h.t.Fatal(err)
	}
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	return h.e.models[u.ID]
}

func hubContext(at time.Time, device string) core.HubContext {
	hc := core.NewHubContext(at, "", "", "")
	hc.Device = device
	return hc
}

func rowByKind(rows []core.HomeRow, kind string) *core.HomeRow {
	for i := range rows {
		if rows[i].Kind == kind {
			return &rows[i]
		}
	}
	return nil
}

func kindsOf(rows []core.HomeRow) []string {
	var out []string
	for _, r := range rows {
		if !r.Hidden {
			out = append(out, r.Kind)
		}
	}
	return out
}

func genreShare(rows []core.HomeRow, genre string, skipKinds ...string) float64 {
	n, hit := 0, 0
	skip := map[string]bool{}
	for _, k := range skipKinds {
		skip[k] = true
	}
	for _, r := range rows {
		if r.Hidden || skip[r.Kind] {
			continue
		}
		for _, t := range r.Items {
			n++
			if overlap(normGenres(t.Genres), []string{genre}) {
				hit++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}
