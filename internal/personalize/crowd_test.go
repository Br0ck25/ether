package personalize

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Planted titles for the crowd tests: well rated, well voted, in a genre
// the viewer never watched and too little-known to reach any other pool.
var (
	plantedCrowd   = core.MovieKey(77001) // everyone who liked the seeds loved it
	plantedJA      = core.MovieKey(77002) // same, but Japanese
	plantedFew     = core.MovieKey(77003) // same, but 40 votes
	plantedR       = core.MovieKey(77004) // same, but rated R
	plantedShow    = core.ShowKey(77005)  // a series fans love
	plantedSingle  = core.MovieKey(77006) // one seed's top pick only
	plantedWatched = core.MovieKey(77007) // the viewer already finished it
)

// crowdSeedKeys are the crowd fan's four favourites (PG / PG-13 dramas and
// comedies).
var crowdSeedKeys = []core.RatingKey{movieKey(1, 3), movieKey(1, 9), movieKey(2, 7), movieKey(2, 11)}

func plantCrowd(h *harness, withRecs bool) {
	mk := func(k core.RatingKey, name, lang string, votes int, rating string) core.Title {
		t := core.Title{Key: k, Type: core.TypeMovie, IDs: core.IDs{TMDB: k.TMDB()}, Title: name, Year: 2019,
			Genres: []string{"Mystery"}, Language: lang, ContentRating: rating, Rating: 8.2, VoteCount: votes,
			Popularity: 8, Runtime: 110 * time.Minute, Released: time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)}
		return t
	}
	h.cat.add(mk(plantedCrowd, "The Consensus", "en", 9000, "PG-13"))
	h.cat.add(mk(plantedJA, "Kyodo", "ja", 9000, "PG-13"))
	h.cat.add(mk(plantedFew, "Few Votes", "en", 40, "PG-13"))
	h.cat.add(mk(plantedR, "Rated R", "en", 9000, "R"))
	h.cat.add(mk(plantedSingle, "One Fan", "en", 9000, "PG-13"))
	h.cat.add(mk(plantedWatched, "Seen It", "en", 9000, "PG-13"))
	h.cat.add(core.Title{Key: plantedShow, Type: core.TypeShow, IDs: core.IDs{TMDB: plantedShow.TMDB()}, Title: "Fan Series", Year: 2020,
		Genres: []string{"Mystery"}, Language: "en", ContentRating: "TV-14", Rating: 8.4, VoteCount: 4000, Popularity: 9,
		Runtime: 50 * time.Minute, Released: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), ChildCount: 2, LeafCount: 16, Status: statusEnded})
	if !withRecs {
		h.cat.dataMu.Lock()
		h.cat.recs[plantedWatched] = []core.RatingKey{}
		h.cat.dataMu.Unlock()
		return
	}
	h.cat.dataMu.Lock()
	defer h.cat.dataMu.Unlock()
	for i, s := range crowdSeedKeys {
		// Each seed's list: two of its genre's titles, then the planted
		// consensus titles, then more of its genre.
		g := 1 + i/2
		list := []core.RatingKey{movieKey(g, 20+i), movieKey(g, 30+i), plantedCrowd, plantedJA, plantedFew, plantedR, plantedShow, plantedWatched}
		if i == 3 {
			list = append([]core.RatingKey{plantedSingle}, list...)
		}
		for j := 0; j < 12; j++ {
			list = append(list, movieKey(g, 12+j))
		}
		h.cat.recs[s] = list
	}
	h.cat.recs[plantedWatched] = []core.RatingKey{}
}

func crowdFan(h *harness, uid int64) {
	for i, k := range crowdSeedKeys {
		h.finish(uid, k, testNow.AddDate(0, 0, -2-2*i))
	}
	h.finish(uid, plantedWatched, testNow.AddDate(0, 0, -30))
	h.e.syncEvents(context.Background())
}

func position(items []core.Title, k core.RatingKey) int {
	for i, t := range items {
		if t.Key == k {
			return i
		}
	}
	return -1
}

func crowdPreview(t *testing.T, h *harness, u core.User, section string) *core.HomePreview {
	t.Helper()
	hc := hubContext(testNow, core.DeviceTV)
	p, err := h.e.PreviewHome(context.Background(), &u, hc, section, 12)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCrowdConsensusBeatsSingleSeed(t *testing.T) {
	h := newHarness(t, nil)
	plantCrowd(h, true)
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(h, u.ID)
	m := h.rebuild(u)
	cons, single := m.crowd[plantedCrowd], m.crowd[plantedSingle]
	if cons.consensus != 4 || single.consensus != 1 {
		t.Fatalf("consensus = %d / %d, want 4 / 1", cons.consensus, single.consensus)
	}
	// The consensus title sits third in four lists; the single-seed title
	// tops one list. Agreement wins.
	if !(cons.score > single.score) || cons.score < 0.8 {
		t.Fatalf("crowd scores: consensus %.2f, single %.2f", cons.score, single.score)
	}
	if _, ok := m.crowd[plantedWatched]; ok {
		t.Fatal("a watched title is not a crowd candidate")
	}
	for _, s := range crowdSeedKeys {
		if _, ok := m.crowd[s]; ok {
			t.Fatalf("seed %v recommended to itself", s)
		}
	}
}

func TestCrowdScoreShape(t *testing.T) {
	// One strong seed's top pick, two and three agreeing seeds, and one
	// weak seed's passing mention.
	score := func(votes []float64) float64 {
		g := &crowdGraph{items: map[core.RatingKey]*crowdEntry{}}
		ce := &crowdEntry{votes: map[string]crowdVote{}}
		for i, w := range votes {
			ce.votes[string(rune('a'+i))] = crowdVote{w: w}
		}
		g.items[1] = ce
		finishCrowd(g)
		return ce.score
	}
	one, two, three, weak := score([]float64{1}), score([]float64{1, 1}), score([]float64{1, 1, 1}), score([]float64{0.2})
	if !(one > 0.4 && one < 0.5 && two > 0.75 && three > 0.9 && weak < 0.06) {
		t.Fatalf("crowd shape: one %.2f two %.2f three %.2f weak %.2f", one, two, three, weak)
	}
}

func TestCrowdLiftsConsensusIntoTop10(t *testing.T) {
	without := newHarness(t, nil)
	plantCrowd(without, false)
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(without, u.ID)
	without.rebuild(u)
	if top := rowByKind(crowdPreview(t, without, u, "home").Rows, KindTop10); top != nil && position(top.Items, plantedCrowd) >= 0 {
		t.Fatal("control: the planted title should not reach Top 10 without crowd evidence")
	}

	h := newHarness(t, nil)
	plantCrowd(h, true)
	crowdFan(h, u.ID)
	h.rebuild(u)
	for _, section := range []string{"home", "movies"} {
		p := crowdPreview(t, h, u, section)
		top := rowByKind(p.Rows, KindTop10)
		if top == nil {
			t.Fatalf("%s: no Top 10 (%v)", section, kindsOf(p.Rows))
		}
		pos := position(top.Items, plantedCrowd)
		if pos < 0 || pos > 2 {
			t.Fatalf("%s: consensus title at %d in Top 10 %v", section, pos, titlesOf(top.Items))
		}
		if r := top.ItemReasons[plantedCrowd]; !strings.Contains(r, "Loved by fans of") || !strings.Contains(r, "% Match") {
			t.Fatalf("%s: reason %q", section, r)
		}
		// Filters hold: votes, content rating and watched on every row, and
		// the viewer's languages on the top picks and crowd rows.
		for _, r := range p.Rows {
			if r.Source == core.RowSourcePlex {
				continue // the front end's own feeds
			}
			filtered := []core.RatingKey{plantedFew, plantedR, plantedWatched}
			if r.Kind == KindTop10 || r.Kind == KindCrowd || r.Kind == KindCrowdTrending {
				filtered = append(filtered, plantedJA)
			}
			for _, k := range filtered {
				if position(r.Items, k) >= 0 {
					t.Fatalf("%s: %s shows filtered title %v", section, r.Kind, h.cat.titles[k].Title)
				}
			}
		}
	}
	// Restricted to the movie library: the fan series never appears.
	movies := core.User{ID: 1, MaxContentRating: "PG-13", AllowedSections: []int{1}}
	for _, r := range crowdPreview(t, h, movies, "home").Rows {
		for _, it := range r.Items {
			if it.Type != core.TypeMovie && r.Source != core.RowSourcePlex {
				t.Fatalf("%s shows %s to a films-only viewer", r.Kind, it.Title)
			}
		}
	}
}

func TestCrowdRows(t *testing.T) {
	h := newHarness(t, nil)
	plantCrowd(h, true)
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(h, u.ID)
	m := h.rebuild(u)
	var crowd *candRow
	for _, r := range m.rows {
		if r.kind == KindCrowd {
			crowd = r
		}
	}
	if crowd == nil || len(crowd.items) < 5 {
		t.Fatalf("crowd row = %+v", crowd)
	}
	if crowd.title != "Fans of What You Watch Also Loved" || crowd.sectionTitles[core.TypeMovie] != "Films Loved by Fans of What You Watch" ||
		crowd.sectionTitles[core.TypeShow] != "Series Loved by Fans of What You Watch" {
		t.Fatalf("crowd titles = %q %v", crowd.title, crowd.sectionTitles)
	}
	if crowd.items[0].t.Key != plantedCrowd && crowd.items[1].t.Key != plantedCrowd {
		t.Fatalf("crowd row head = %v", crowd.items[0].t.Title)
	}
	for _, it := range crowd.items {
		if !strings.HasPrefix(it.reason, "Loved by fans of") {
			t.Fatalf("crowd reason %q", it.reason)
		}
		if it.t.Language != "en" || it.t.VoteCount < 100 {
			t.Fatalf("crowd row kept %s (%s, %d votes)", it.t.Title, it.t.Language, it.t.VoteCount)
		}
	}
	got := crowdReason(&crowdEntry{seeds: []string{"Severance", "Dark"}})
	if got != "Loved by fans of Severance and Dark" {
		t.Fatalf("reason = %q", got)
	}
	if got := crowdReason(&crowdEntry{seeds: []string{"A", "B", "C", "D"}}); got != "Loved by fans of A, B and 2 more of your favourites" {
		t.Fatalf("reason = %q", got)
	}
	// The TV page names the row for series.
	p := crowdPreview(t, h, u, "shows")
	if r := rowByKind(p.Rows, KindCrowd); r != nil && r.Title != "Series Loved by Fans of What You Watch" {
		t.Fatalf("TV crowd row title = %q", r.Title)
	}
	// Descriptors register both kinds for the Home Designer.
	descs, err := h.e.AvailableRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range descs {
		if d.Kind == KindCrowd || d.Kind == KindCrowdTrending {
			found++
			if d.Group != groupPicks || len(d.Sections) != 3 || d.Description == "" {
				t.Fatalf("descriptor %+v", d)
			}
		}
	}
	if found != 2 {
		t.Fatalf("descriptors for crowd kinds = %d", found)
	}
}

func TestCrowdTrendingRow(t *testing.T) {
	h := newHarness(t, nil)
	plantCrowd(h, true)
	// Two crowd titles and one unrelated title trend today.
	h.cat.prependList("trending_day:movie", plantedCrowd)
	h.cat.prependList("trending_day:movie", plantedSingle)
	h.cat.prependList("trending_day:movie", movieKey(1, 21))
	h.cat.prependList("trending_week:movie", movieKey(2, 32))
	h.cat.prependList("trending_week:movie", movieKey(1, 31))
	h.cat.prependList("trending_week:movie", movieKey(1, 14))
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(h, u.ID)
	m := h.rebuild(u)
	var row *candRow
	for _, r := range m.rows {
		if r.kind == KindCrowdTrending {
			row = r
		}
	}
	if row == nil || len(row.items) == 0 {
		t.Fatal("no trending-with-fans row")
	}
	for _, it := range row.items {
		if m.buzz[tasteKey(it.t.Key)] <= 0 || m.crowd[tasteKey(it.t.Key)].score <= 0 {
			t.Fatalf("%s is not trending ∩ crowd", it.t.Title)
		}
		if !strings.Contains(it.reason, " · Loved by fans of") {
			t.Fatalf("reason %q", it.reason)
		}
	}
	if position(titlesFromItems(row.items), plantedCrowd) < 0 {
		t.Fatal("the trending consensus title is missing")
	}
	if !strings.HasPrefix(row.items[position(titlesFromItems(row.items), plantedCrowd)].reason, "Rising fast today") {
		t.Fatalf("buzz reason = %q", row.items[0].reason)
	}

	// Personal trending outranks the generic feed for a known viewer.
	a := &assembly{model: m, hc: hubContext(testNow, core.DeviceTV), user: u}
	var generic *candRow
	for _, r := range m.rows {
		if r.kind == KindTrending {
			generic = r
		}
	}
	if generic == nil {
		t.Fatal("no generic trending row")
	}
	ps, _ := a.rowScore(row, row.items)
	gs, _ := a.rowScore(generic, generic.items)
	if ps <= gs {
		t.Fatalf("personal trending %.3f should outrank generic trending %.3f", ps, gs)
	}
}

// fakeTrendTrakt is a Trakt dependency with public lists.
type fakeTrendTrakt struct {
	fakeTrakt
	mu    sync.Mutex
	lists map[string][]core.Title
	calls int
}

func (f *fakeTrendTrakt) PublicList(_ context.Context, list string, typ core.MediaType, _ int) ([]core.Title, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.lists[list+":"+string(typ)], nil
}

func TestBuzzWithAndWithoutTrakt(t *testing.T) {
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	target := movieKey(1, 22) // a drama the crowd recommends, not trending on TMDB

	// Without Trakt public lists (a Trakt source that lacks them, and
	// none at all) buzz comes from TMDB alone and the build works.
	for _, tr := range []*fakeTrakt{{}, nil} {
		h := newHarness(t, func(o *Options) {
			if tr != nil {
				o.Trakt = tr
			}
		})
		plantCrowd(h, true)
		crowdFan(h, u.ID)
		m := h.rebuild(u)
		if len(m.buzz) == 0 || m.buzz[target] != 0 {
			t.Fatalf("TMDB-only buzz: %d titles, target %.2f", len(m.buzz), m.buzz[target])
		}
		if rowByKind(crowdPreview(t, h, u, "home").Rows, KindTop10) == nil {
			t.Fatal("no Top 10 without Trakt")
		}
	}

	// With Trakt: people are watching the target right now.
	tt := &fakeTrendTrakt{lists: map[string][]core.Title{}}
	h := newHarness(t, func(o *Options) { o.Trakt = tt })
	plantCrowd(h, true)
	tt.lists["trending:movie"] = []core.Title{{Key: target, Type: core.TypeMovie}, {Key: movieKey(5, 5), Type: core.TypeMovie}}
	tt.lists["watched_weekly:movie"] = []core.Title{{Key: target, Type: core.TypeMovie}}
	h.cat.prependList("trending_week:movie", movieKey(1, 31))
	crowdFan(h, u.ID)
	m := h.rebuild(u)
	if m.buzz[target] < 0.3 {
		t.Fatalf("Trakt buzz for target = %.2f", m.buzz[target])
	}
	if tt.calls == 0 {
		t.Fatal("Trakt public lists were not read")
	}
	var row *candRow
	for _, r := range m.rows {
		if r.kind == KindCrowdTrending {
			row = r
		}
	}
	if row == nil {
		t.Fatal("no trending-with-fans row")
	}
	// Watched right now on Trakt: the target joins the personal trending
	// row (TMDB alone never lists it) above a title only trending on TMDB
	// this week.
	items := titlesFromItems(row.items)
	pos, weekOnly := position(items, target), position(items, movieKey(1, 31))
	if pos < 0 || weekOnly >= 0 && pos > weekOnly {
		t.Fatalf("target at %d, week-only title at %d: %v", pos, weekOnly, titlesOf(items))
	}
	if !strings.HasPrefix(row.items[pos].reason, "People are watching this right now") {
		t.Fatalf("reason = %q", row.items[pos].reason)
	}
	// Buzz is a modest Top 10 freshness term that follows the dial.
	a := &assembly{model: m, hc: hubContext(testNow, core.DeviceTV), user: u}
	it := candItem{t: h.cat.titles[target], base: 0.5}
	top := &candRow{kind: KindTop10}
	// buzzTerm is what the target's buzz adds to its Top 10 score at a
	// Freshness setting.
	buzzTerm := func(f float64) float64 {
		a.tune = &tuning{discovery: 0.5, freshness: f, variety: 0.5}
		x, y := it, it
		with := a.adjust(top, &x, 0)
		saved := m.buzz
		m.buzz = nil
		without := a.adjust(top, &y, 0)
		m.buzz = saved
		return with - without
	}
	lo, mid, hi := buzzTerm(0), buzzTerm(0.5), buzzTerm(1)
	if !(hi > mid && mid > lo && mid > 0) || mid > 0.05 {
		t.Fatalf("buzz should be a modest term following the Freshness dial: %.3f %.3f %.3f", lo, mid, hi)
	}
}

func TestCrowdFetchBudget(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 1}
	// 24 favourites: more seeds than the graph reads and more pages than
	// one build may fetch.
	for j := 0; j < 24; j++ {
		h.finish(u.ID, movieKey(j%10, 10+j), testNow.AddDate(0, 0, -1-j))
	}
	h.e.syncEvents(context.Background())
	h.rebuild(u)
	first := h.cat.fetchedPages()
	if first != crowdFetchBudget {
		t.Fatalf("first build fetched %d pages, want the budget %d", first, crowdFetchBudget)
	}
	// The next build picks up where the budget stopped; then everything is
	// stored and builds are free.
	h.rebuild(u)
	second := h.cat.fetchedPages() - first
	want := crowdDeepSeeds*2 + (crowdMaxSeeds - crowdDeepSeeds) - crowdFetchBudget
	if second != want {
		t.Fatalf("second build fetched %d pages, want %d", second, want)
	}
	h.rebuild(u)
	if third := h.cat.fetchedPages() - first - second; third != 0 {
		t.Fatalf("third build fetched %d pages", third)
	}
	if n := h.cat.callCount("crowd"); n > 3*crowdMaxSeeds {
		t.Fatalf("%d crowd lookups for three builds", n)
	}
}

func TestCrowdFallsBackWithoutStore(t *testing.T) {
	// A catalogue without a crowd store still yields a crowd graph through
	// Similar (whose first page is TMDB's recommendations).
	h := newHarness(t, func(o *Options) { o.Catalog = similarOnly{o.Catalog.(*fakeCatalog)} })
	plantCrowd(h, false)
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(h, u.ID)
	m := h.rebuild(u)
	if len(m.crowd) == 0 {
		t.Fatal("no crowd evidence through Similar")
	}
	if h.cat.callCount("crowd") != 0 {
		t.Fatal("crowd store used")
	}
}

// similarOnly hides the fake catalogue's crowd store.
type similarOnly struct{ *fakeCatalog }

func (similarOnly) CrowdRecommendations() {}

func TestRankerCrowdFeatures(t *testing.T) {
	h := newHarness(t, nil)
	plantCrowd(h, true)
	h.cat.prependList("trending_day:movie", plantedCrowd)
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(h, u.ID)
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	if _, err := h.e.HomeCandidatesFor(context.Background(), &u, hc, 12); err != nil {
		t.Fatal(err)
	}
	snaps := h.e.rk.snapshots(u.ID, "home", hc.Bucket())
	var got *itemSnap
	for k, s := range snaps {
		if k.key == plantedCrowd {
			got = s
		}
	}
	if got == nil {
		t.Fatal("no snapshot of the consensus title")
	}
	if got.raw[fCrowd] < 0.8 || got.raw[fConsensus] != 4 || got.raw[fBuzz] <= 0 {
		t.Fatalf("crowd features = crowd %.2f consensus %.0f buzz %.2f", got.raw[fCrowd], got.raw[fConsensus], got.raw[fBuzz])
	}
	for _, f := range []rawFeature{fCrowd, fConsensus, fBuzz} {
		if featureLabel(rawFeatures[f].name) == rawFeatures[f].name {
			t.Fatalf("feature %s has no label", rawFeatures[f].name)
		}
	}
	if featureLabel("crowd") != "Loved by fans of what they watch" || featureLabel("consensus") != "Recommended by several of their favourites" || featureLabel("buzz") != "Trending now" {
		t.Fatal("feature labels changed")
	}
}

func TestRankerOldModelsAndSnapshotsStillLoad(t *testing.T) {
	// A model persisted before crowd, consensus and buzz existed.
	old := []string{"fit", "quality", "base", "popularity", "age", "trending", "runtime", "movie", "daypart_genre", "intent",
		"fatigue", "exposures", "since_shown", "server_rate", "in_progress", "new_to_viewer", "watched", "kind:top-10", "surface:home"}
	m := &rankModel{Names: old, Active: true, LogitStd: 1}
	for i := range old {
		m.Mean = append(m.Mean, 0)
		m.Std = append(m.Std, 1)
		m.Weights = append(m.Weights, 0.1*float64(i+1))
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	if _, err := h.db.Exec(`INSERT INTO personalize_ranker (id, trained_at, model) VALUES (1, 0, ?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadRankModel(context.Background(), h.db)
	if err != nil || loaded == nil {
		t.Fatalf("load = %v %v", loaded, err)
	}
	loaded.compile()
	if loaded.cRaw[fCrowd] != 0 || loaded.cRaw[fConsensus] != 0 || loaded.cRaw[fBuzz] != 0 || loaded.cRaw[fFit] == 0 {
		t.Fatalf("compiled weights = %v", loaded.cRaw)
	}
	var x [numRaw]float32
	x[fFit], x[fCrowd] = 1, 1
	base := loaded.logit(x[:], KindTop10, "home")
	x[fCrowd] = 0
	if loaded.logit(x[:], KindTop10, "home") != base {
		t.Fatal("an old model must ignore the new features")
	}

	// A version 1 snapshot (17 features) decodes with the new ones zero.
	v1 := make([]float32, v1Features)
	for i := range v1 {
		v1[i] = float32(i + 1)
	}
	var out [numRaw]float32
	out[fBuzz] = 9
	if !decodeSnapshot(1, encodeFeatures(v1), &out) || out[fWatched] != 17 || out[fCrowd] != 0 || out[fBuzz] != 0 {
		t.Fatalf("v1 snapshot = %v", out)
	}
	if decodeSnapshot(2, encodeFeatures(v1), &out) || decodeSnapshot(7, encodeFeatures(out[:]), &out) {
		t.Fatal("mismatched snapshots must not decode")
	}
}

func titlesOf(items []core.Title) []string {
	var out []string
	for _, t := range items {
		out = append(out, t.Title)
	}
	return out
}

func titlesFromItems(items []candItem) []core.Title {
	out := make([]core.Title, len(items))
	for i, it := range items {
		out[i] = it.t
	}
	return out
}

func TestCrowdHouseholdCoWatch(t *testing.T) {
	h := newHarness(t, nil)
	plantCrowd(h, true)
	u := core.User{ID: 1, MaxContentRating: "PG-13"}
	crowdFan(h, u.ID)
	coWatched, crowdBoth := movieKey(7, 40), plantedCrowd
	m := h.rebuild(u)
	if _, ok := m.crowd[coWatched]; ok {
		t.Fatal("household evidence without other viewers")
	}
	// Viewer 2 shares two favourites and finished two more titles; viewer
	// 3 shares nothing.
	h.finish(2, crowdSeedKeys[0], testNow.AddDate(0, 0, -5))
	h.finish(2, crowdSeedKeys[2], testNow.AddDate(0, 0, -6))
	h.finish(2, coWatched, testNow.AddDate(0, 0, -4))
	h.finish(2, crowdBoth, testNow.AddDate(0, 0, -3))
	h.finish(3, movieKey(8, 41), testNow.AddDate(0, 0, -3))
	h.e.syncEvents(context.Background())
	before := m.crowd[crowdBoth].score
	m = h.rebuild(u)
	cf, ok := m.crowd[coWatched]
	if !ok || cf.consensus != 0 || cf.score <= 0 || cf.score > 0.2 {
		t.Fatalf("household-only evidence = %+v (present %v)", cf, ok)
	}
	if after := m.crowd[crowdBoth].score; after <= before {
		t.Fatalf("household co-watch should add to crowd evidence: %.3f -> %.3f", before, after)
	}
	if _, ok := m.crowd[movieKey(8, 41)]; ok {
		t.Fatal("a viewer with nothing in common contributed")
	}
	if r := crowdReason(&crowdEntry{house: 1}); r != "Watched by viewers here with similar taste" {
		t.Fatalf("household reason = %q", r)
	}
}
