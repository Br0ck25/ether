package recommend

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
)

type fakeTrakt struct {
	recs, watchlist []core.Title
	history         []core.HistoryEntry
	delay           time.Duration
	calls           atomic.Int32
}

func (f *fakeTrakt) wait(ctx context.Context) error {
	f.calls.Add(1)
	if f.delay == 0 {
		return nil
	}
	select {
	case <-time.After(f.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeTrakt) Recommendations(ctx context.Context, _ int64, _ int) ([]core.Title, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return f.recs, nil
}
func (f *fakeTrakt) Watchlist(ctx context.Context, _ int64, _ int) ([]core.Title, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return f.watchlist, nil
}
func (f *fakeTrakt) PullHistory(ctx context.Context, _ int64, _ int) ([]core.HistoryEntry, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	return f.history, nil
}

type fakeLLM struct {
	rows  []core.Hub
	more  []core.Title
	seeds []core.Title
	mu    sync.Mutex
}

func (f *fakeLLM) ThemedRows(_ context.Context, _ *core.User, seeds []core.Title, _ int) ([]core.Hub, error) {
	f.mu.Lock()
	f.seeds = seeds
	f.mu.Unlock()
	return f.rows, nil
}
func (f *fakeLLM) MoreLikeThis(context.Context, *core.Title, int) ([]core.Title, error) {
	return f.more, nil
}

func TestTraktAndLLMRows(t *testing.T) {
	ctx := context.Background()
	heat := core.Title{Key: core.MovieKey(949), Type: core.TypeMovie, Title: "Heat", Year: 1995, Genres: []string{"Crime"}}
	thief := core.Title{Key: core.MovieKey(11524), Type: core.TypeMovie, Title: "Thief", Year: 1981, Genres: []string{"Crime"}}
	tr := &fakeTrakt{recs: []core.Title{heat, arrival}, watchlist: []core.Title{heat, goryR, thief}}
	ll := &fakeLLM{rows: []core.Hub{{ID: "llm.mind-benders", Title: "Mind-benders", Items: []core.Title{arrival, thief, goryR}}}}
	r, _, user, _ := fixtureWith(t, func(o *Options) { o.Trakt = tr; o.LLM = ll })
	hubs, err := r.HomeHubs(ctx, user, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := hubIDs(hubs)
	pos := map[string]int{}
	for i, id := range ids {
		pos[id] = i
	}
	for _, id := range []string{"home.toppicks", "trakt.recommendations", "trakt.watchlist", "llm.mind-benders", "home.newreleases"} {
		if _, ok := pos[id]; !ok {
			t.Fatalf("missing %s: %v", id, ids)
		}
	}
	if !(pos["home.toppicks"] < pos["trakt.recommendations"] && pos["trakt.recommendations"] < pos["llm.mind-benders"] && pos["llm.mind-benders"] < pos["home.newreleases"]) {
		t.Fatalf("source order: %v", ids)
	}
	recs := hubs[pos["trakt.recommendations"]]
	if len(recs.Items) != 1 || recs.Items[0].Key != heat.Key {
		t.Fatalf("trakt recs should drop watched Arrival: %+v", recs.Items)
	}
	// Watchlist is the user's own list: Heat repeats, R title is filtered.
	wl := hubs[pos["trakt.watchlist"]]
	if len(wl.Items) != 2 || wl.Items[0].Key != heat.Key || wl.Items[1].Key != thief.Key {
		t.Fatalf("watchlist: %+v", wl.Items)
	}
	// LLM row: watched + restricted + already-shown (Thief, via watchlist?) rules.
	lh := hubs[pos["llm.mind-benders"]]
	if lh.Context != "hub.home.llm" {
		t.Fatalf("llm context: %q", lh.Context)
	}
	for _, it := range lh.Items {
		if it.Key == arrival.Key || it.Key == goryR.Key {
			t.Fatalf("llm row leaked watched/restricted title %s", it.Title)
		}
	}
	ll.mu.Lock()
	seeds := ll.seeds
	ll.mu.Unlock()
	if len(seeds) == 0 {
		t.Fatal("llm got no seeds")
	}
	for _, sd := range seeds {
		if sd.Key == inProg.Key {
			t.Fatal("in-progress title used as a liked seed")
		}
	}
}

func TestSlowTraktDoesNotStallHome(t *testing.T) {
	tr := &fakeTrakt{delay: 5 * time.Second}
	r, _, user, _ := fixtureWith(t, func(o *Options) { o.Trakt = tr; o.SourceTimeout = 50 * time.Millisecond })
	start := time.Now()
	hubs, err := r.HomeHubs(context.Background(), user, 10)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Home took %v with a slow Trakt", el)
	}
	if findHub(hubs, "home.continue") == nil || findHub(hubs, "trakt.") != nil {
		t.Fatalf("hubs: %v", hubIDs(hubs))
	}
}

func TestTraktHistoryMergesIntoModel(t *testing.T) {
	ctx := context.Background()
	// Only on Trakt: the user watched Sicario elsewhere.
	tr := &fakeTrakt{history: []core.HistoryEntry{{UserID: 1, Key: sicario.Key, ViewedAt: time.Now().Add(-time.Hour), Completed: true}}}
	r, _, user, _ := fixtureWith(t, func(o *Options) { o.Trakt = tr })
	p, err := r.BuildProfile(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range p.Signals {
		if s.Title.Key == sicario.Key && s.Weight > 0.9 {
			found = true
		}
	}
	if !found {
		t.Fatalf("trakt play not merged: %+v", p.Signals)
	}
	if p.Genre["Thriller"] <= 0 {
		t.Fatalf("trakt play did not shape affinities: %v", p.Genre)
	}
	picks, _ := r.TopPicks(ctx, user, 50)
	for _, pk := range picks {
		if pk.Title.Key == sicario.Key {
			t.Fatal("title watched on Trakt recommended")
		}
	}
}

func TestKeywordAffinityAndWhy(t *testing.T) {
	p := &Profile{
		Genre: map[string]float64{"Drama": 0.2}, Keyword: map[string]float64{"time travel": 1.2, "heist": -0.8},
		Person: map[int64]float64{}, PersonName: map[int64]string{}, Decade: map[int]float64{},
		Language: map[string]float64{}, Studio: map[string]float64{}, Collection: map[int64]float64{},
	}
	a := core.Title{Key: core.MovieKey(1), Genres: []string{"Drama"}, Keywords: []string{"Time Travel", "love"}}
	b := core.Title{Key: core.MovieKey(2), Genres: []string{"Drama"}, Keywords: []string{"heist"}}
	sa, why := p.Score(a)
	sb, _ := p.Score(b)
	if sa <= sb {
		t.Fatalf("keyword affinity: %v <= %v", sa, sb)
	}
	if !strings.Contains(why, "time travel") {
		t.Fatalf("why: %q", why)
	}
	// Deterministic: repeated scoring is identical.
	for i := 0; i < 20; i++ {
		s2, w2 := p.Score(a)
		if s2 != sa || w2 != why {
			t.Fatal("Score not deterministic")
		}
	}
}

func TestSignalWeights(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	completed := core.WatchStatus{ViewCount: 1}
	loved := core.WatchStatus{ViewCount: 1, UserRating: 10}
	hated := core.WatchStatus{ViewCount: 1, UserRating: 1}
	abandoned := core.WatchStatus{ViewOffset: 10 * time.Minute, Duration: 100 * time.Minute}
	recentStart := abandoned
	if w := signalWeight(completed, true, now, now); w != 1 {
		t.Fatalf("completion %v", w)
	}
	if w := signalWeight(loved, true, now, now); w <= 2 {
		t.Fatalf("loved %v", w)
	}
	if w := signalWeight(hated, true, now, now); w >= 0.1 {
		t.Fatalf("hated %v", w)
	}
	if w := signalWeight(abandoned, false, now.Add(-30*24*time.Hour), now); w >= 0 {
		t.Fatalf("abandoned should be negative: %v", w)
	}
	if w := signalWeight(recentStart, false, now.Add(-time.Hour), now); w <= 0 || w >= 0.5 {
		t.Fatalf("recent start %v", w)
	}
	old := signalWeight(completed, true, now.Add(-120*24*time.Hour), now)
	if old < 0.49 || old > 0.51 {
		t.Fatalf("120-day half-life: %v", old)
	}
}

func TestInvalidationIgnoresTimelineTicks(t *testing.T) {
	ctx := context.Background()
	r, st, user, _ := fixtureWith(t, nil)
	if _, err := r.TopPicks(ctx, user, 10); err != nil {
		t.Fatal(err)
	}
	cachedEntry := func() *entry {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.cache[1]
	}
	before := cachedEntry()
	if before == nil {
		t.Fatal("not cached")
	}
	_ = st.Progress(ctx, 1, comedy.Key, 30*time.Minute, 100*time.Minute, "playing")
	if cachedEntry() != before {
		t.Fatal("playing tick invalidated the model")
	}
	_ = st.Progress(ctx, 1, comedy.Key, 31*time.Minute, 100*time.Minute, "stopped")
	if cachedEntry() != nil {
		t.Fatal("stop should invalidate")
	}
	// Other users' caches are untouched by user 1's events.
	other := &core.User{ID: 2}
	if _, err := r.TopPicks(ctx, other, 5); err != nil {
		t.Fatal(err)
	}
	_ = st.Rate(ctx, 1, comedy.Key, 3)
	r.mu.Lock()
	_, ok := r.cache[2]
	r.mu.Unlock()
	if !ok {
		t.Fatal("user 2 cache dropped by user 1 event")
	}
}

func TestConcurrentHubsAcrossUsers(t *testing.T) {
	ctx := context.Background()
	r, st, _, _ := fixtureWith(t, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := &core.User{ID: int64(i%4 + 1)}
			if _, err := r.HomeHubs(ctx, u, 5); err != nil {
				errs <- err
			}
			if _, err := r.SectionHubs(ctx, u, core.TypeMovie, 5); err != nil {
				errs <- err
			}
			if i%3 == 0 {
				_ = st.Scrobble(ctx, u.ID, sicario.Key)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// A cancelled caller gets ctx.Err(), not a poisoned cache.
	r.Invalidate(1)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.TopPicks(cctx, &core.User{ID: 1}, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if _, err := r.TopPicks(ctx, &core.User{ID: 1}, 5); err != nil {
		t.Fatal(err)
	}
}

func TestRelatedEpisodeAndLLMInterleave(t *testing.T) {
	ctx := context.Background()
	ll := &fakeLLM{more: []core.Title{sicario}}
	r, _, user, cat := fixtureWith(t, func(o *Options) { o.LLM = ll })
	cat.similar[sever.Key] = []core.RatingKey{arrival.Key, dune2.Key}
	ep := ep3
	ep.ShowKey = sever.Key
	cat.add(ep)
	rel, err := r.Related(ctx, user, ep.Key, 10)
	if err != nil {
		t.Fatal(err)
	}
	h := findHub(rel, "tv.similar")
	if h == nil || h.Context != "hub.tv.similar" || h.Type != core.TypeShow {
		t.Fatalf("episode related: %+v", rel)
	}
	keys := map[core.RatingKey]bool{}
	for _, it := range h.Items {
		keys[it.Key] = true
	}
	if !keys[dune2.Key] || !keys[sicario.Key] || keys[arrival.Key] {
		t.Fatalf("similar via show + llm, minus watched: %+v", h.Items)
	}
	if _, err := r.Related(ctx, user, core.MovieKey(424242), 10); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown title: %v", err)
	}
}

func TestContentRatingPushedToBrowse(t *testing.T) {
	ctx := context.Background()
	r, _, user, cat := fixtureWith(t, nil)
	if _, err := r.SectionHubs(ctx, user, core.TypeMovie, 5); err != nil {
		t.Fatal(err)
	}
	cat.mu.Lock()
	defer cat.mu.Unlock()
	pushed := false
	for _, f := range cat.filters {
		if f.MaxContentRating == "PG-13" {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("MaxContentRating not pushed down: %+v", cat.filters)
	}
}

func TestNoStateStillServesCatalogueRows(t *testing.T) {
	cat := newFake()
	for _, title := range []core.Title{dune, comedy, newMovie, sever} {
		cat.add(title)
	}
	cat.lists["trending_week:movie"] = []core.RatingKey{comedy.Key}
	r := New(Options{Catalog: cat})
	hubs, err := r.HomeHubs(context.Background(), nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if findHub(hubs, "home.trending.week") == nil || findHub(hubs, "home.newreleases") == nil || findHub(hubs, "home.continue") != nil {
		t.Fatalf("stateless hubs: %v", hubIDs(hubs))
	}
	if _, err := New(Options{}).HomeHubs(context.Background(), nil, 5); err == nil {
		t.Fatal("missing catalog should error")
	}
	_ = state.EventProgress
}
