package personalize

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// tvFan watches series: every first-season episode of four shows weeks
// ago, three episodes of a complete two-season drama a month ago (then
// stalled), and two episodes of a crime show this week.
func tvFan(h *harness, uid int64) {
	day := func(d int) time.Time { return testNow.AddDate(0, 0, -d) }
	full := []core.RatingKey{showKey(1, 1), showKey(9, 3), showKey(6, 0), showKey(0, 3)} // Drama, Crime, Thriller, Sci-Fi
	for i, sk := range full {
		for e := 1; e <= 8; e++ {
			h.finish(uid, core.EpisodeKey(sk.TMDB(), 1, e), day(40-i*5).Add(time.Duration(e)*time.Hour))
		}
	}
	for e := 1; e <= 3; e++ {
		h.finish(uid, core.EpisodeKey(showKey(1, 4).TMDB(), 1, e), day(30).Add(time.Duration(e)*time.Hour))
	}
	for e := 1; e <= 2; e++ {
		h.finish(uid, core.EpisodeKey(showKey(9, 0).TMDB(), 1, e), day(3).Add(time.Duration(e)*time.Hour))
	}
	h.e.syncEvents(context.Background())
}

func visibleEngineRows(rows []core.HomeRow) []core.HomeRow {
	var out []core.HomeRow
	for _, r := range rows {
		if !r.Hidden && r.Source != core.RowSourcePlex {
			out = append(out, r)
		}
	}
	return out
}

func leadKeys(rows []core.HomeRow) map[core.RatingKey]bool {
	return leadFromRows(rows, leadItems).keys
}

func pageKeys(rows []core.HomeRow, perRow int) map[core.RatingKey]bool {
	out := map[core.RatingKey]bool{}
	for _, r := range visibleEngineRows(rows) {
		for i, t := range r.Items {
			if i < perRow {
				out[tasteKey(t.Key)] = true
			}
		}
	}
	return out
}

func kindSet(rows []core.HomeRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range visibleEngineRows(rows) {
		out[r.Kind] = true
	}
	return out
}

func preview(t *testing.T, h *harness, u core.User, hc core.HubContext, section string) *core.HomePreview {
	t.Helper()
	p, err := h.e.PreviewHome(context.Background(), &u, hc, section, 12)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var lensKinds = map[string]bool{KindTasteCluster: true, KindEra: true, KindWorld: true, KindOccasion: true, KindAcclaimed: true,
	KindTopGenre: true, KindBridge: true, KindStudio: true, KindNewSeasons: true, KindFinishShow: true, KindStartNew: true, KindBinge: true, KindSticksLanding: true, KindLimited: true,
	KindAiringTaste: true, KindNetwork: true, KindStandalone: true, KindPerson: true, KindFranchise: true, KindHiddenGems: true, KindTrendingTaste: true, KindDifferent: true}

func TestRecommendedPagesDifferFromHome(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 1, Username: "mia", IsOwner: true}
	h.users.users = []core.User{u}
	sciFiFan(h, u.ID)
	tvFan(h, u.ID)
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	home := preview(t, h, u, hc, "home")
	movies := preview(t, h, u, hc, "movies")
	shows := preview(t, h, u, hc, "shows")
	t.Logf("home:\n%s\nmovies:\n%s\nshows:\n%s", describe(home.Rows), describe(movies.Rows), describe(shows.Rows))

	for k := range kindSet(home.Rows) {
		if lensKinds[k] {
			t.Errorf("lens row %s on Home by default", k)
		}
	}
	lenses := func(rows []core.HomeRow) int {
		n := 0
		for k := range kindSet(rows) {
			if lensKinds[k] {
				n++
			}
		}
		return n
	}
	if n := lenses(movies.Rows); n < 4 {
		t.Errorf("Movies has %d lens kinds: %v", n, kindsOf(movies.Rows))
	}
	if n := lenses(shows.Rows); n < 4 {
		t.Errorf("TV Shows has %d lens kinds: %v", n, kindsOf(shows.Rows))
	}
	// Outside the shared discovery head, most Recommended rows are kinds Home
	// does not have.
	// Every page opens with the viewer's top picks, which may repeat the
	// Home's (toppicks.go); the rest of the page is what must differ.
	for name, p := range map[string]*core.HomePreview{"home": home, "movies": movies, "shows": shows} {
		if rows := visibleEngineRows(p.Rows); len(rows) == 0 || (rows[0].Kind != KindTop10 && rows[0].Kind != KindTop10Today) {
			t.Errorf("%s does not open with top picks: %v", name, kindsOf(p.Rows))
		}
	}
	notLead := func(rows []core.HomeRow) []core.HomeRow {
		var out []core.HomeRow
		for _, r := range rows {
			if r.Kind != KindTop10 && r.Kind != KindTop10Today && r.Kind != KindNewReleases && r.Kind != KindTrending {
				out = append(out, r)
			}
		}
		return out
	}
	lead := leadKeys(home.Rows) // what the engine holds back: the Home's first rows, top picks included
	home.Rows, movies.Rows, shows.Rows = notLead(home.Rows), notLead(movies.Rows), notLead(shows.Rows)
	hk := kindSet(home.Rows)
	for name, p := range map[string]*core.HomePreview{"movies": movies, "shows": shows} {
		rows := visibleEngineRows(p.Rows)
		shared := 0
		for _, r := range rows {
			if hk[r.Kind] {
				shared++
			}
		}
		if 3*shared > len(rows) {
			t.Errorf("%s shares %d of %d row kinds with Home", name, shared, len(rows))
		}
		// Titles: nothing the Home leads with, little overlap overall.
		for _, r := range visibleEngineRows(p.Rows) {
			if leadsWithHome(r, lead) {
				t.Errorf("%s row %s leads with a Home lead title", name, r.HubID)
			}
		}
		if s := jaccard(pageKeys(home.Rows, 12), pageKeys(p.Rows, 12)); s > 0.25 {
			hk := pageKeys(home.Rows, 12)
			for _, r := range visibleEngineRows(p.Rows) {
				for i, it := range r.Items {
					if i < 12 && hk[tasteKey(it.Key)] {
						t.Logf("shared: %s in %s (pos %d)", it.Title, r.HubID, i)
					}
				}
			}
			t.Errorf("%s shares %.0f%% of its titles with Home", name, s*100)
		}
		if p.HomeDeduped == 0 {
			t.Errorf("%s held nothing back from a Home that leads with the same taste", name)
		}
	}
	// Movies and TV Shows pages are type-native.
	for _, r := range visibleEngineRows(movies.Rows) {
		for _, it := range r.Items {
			if it.Type != core.TypeMovie {
				t.Fatalf("%s in Movies row %s", it.Title, r.HubID)
			}
		}
	}
	// A small catalogue may exhaust trending during cross-page dedupe;
	// the remaining TV discovery still includes series-specific lenses.
	if lenses(shows.Rows) < 4 {
		t.Errorf("TV Shows lost its series discovery: %v", kindsOf(shows.Rows))
	}
}

// leadsWithHome reports whether a row shows a Home lead title before one
// of its own: held titles may only trail a row too thin without them.
// Anchored rows (franchise order, finish what you started) are exempt.
func leadsWithHome(r core.HomeRow, lead map[core.RatingKey]bool) bool {
	if r.Kind == KindTop10 || r.Kind == KindTop10Today || r.Kind == KindNewReleases || r.Kind == KindTrending || r.Kind == KindFranchise || r.Kind == KindFinishShow || r.Kind == KindNewSeasons {
		return false
	}
	seenLead := false
	for _, t := range r.Items {
		if lead[tasteKey(t.Key)] {
			seenLead = true
		} else if seenLead {
			return true
		}
	}
	return false
}

func TestDedupeHomeSwitch(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	u := core.User{ID: 1, Username: "mia", IsOwner: true}
	h.users.users = []core.User{u}
	sciFiFan(h, u.ID)
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	home := preview(t, h, u, hc, "home")
	lead := leadKeys(home.Rows)

	on := preview(t, h, u, hc, "movies")
	off := false
	draft := core.HomeLayout{Global: []core.LayoutPage{{Section: "movies", DedupeHome: &off, Rows: []core.RowPolicy{}}}}
	offP, err := h.e.PreviewLayout(ctx, &u, hc, "movies", 12, draft)
	if err != nil {
		t.Fatal(err)
	}
	count := func(p *core.HomePreview) int {
		n := 0
		var rows []core.HomeRow // the top picks may repeat the Home by design
		for _, r := range p.Rows {
			if r.Kind != KindTop10 && r.Kind != KindTop10Today && r.Kind != KindNewReleases && r.Kind != KindTrending {
				rows = append(rows, r)
			}
		}
		for k := range pageKeys(rows, 12) {
			if lead[k] {
				n++
			}
		}
		return n
	}
	t.Logf("on: %d lead titles, held %d; off: %d", count(on), on.HomeDeduped, count(offP))
	if on.HomeDeduped == 0 || offP.HomeDeduped != 0 {
		t.Fatalf("homeDeduped on=%d off=%d", on.HomeDeduped, offP.HomeDeduped)
	}
	// The top picks draft first on both, so they take many lead titles
	// either way; dedupe must never add Home titles below them.
	if count(offP) < count(on) || count(offP) < 5 {
		t.Fatalf("switching dedupe off should bring Home lead titles back: on %d off %d", count(on), count(offP))
	}
	// A viewer page overrides the global switch.
	vdraft := core.HomeLayout{Global: draft.Global, Viewers: []core.ViewerLayout{{UserID: u.ID, Pages: []core.LayoutPage{{Section: "movies", DedupeHome: boolPtr(true), Rows: []core.RowPolicy{}}}}}}
	vp, err := h.e.PreviewLayout(ctx, &u, hc, "movies", 12, vdraft)
	if err != nil {
		t.Fatal(err)
	}
	if vp.HomeDeduped == 0 {
		t.Fatal("viewer page DedupeHome=true did not override the global page")
	}
	// The production path dedupes the same way, deterministically.
	hubs, _ := h.e.SectionCandidatesFor(ctx, &u, core.TypeMovie, hc, 12)
	again, _ := h.e.SectionCandidatesFor(ctx, &u, core.TypeMovie, hc, 12)
	if len(hubs) == 0 || len(hubs) != len(again) || hubs[0].ID != again[0].ID {
		t.Fatalf("section candidates not stable: %v vs %v", hubIDsOf(hubs), hubIDsOf(again))
	}
	for _, hb := range hubs {
		if leadsWithHome(core.HomeRow{Kind: h.e.kindOfHub(hb.ID), Items: hb.Items}, lead) {
			t.Fatalf("production Movies row %s leads with a Home lead title", hb.ID)
		}
	}
}

func boolPtr(b bool) *bool        { return &b }
func floatPtr(f float64) *float64 { return &f }

func TestHomeLeadExclusionDemotesThinRows(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var keys []core.RatingKey
	for j := 1; j <= 6; j++ {
		keys = append(keys, movieKey(0, j))
	}
	thin := testRow(KindAcclaimed, "acclaimed", items(cat, keys...))
	thin.minItems = 4
	wide := testRow(KindTasteCluster, "cluster.x", items(cat, append(append([]core.RatingKey{}, keys[:3]...), movieKey(1, 1), movieKey(1, 2), movieKey(1, 3), movieKey(1, 4), movieKey(1, 5))...))
	lead := &homeLead{keys: map[core.RatingKey]bool{}, rows: map[string]bool{}}
	for _, k := range keys[:4] {
		lead.keys[k] = true
	}
	rows := run(emptyModel(thin, wide), func(a *assembly) { a.section = core.TypeMovie; a.lead = lead })
	w := rowByKind(rows, KindTasteCluster)
	if w == nil {
		t.Fatalf("rows %v", kindsOf(rows))
	}
	for _, it := range w.Items {
		if lead.keys[it.Key] {
			t.Fatalf("wide row kept lead title %s", it.Title)
		}
	}
	th := rowByKind(rows, KindAcclaimed)
	if th == nil || len(th.Items) < 4 {
		t.Fatalf("thin row should be demoted, not dropped: %v", kindsOf(rows))
	}
	if lead.keys[th.Items[0].Key] {
		t.Fatalf("demoted row still leads with a Home title: %s", th.Items[0].Title)
	}
}

func TestDedupeBeforeTruncation(t *testing.T) {
	cat := newFakeCatalog(testNow)
	k := func(g, j int) core.RatingKey { return movieKey(g, j) }
	// Home rows: Top 10 and New This Week survive a two-row page; Comfort
	// ranks third and is cut. Comfort's best titles are New This Week's
	// last ones.
	top := testRow(KindTop10, "top10", items(cat, k(0, 1), k(0, 2), k(0, 3), k(0, 4), k(0, 5), k(0, 6)))
	keep := testRow(KindNewThisWeek, "newthisweek", items(cat, k(1, 1), k(1, 2), k(1, 3), k(1, 4), k(1, 5), k(1, 6)))
	keep.minItems = 6
	cut := testRow(KindComfort, "comfort", items(cat, k(1, 5), k(1, 6), k(2, 1), k(2, 2), k(2, 3), k(2, 4)))
	cut.minItems = 2
	scores := map[string]float64{"top10": 3, "newthisweek": 2, "comfort": 1}
	m := emptyModel(top, keep, cut)
	page := func(pageLen int) []core.HomeRow {
		return run(m, func(a *assembly) {
			a.pageLen = pageLen
			a.layout = &core.HomeLayout{}
			for _, c := range m.rows {
				c.scale = scores[c.id]
			}
		})
	}
	// Without the page length every row drafts as an equal: Comfort steals
	// two titles and New This Week is too thin to show.
	if r := rowByKind(page(0), KindNewThisWeek); r != nil {
		t.Fatalf("expected the old starvation without a page length: %v", kindsOf(page(0)))
	}
	rows := page(2)
	nt := rowByKind(rows, KindNewThisWeek)
	if nt == nil || len(nt.Items) != 6 {
		t.Fatalf("a surviving row lost titles to a cut row: %v", kindsOf(rows))
	}
	final := arrangeLayout(rows, core.HomeLayout{}, 1, hubContext(testNow, core.DeviceTV), "home", 2)
	var shown []string
	for _, r := range final {
		if !r.Hidden {
			shown = append(shown, r.Kind)
		}
	}
	if strings.Join(shown, ",") != KindTop10+","+KindNewThisWeek {
		t.Fatalf("final page = %v", shown)
	}
	if c := rowByKind(rows, KindComfort); c != nil {
		for _, it := range c.Items {
			if it.Key == k(1, 5) || it.Key == k(1, 6) {
				t.Fatal("the cut row still claimed a surviving row's title")
			}
		}
	}
}

func TestSurvivorsReserveSectionFrontEndRows(t *testing.T) {
	cat := newFakeCatalog(testNow)
	a := &assembly{section: core.TypeMovie, hc: hubContext(testNow, core.DeviceTV), pageLen: 4, layout: &core.HomeLayout{}, user: core.User{ID: 1}}
	var sel []*rankedRow
	for i, kind := range []string{KindTasteCluster, KindAcclaimed, KindEra, KindWorld} {
		c := testRow(kind, kind, items(cat, movieKey(i, 1)))
		sel = append(sel, &rankedRow{c: c, hubID: "movie." + kind})
	}
	got := a.survivors(sel)
	if len(got) != 2 || !got[sel[0]] || !got[sel[1]] {
		t.Fatalf("Continue Watching and Recently Added take two of four slots: %v", got)
	}
}

func TestSurfaceBanditFallsBackToKind(t *testing.T) {
	g := arm{shows: 300, reward: 30}
	u := arm{shows: 50, reward: 10}
	// Cold surface arms: exactly the kind-level estimate.
	m1, s1 := surfaceRowRate(0.08, 1, g, u, arm{}, arm{}, 0, nil)
	m0, s0 := rowRate(0.08, g, u, 0, nil)
	if m1 != m0 || s1 != s0 {
		t.Fatalf("cold surface %v/%v != kind %v/%v", m1, s1, m0, s0)
	}
	// A surface weight below 1 lowers the cold prior.
	if mw, _ := surfaceRowRate(0.08, 0.5, arm{}, arm{}, arm{}, arm{}, 0, nil); mw >= 0.08 {
		t.Fatalf("weighted cold prior %v", mw)
	}
	// Evidence on this surface moves the estimate; the kind's other
	// evidence still anchors it.
	hot, _ := surfaceRowRate(0.08, 1, g, u, arm{shows: 40, reward: 20}, arm{shows: 40, reward: 20}, 0, nil)
	cold, _ := surfaceRowRate(0.08, 1, g, u, arm{shows: 40, reward: 0}, arm{shows: 40, reward: 0}, 0, nil)
	if !(hot > m0 && cold < m0) {
		t.Fatalf("surface evidence: hot %v, kind %v, cold %v", hot, m0, cold)
	}
	if cold <= 0.001 {
		t.Fatalf("a cold surface arm should not collapse: %v", cold)
	}
}

func TestSurfaceArmsLearnPerPage(t *testing.T) {
	cat := newFakeCatalog(testNow)
	mk := func(kind, id string, g int) *candRow {
		var keys []core.RatingKey
		for j := 1; j <= 6; j++ {
			keys = append(keys, movieKey(g, j))
		}
		return testRow(kind, id, items(cat, keys...))
	}
	m := emptyModel(mk(KindTrending, "trending", 0), mk(KindNewReleases, "newreleases", 1))
	first := func(section core.MediaType, arms map[string]arm) string {
		rows := run(m, func(a *assembly) { a.section = section; a.arms = arms })
		return visibleEngineRows(rows)[0].Kind
	}
	movieHome := first("", nil)
	moviePage := first(core.TypeMovie, nil)
	// This viewer plays new releases on the Movies page and ignores
	// trending there.
	learned := map[string]arm{
		surfaceKey(surfMovies, KindTrending):    {shows: 60, reward: 0},
		surfaceKey(surfMovies, KindNewReleases): {shows: 60, reward: 30},
	}
	if got := first(core.TypeMovie, learned); got != KindNewReleases {
		t.Fatalf("Movies page did not learn: %s (was %s)", got, moviePage)
	}
	if got := first("", learned); got != movieHome {
		t.Fatalf("Movies evidence changed the Home: %s, was %s", got, movieHome)
	}
}

func TestKindShowsCountDistinctDaysPerSurface(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	u := &core.User{ID: 7}
	hc := hubContext(testNow, core.DeviceTV)
	a, b := h.cat.titles[movieKey(0, 1)], h.cat.titles[movieKey(0, 2)]
	for d := 0; d < 3; d++ {
		hc.Now = testNow.AddDate(0, 0, -d)
		h.e.RecordImpressions(u, hc, []core.Hub{
			{ID: "home.because.1", Items: []core.Title{a}}, {ID: "home.because.2", Items: []core.Title{b}},
			{ID: "home.because.3", Items: []core.Title{b}}, {ID: "movie.because.1", Items: []core.Title{a}},
		})
	}
	h.e.Flush(ctx)
	shows, err := h.e.store.kindShows(ctx, testNow.AddDate(0, 0, -10))
	if err != nil || shows[u.ID][KindBecause] != 3 {
		t.Fatalf("kind shows = %v %v, want 3 days", shows, err)
	}
	surf, err := h.e.store.surfaceShows(ctx, testNow.AddDate(0, 0, -10))
	if err != nil || surf[u.ID][surfaceKey(surfHome, KindBecause)] != 3 || surf[u.ID][surfaceKey(surfMovies, KindBecause)] != 3 {
		t.Fatalf("surface shows = %v %v", surf, err)
	}
	h.e.invalidateStats()
	st := h.e.statsFor(ctx)
	if st.users[u.ID][surfaceKey(surfMovies, KindBecause)].shows != 3 || st.global[surfaceKey(surfHome, KindBecause)].shows != 3 {
		t.Fatalf("stats arms = %+v / %+v", st.users[u.ID], st.global)
	}
}

func TestTuningDialsMoveOutcomes(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 1, Username: "mia", IsOwner: true}
	h.users.users = []core.User{u}
	sciFiFan(h, u.ID)
	m := h.rebuild(u)
	at := func(section core.MediaType, tu tuning) []core.HomeRow {
		a := &assembly{model: m, user: u, hc: hubContext(testNow, core.DeviceTV), section: section, limit: 12,
			global: map[string]arm{}, day: dayOf(testNow, time.UTC), maxRows: 100, tune: &tu, layout: &core.HomeLayout{}, pageLen: 16}
		return a.run()
	}
	mean := func(rows []core.HomeRow, f func(*core.Title) float64) float64 {
		sum, n := 0.0, 0
		for _, r := range visibleEngineRows(rows) {
			for i := range r.Items {
				if i < 10 {
					sum += f(&r.Items[i])
					n++
				}
			}
		}
		return sum / float64(max(n, 1))
	}
	pos := func(rows []core.HomeRow, kind string) int {
		for i, r := range visibleEngineRows(rows) {
			if r.Kind == kind {
				return i
			}
		}
		return 99
	}
	base := defaultTuning
	// Discovery: unfamiliar genres rise, the Thompson sample strays further.
	lo, hi := base, base
	lo.discovery, hi.discovery = 0, 1
	nov := func(t *core.Title) float64 { return novelty(m.taste, t) }
	l, r := at(core.TypeMovie, lo), at(core.TypeMovie, hi)
	if !(mean(r, nov) > mean(l, nov)) {
		t.Errorf("discovery 1 novelty %.3f <= discovery 0 %.3f", mean(r, nov), mean(l, nov))
	}
	if pos(r, KindDifferent) > pos(l, KindDifferent) {
		t.Errorf("Something Different should not fall with discovery: %d vs %d", pos(r, KindDifferent), pos(l, KindDifferent))
	}
	if hi.exploreScale() <= lo.exploreScale() {
		t.Error("exploration scale")
	}
	// Freshness: newer titles and fresh rows rise.
	lo, hi = base, base
	lo.freshness, hi.freshness = 0, 1
	rec := func(t *core.Title) float64 { return recency(t, testNow) }
	l, r = at(core.TypeMovie, lo), at(core.TypeMovie, hi)
	if !(mean(r, rec) > mean(l, rec)) {
		t.Errorf("freshness 1 recency %.3f <= freshness 0 %.3f", mean(r, rec), mean(l, rec))
	}
	if pos(r, KindTopRated) < pos(l, KindTopRated) {
		t.Errorf("Top Rated should not rise with freshness: %d vs %d", pos(r, KindTopRated), pos(l, KindTopRated))
	}
	// Variety: rows spread across genres, titles within rows too.
	lo, hi = base, base
	lo.variety, hi.variety = 0, 1
	if hi.lambda() >= lo.lambda() {
		t.Error("variety lowers MMR lambda")
	}
	spread := func(rows []core.HomeRow) int {
		seen := map[string]bool{}
		for i, r := range visibleEngineRows(rows) {
			if i >= 8 {
				break
			}
			gs := map[string]int{}
			for _, it := range r.Items[:min(6, len(r.Items))] {
				for _, g := range normGenres(it.Genres) {
					gs[g]++
				}
			}
			for g := range gs {
				seen[g] = true
			}
		}
		return len(seen)
	}
	l, r = at(core.TypeMovie, lo), at(core.TypeMovie, hi)
	if spread(r) < spread(l) {
		t.Errorf("variety 1 covers %d genres, variety 0 %d", spread(r), spread(l))
	}
	// Bounded: the item dials never exceed ±0.26.
	a := &assembly{model: m, hc: hubContext(testNow, core.DeviceTV)}
	for _, tu := range []tuning{{0, 0, 0}, {1, 1, 1}, {0, 1, 0.5}, {1, 0, 0.5}} {
		tu := tu
		a.tune = &tu
		for _, k := range []core.RatingKey{movieKey(0, 0), movieKey(3, 30), showKey(5, 1)} {
			it := candItem{t: h.cat.titles[k], fit: 1}
			if v := a.tuneItem(&it); math.Abs(v) > 0.26 {
				t.Fatalf("tuneItem %v = %v", tu, v)
			}
		}
	}
	// Defaults change nothing.
	a.tune = nil
	it := candItem{t: h.cat.titles[movieKey(0, 0)], fit: 0.5}
	if a.tuneItem(&it) != 0 || defaultTuning.rowMult(leanExplore|leanFresh) != 1 || defaultTuning.lambda() != mmrLambda {
		t.Fatal("default dials must be neutral")
	}
}

func TestResolveTuningPrecedence(t *testing.T) {
	d := core.HomeLayout{
		Global: []core.LayoutPage{{Section: "movies", Tuning: &core.LayoutTuning{Discovery: floatPtr(0.9), Freshness: floatPtr(0.2)}}},
		Viewers: []core.ViewerLayout{{UserID: 3, Pages: []core.LayoutPage{{Section: "movies",
			Tuning: &core.LayoutTuning{Freshness: floatPtr(0.7), Variety: floatPtr(0)}}}}},
	}
	got := resolveTuning(d, 3, "movies")
	if got != (tuning{0.9, 0.7, 0}) {
		t.Fatalf("viewer → global → default: %+v", got)
	}
	if got := resolveTuning(d, 4, "movies"); got != (tuning{0.9, 0.2, 0.5}) {
		t.Fatalf("global only: %+v", got)
	}
	if got := resolveTuning(d, 3, "home"); got != defaultTuning {
		t.Fatalf("other page: %+v", got)
	}
	if !resolveDedupeHome(d, 3, "movies") || resolveDedupeHome(d, 3, "home") {
		t.Fatal("DedupeHome defaults: on for sections, never Home")
	}
	// The dials key the memo.
	if resolveTuning(d, 3, "movies").key() == resolveTuning(d, 4, "movies").key() {
		t.Fatal("tuning memo key")
	}
}

func TestLayoutValidatesDedupeAndTuning(t *testing.T) {
	bad := core.HomeLayout{
		Global: []core.LayoutPage{
			{Section: "home", DedupeHome: boolPtr(false)},
			{Section: "movies", Tuning: &core.LayoutTuning{Discovery: floatPtr(1.5), Variety: floatPtr(2)}},
		},
		Viewers: []core.ViewerLayout{{UserID: 2, Pages: []core.LayoutPage{{Section: "shows", Tuning: &core.LayoutTuning{Freshness: floatPtr(-0.1)}}}}},
	}
	_, err := normalizeHomeLayout(bad)
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	want := map[string]bool{"layout.global[0].dedupeHome": true, "layout.global[1].tuning.discovery": true,
		"layout.global[1].tuning.variety": true, "layout.viewers[0].pages[0].tuning.freshness": true}
	for _, p := range ve.Problems {
		delete(want, p.Field)
	}
	if len(want) > 0 {
		t.Fatalf("missing problems %v in %v", want, ve.Problems)
	}
	// NaN (which JSON cannot carry) is rejected too.
	if _, err := normalizeHomeLayout(core.HomeLayout{Global: []core.LayoutPage{{Section: "shows", Tuning: &core.LayoutTuning{Variety: floatPtr(math.NaN())}}}}); err == nil {
		t.Fatal("NaN tuning accepted")
	}
	good := core.HomeLayout{
		Global:  []core.LayoutPage{{Section: "movies", DedupeHome: boolPtr(false), Tuning: &core.LayoutTuning{Discovery: floatPtr(0), Variety: floatPtr(1)}}, {Section: "shows", Tuning: &core.LayoutTuning{}}},
		Viewers: []core.ViewerLayout{{UserID: 2, Pages: []core.LayoutPage{{Section: "home", Tuning: &core.LayoutTuning{Freshness: floatPtr(0.8)}}}}},
	}
	d, err := normalizeHomeLayout(good)
	if err != nil {
		t.Fatal(err)
	}
	if *d.Global[0].DedupeHome || *d.Global[0].Tuning.Variety != 1 || d.Global[1].Tuning != nil || *d.Viewers[0].Pages[0].Tuning.Freshness != 0.8 {
		t.Fatalf("normalized = %+v", d)
	}
	// It persists through the engine.
	h := newHarness(t, nil)
	saved, err := h.e.SetHomeLayout(context.Background(), good)
	if err != nil || saved.Global[0].DedupeHome == nil || *saved.Global[0].Tuning.Discovery != 0 {
		t.Fatalf("saved = %+v %v", saved, err)
	}
}

func TestAvailableRowsSectionsAndGroups(t *testing.T) {
	h := newHarness(t, nil)
	h.e.SetExtraHomeRows(func(_ context.Context, _ *core.User, _ core.HubContext, section string, _ int) []core.Hub {
		out := []core.Hub{{ID: "watchlist.coming", Title: "Waiting for a stream"}}
		if section == "home" {
			out = append(out, core.Hub{ID: "live.onnow", Title: "On Now"})
		}
		return out
	})
	rows, err := h.e.AvailableRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]core.RowDescriptor{}
	for _, r := range rows {
		if len(r.Sections) == 0 || r.Group == "" {
			t.Errorf("%s lacks sections/group: %+v", r.HubID, r)
		}
		by[r.HubID] = r
	}
	has := func(id string, secs ...string) {
		t.Helper()
		got := strings.Join(by[id].Sections, ",")
		if got != strings.Join(secs, ",") {
			t.Errorf("%s sections = %q, want %q", id, got, strings.Join(secs, ","))
		}
	}
	has(KindRecentlyAdded, "movies", "shows") // never offered for Home
	has(KindPlaylists, "home")
	has(KindNewSeasons, "home", "shows")
	has(KindEra, "home", "movies")
	has(KindTop10, "home", "movies", "shows")
	has(KindTopRated, "home", "movies", "shows")
	has("watchlist.coming", "home", "movies", "shows")
	has("live.onnow", "home")
	for id, group := range map[string]string{KindContinue: groupPlex, KindTop10: groupPicks, KindTasteCluster: groupExplore,
		KindBinge: groupShows, KindPopularServer: groupHousehold, "live.onnow": groupPlex} {
		if by[id].Group != group {
			t.Errorf("%s group = %q, want %q", id, by[id].Group, group)
		}
	}
	for _, k := range kinds {
		if _, ok := by[k.kind]; !ok {
			t.Errorf("kind %s not offered", k.kind)
		}
	}
}

func TestOwnerPolicyPlacesLensOnHome(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	u := core.User{ID: 1, Username: "mia", IsOwner: true}
	h.users.users = []core.User{u}
	sciFiFan(h, u.ID)
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	d := core.HomeLayout{Global: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{{Row: KindFranchise, Status: "eligible"}, {Row: KindTasteCluster, Status: "always", Slot: 3}}}}}
	p, err := h.e.PreviewLayout(ctx, &u, hc, "home", 12, d)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowByKind(p.Rows, KindTasteCluster); r == nil || r.Hidden || r.Slot != 3 {
		t.Fatalf("an owner can place a lens on Home:\n%s", describe(p.Rows))
	}
	if rowByKind(p.Rows, KindFranchise) == nil {
		t.Fatalf("an eligible policy admits the franchise row on Home: %v", kindsOf(p.Rows))
	}
	// A row that does not fit a page stays off it whatever the policy.
	d = core.HomeLayout{Global: []core.LayoutPage{{Section: "movies", Rows: []core.RowPolicy{{Row: KindAiringToday, Status: "always"}}}}}
	p, _ = h.e.PreviewLayout(ctx, &u, hc, "movies", 12, d)
	if r := rowByKind(p.Rows, KindAiringToday); r != nil && len(r.Items) > 0 {
		t.Fatalf("Airing Today on Movies: %+v", r)
	}
}

func TestLensHubIDsParse(t *testing.T) {
	for id, want := range map[string]string{
		"movie.cluster.thriller.heist":            KindTasteCluster,
		"tv.cluster.series-crime.drama":           KindTasteCluster,
		"movie.era.1990":                          KindEra,
		"movie.world.ko":                          KindWorld,
		"movie.occasion.date-night":               KindOccasion,
		"movie.acclaimed":                         KindAcclaimed,
		"movie.toprated.genre.thriller":           KindTopGenre,
		"movie.toprated":                          KindTopRated,
		"tv.newseasons":                           KindNewSeasons,
		"tv.finish":                               KindFinishShow,
		"tv.startnew":                             KindStartNew,
		"tv.binge":                                KindBinge,
		"tv.limited":                              KindLimited,
		"tv.airing.taste":                         KindAiringTaste,
		"tv.airing":                               KindAiringToday,
		"tv.network.summit-channel":               KindNetwork,
		"tv.person.42":                            KindPerson,
		"movie.because.1000000000001002":          KindBecause,
		"movie.franchise.9001":                    KindFranchise,
		"tv.inprogress.2":                         KindContinue,
		"movie.recentlyadded.1":                   KindRecentlyAdded,
		"home.top10":                              KindTop10,
		"movie.toprated.genre.series-documentary": KindTopGenre,
	} {
		if got := parseHubKind(id); got != want {
			t.Errorf("parseHubKind(%q) = %q, want %q", id, got, want)
		}
	}
	if hubKind("movie.era.1990") != KindEra || hubKind("watchlist.coming") != "watchlist.coming" {
		t.Fatal("hubKind falls back to the id scheme for engine rows only")
	}
}
