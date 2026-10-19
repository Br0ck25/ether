package personalize

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func items(cat *fakeCatalog, keys ...core.RatingKey) []candItem {
	var out []candItem
	for i, k := range keys {
		t := cat.titles[k]
		out = append(out, candItem{t: t, base: 1 - 0.01*float64(i), fit: 0.5, qual: quality(&t), reason: "r"})
	}
	return out
}

func testRow(kind, id string, its []candItem) *candRow {
	r := newRow(kind, id, id, "because", its)
	return r
}

func emptyModel(rows ...*candRow) *userModel {
	return &userModel{taste: buildTaste(tasteInput{now: testNow, loc: time.UTC}), rows: rows, fatigue: map[core.RatingKey]float64{}}
}

func run(m *userModel, mut func(a *assembly)) []core.HomeRow {
	a := &assembly{model: m, user: core.User{ID: 1}, hc: hubContext(testNow, core.DeviceTV), limit: 6,
		global: map[string]arm{}, explore: 0, day: dayOf(testNow, time.UTC), maxRows: 14, explain: true}
	if mut != nil {
		mut(a)
	}
	return a.run()
}

func TestAssemblyDedupesAcrossRows(t *testing.T) {
	cat := newFakeCatalog(testNow)
	shared := []core.RatingKey{movieKey(0, 1), movieKey(0, 2), movieKey(0, 3)}
	top := testRow(KindTop10, "top10", items(cat, append(shared, movieKey(0, 4), movieKey(0, 5), movieKey(0, 6))...))
	because := testRow(KindBecause, "because.1", items(cat, append(shared, movieKey(1, 1), movieKey(1, 2), movieKey(1, 3), movieKey(1, 4), movieKey(1, 5))...))
	trend := testRow(KindTrending, "trending", items(cat, append(shared, movieKey(2, 1), movieKey(2, 2), movieKey(2, 3), movieKey(2, 4))...))
	rows := run(emptyModel(top, because, trend), nil)
	seen := map[core.RatingKey]string{}
	for _, r := range rows {
		for _, it := range r.Items {
			if prev, dup := seen[it.Key]; dup {
				t.Fatalf("%s appears in %s and %s", it.Title, prev, r.HubID)
			}
			seen[it.Key] = r.HubID
		}
	}
	if len(rows) != 4 || rows[3].Kind != KindContinue { // the engine always places Continue Watching on Home
		t.Fatalf("rows = %v", kindsOf(rows))
	}
	// The shared titles stay in the highest row.
	if rows[0].Kind != KindTop10 {
		t.Fatalf("Top 10 should outrank: %v", kindsOf(rows))
	}
	for _, k := range shared {
		if seen[k] != rows[0].HubID {
			t.Fatalf("shared title %d in %s, want the top row", k, seen[k])
		}
	}
	// A row that dedupe leaves too thin is dropped.
	thin := testRow(KindHiddenGems, "hiddengems", items(cat, append(shared, movieKey(3, 1))...))
	thin.minItems = 4
	rows = run(emptyModel(top, thin), nil)
	if r := rowByKind(rows, KindHiddenGems); r != nil && !r.Hidden {
		t.Fatalf("thin row kept: %v", kindsOf(rows))
	}
}

func TestAssemblyMMRDiversifies(t *testing.T) {
	cat := newFakeCatalog(testNow)
	// Six saga films score highest, then six varied titles just below.
	var its []candItem
	for j := 4; j < 8; j++ {
		t := cat.titles[movieKey(0, j)]
		its = append(its, candItem{t: t, base: 1, fit: 0.8, qual: 0.6})
	}
	for g := 1; g < 7; g++ {
		t := cat.titles[movieKey(g, 1)]
		its = append(its, candItem{t: t, base: 0.9, fit: 0.6, qual: 0.6})
	}
	out := mmr(its, 6, mmrLambda)
	saga := 0
	for _, it := range out {
		if it.t.CollectionID == sagaCollection {
			saga++
		}
	}
	if saga == 0 || saga > 3 {
		t.Fatalf("MMR picked %d of the franchise in 6 slots: %v", saga, out)
	}
	if out[0].t.CollectionID != sagaCollection {
		t.Fatal("MMR should still lead with the most relevant item")
	}
	// Deterministic.
	again := mmr(its, 6, mmrLambda)
	for i := range out {
		if out[i].t.Key != again[i].t.Key {
			t.Fatal("MMR not deterministic")
		}
	}
	// λ = 1 is plain relevance order.
	plain := mmr(its, 6, 1)
	for i := 0; i < 4; i++ {
		if plain[i].t.CollectionID != sagaCollection {
			t.Fatal("λ=1 should not diversify")
		}
	}
}

func TestAssemblyGlobalPolicies(t *testing.T) {
	cat := newFakeCatalog(testNow)
	mk := func(kind, id string, g int) *candRow {
		var keys []core.RatingKey
		for j := 0; j < 6; j++ {
			keys = append(keys, movieKey(g, j))
		}
		return testRow(kind, id, items(cat, keys...))
	}
	m := emptyModel(mk(KindTop10, "top10", 0), mk(KindTrending, "trending", 1), mk(KindNewReleases, "newreleases", 2),
		mk(KindComfort, "comfort", 5), mk(KindBecause, "because.1", 4))
	base := run(m, nil)
	if len(kindsOf(base)) != 6 { // five rows plus Continue Watching, which the engine always places on Home
		t.Fatalf("base rows = %v", kindsOf(base))
	}
	for _, r := range base {
		if !r.Hidden && (r.Layer != core.LayerPersonalized || r.Slot == 0 || r.Placement == "") {
			t.Fatalf("default provenance = %+v", r)
		}
	}
	doc := &core.HomeLayout{Global: []core.LayoutPage{{Section: "home", MaxRows: 2, Rows: []core.RowPolicy{
		{Row: KindNewReleases, Status: core.RowAlways, Slot: 1},
		{Row: "home.top10", Priority: 0.3},
		{Row: "because.*", Status: core.RowOff},
		{Row: KindComfort, Status: core.RowEligible, When: &core.LayoutWhen{Days: []int{0, 6}}}, // weekends only; testNow is a Wednesday
	}}}}
	rows := run(m, func(a *assembly) { a.layout = mustIndex(t, doc) })
	visible := kindsOf(rows)
	if len(visible) != 3 || visible[0] != KindNewReleases || !rows[0].Pinned || rows[0].Layer != core.LayerGlobal || rows[0].Slot != 1 ||
		rows[0].AnchorSlot != 1 || !strings.Contains(rows[0].Placement, "Anchored to slot 1 in Global Home") {
		t.Fatalf("anchored first, 2 engine rows + Continue Watching: %v %+v", visible, rows[0])
	}
	for _, k := range visible {
		if k == KindTop10 {
			t.Fatalf("deprioritised row survived the page length ahead of others: %v", visible)
		}
	}
	comfort := rowByKind(rows, KindComfort)
	if comfort == nil || !comfort.Hidden || !strings.Contains(comfort.HiddenReason, "Scheduled for Weekends in Global Home") || comfort.Layer != core.LayerGlobal {
		t.Fatalf("out-of-schedule row: %+v", comfort)
	}
	because := rowByKind(rows, KindBecause)
	if because == nil || !because.Hidden || because.HiddenReason != "Off in Global Home." || because.Policy == nil || because.PolicyLayer != core.LayerGlobal {
		t.Fatalf("off row: %+v", because)
	}
	top := rowByKind(rows, KindTop10)
	if top == nil || !top.Hidden || !strings.Contains(top.HiddenReason, "Didn't make the page: 2 rows at most (in Global Home)") {
		t.Fatalf("cut row: %+v", top)
	}
	for i, r := range rows {
		if r.Hidden && i < len(visible) || !r.Hidden && r.Slot != i+1 {
			t.Fatalf("visible rows first with slots 1…n, hidden last: %d %+v", i, r)
		}
	}
	// Priority lifts a low row.
	stats := map[string]arm{KindNewReleases: {shows: 100, reward: 3}, KindTop10: {shows: 100, reward: 6}}
	boosted := run(m, func(a *assembly) {
		a.layout = mustIndex(t, &core.HomeLayout{Global: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{{Row: KindNewReleases, Priority: 3}}}}})
		a.global = stats
	})
	plain := run(m, func(a *assembly) { a.global = stats })
	if slotOf(boosted, KindNewReleases) >= slotOf(plain, KindNewReleases) {
		t.Fatalf("priority did not raise the row: %d vs %d", slotOf(boosted, KindNewReleases), slotOf(plain, KindNewReleases))
	}
	if r := rowByKind(boosted, KindNewReleases); !strings.Contains(r.Placement, "priority ×3 in Global Home") {
		t.Fatalf("placement = %q", r.Placement)
	}
}

func TestAssemblyRankingLearnsFromEngagement(t *testing.T) {
	cat := newFakeCatalog(testNow)
	mk := func(kind, id string, g int) *candRow {
		var keys []core.RatingKey
		for j := 0; j < 6; j++ {
			keys = append(keys, movieKey(g, j))
		}
		return testRow(kind, id, items(cat, keys...))
	}
	m := emptyModel(mk(KindTop10, "top10", 0), mk(KindHiddenGems, "hiddengems", 1))
	before := run(m, nil)
	if before[0].Kind != KindTop10 {
		t.Fatalf("prior order = %v", kindsOf(before))
	}
	// This viewer ignores Top 10 and plays hidden gems constantly.
	after := run(m, func(a *assembly) {
		a.arms = map[string]arm{KindTop10: {shows: 60, reward: 0}, KindHiddenGems: {shows: 60, reward: 30}}
	})
	if after[0].Kind != KindHiddenGems {
		t.Fatalf("learned order = %v (scores %v / %v)", kindsOf(after), after[0].Score, after[1].Score)
	}
}

func TestAssemblyExplorationRotatesDaily(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var rows []*candRow
	for i := 0; i < 6; i++ {
		var keys []core.RatingKey
		for j := 0; j < 6; j++ {
			keys = append(keys, movieKey(i, j))
		}
		r := testRow(KindBecause, fmt.Sprintf("because.%d", i), items(cat, keys...))
		rows = append(rows, r)
	}
	m := emptyModel(rows...)
	picks := map[string]bool{}
	for d := 0; d < 10; d++ {
		day := testNow.AddDate(0, 0, d)
		out := run(m, func(a *assembly) {
			a.explore = 0.5
			a.hc = hubContext(day, core.DeviceTV)
			a.day = dayOf(day, time.UTC)
		})
		if got := countKind(out, KindBecause); got != 3 {
			t.Fatalf("because rows capped at 3, got %d", got)
		}
		key := ""
		for _, r := range out {
			key += r.HubID + ";"
		}
		picks[key] = true
		// Stable within a day.
		again := run(m, func(a *assembly) {
			a.explore = 0.5
			a.hc = hubContext(day.Add(time.Hour), core.DeviceTV)
			a.day = dayOf(day, time.UTC)
		})
		for i := range out {
			if out[i].HubID != again[i].HubID {
				t.Fatal("Home changed within a day")
			}
		}
	}
	if len(picks) < 3 {
		t.Fatalf("only %d distinct Homes over 10 days: rotation too weak", len(picks))
	}
}

func TestAssemblyRespectsViewerLimits(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var keys []core.RatingKey
	for g := 0; g < 6; g++ { // includes Horror (R), Family (G)
		keys = append(keys, movieKey(g, 1), showKey(g, 1))
	}
	m := emptyModel(testRow(KindTrending, "trending", items(cat, keys...)))
	rows := run(m, func(a *assembly) {
		a.user = core.User{ID: 2, MaxContentRating: "PG"}
		a.limit = 20
	})
	for _, r := range rows {
		for _, it := range r.Items {
			if it.ContentRating == "R" || it.ContentRating == "TV-MA" || it.ContentRating == "PG-13" || it.ContentRating == "TV-14" {
				t.Errorf("%s (%s) shown to a PG viewer", it.Title, it.ContentRating)
			}
		}
	}
	// Allowed sections: a viewer without the TV library gets no shows.
	rows = run(m, func(a *assembly) {
		a.allowed = func(t core.MediaType) bool { return t == core.TypeMovie }
		a.limit = 20
	})
	for _, r := range rows {
		for _, it := range r.Items {
			if it.Type != core.TypeMovie {
				t.Errorf("%s shown without access to its section", it.Title)
			}
		}
	}
	// Sections only get their own type, with section hub ids.
	rows = run(m, func(a *assembly) { a.section = core.TypeShow; a.limit = 20 })
	if len(rows) != 1 || rows[0].HubID != "tv.trending" || rows[0].Hub.Type != core.TypeShow || rows[0].Hub.Context != "hub.tv.trending" {
		t.Fatalf("section rows = %+v", rows)
	}
	for _, it := range rows[0].Items {
		if it.Type != core.TypeShow {
			t.Fatalf("movie in the TV section: %s", it.Title)
		}
	}
}

func TestAssemblyTop10AndContinue(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var keys []core.RatingKey
	for j := 0; j < 14; j++ {
		keys = append(keys, movieKey(0, j))
	}
	top := testRow(KindTop10, "top10", items(cat, keys...))
	top.maxItems = 10
	cw := testRow(KindContinue, "continue", nil)
	cw.keepOrder, cw.homeOnly, cw.minItems, cw.source = true, true, 0, core.RowSourcePlex
	m := emptyModel(top, cw)
	rows := run(m, func(a *assembly) {
		a.limit = 20
		a.cw = items(cat, movieKey(1, 1), movieKey(1, 2))
	})
	tr := rowByKind(rows, KindTop10)
	if tr == nil || len(tr.Items) != 10 || !tr.Hub.More {
		t.Fatalf("Top 10 = %+v", tr)
	}
	c := rowByKind(rows, KindContinue)
	if c == nil || c.HubID != "home.continue" || len(c.Items) != 2 || c.Source != core.RowSourcePlex {
		t.Fatalf("continue row = %+v", c)
	}
	hubs := toHubs(rows)
	for _, h := range hubs {
		if h.ID == "home.continue" && len(h.Items) != 0 {
			t.Fatal("the Continue Watching placeholder must carry no items")
		}
	}
	// Empty Continue Watching still marks a position (the front end shows
	// its own row there) but ranks last.
	rows = run(m, func(a *assembly) { a.limit = 20 })
	if last := rows[len(rows)-1]; last.Kind != KindContinue {
		t.Fatalf("empty continue should rank last: %v", kindsOf(rows))
	}
	// Sections never get the engine's Continue Watching.
	rows = run(m, func(a *assembly) { a.section = core.TypeMovie; a.cw = items(cat, movieKey(1, 1)) })
	if rowByKind(rows, KindContinue) != nil {
		t.Fatal("continue row in a section")
	}
}

func TestAssemblyContextShapesRows(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var short, long []core.RatingKey
	for g := 0; g < 8; g++ {
		short = append(short, movieKey(g, 3)) // 98-minute films
		long = append(long, movieKey(g, 13))  // 158-minute films
	}
	// 24-minute sitcom and cartoon episodes, 8 per series.
	short = append(short, showKey(2, 0), showKey(2, 1), showKey(4, 0), showKey(4, 1))
	sot := testRow(KindShortOnTime, "shortontime", items(cat, append(short, long...)...))
	films := testRow(KindNewReleases, "newreleases", items(cat, long...))
	m := emptyModel(sot, films)
	score := func(device string, at time.Time) (float64, float64, *core.HomeRow) {
		rows := run(m, func(a *assembly) { a.hc = hubContext(at, device); a.limit = 20 })
		s, f := 0.0, 0.0
		var sr *core.HomeRow
		for i := range rows {
			switch rows[i].Kind {
			case KindShortOnTime:
				s, sr = rows[i].Score, &rows[i]
			case KindNewReleases:
				f = rows[i].Score
			}
		}
		return s, f, sr
	}
	weeknightPhone := time.Date(2026, 10, 14, 22, 30, 0, 0, time.UTC) // Wednesday, late
	saturdayEvening := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	s1, f1, row := score(core.DeviceMobile, weeknightPhone)
	s2, f2, _ := score(core.DeviceTV, saturdayEvening)
	if s1/f1 <= s2/f2 {
		t.Fatalf("Short on Time should matter more on a weeknight phone (%v/%v) than a Saturday-night TV (%v/%v)", s1, f1, s2, f2)
	}
	for i, it := range row.Items {
		if it.Runtime > shortMovieMinutes*time.Minute {
			t.Fatalf("%s (%v) in Short on Time", it.Title, it.Runtime)
		}
		// On a phone late at night the episodes lead.
		if i < 4 && it.Runtime > 30*time.Minute {
			t.Fatalf("%s (%v) leads Short on Time on a phone at 22:30: %v", it.Title, it.Runtime, row.Items)
		}
	}
}

func TestIsShort(t *testing.T) {
	cases := []struct {
		name string
		t    core.Title
		want bool
	}{
		{"95-minute film", core.Title{Type: core.TypeMovie, Runtime: 95 * time.Minute}, true},
		{"2-hour film", core.Title{Type: core.TypeMovie, Runtime: 120 * time.Minute}, false},
		{"unknown runtime", core.Title{Type: core.TypeMovie}, false},
		{"8-episode half-hour series", core.Title{Type: core.TypeShow, Runtime: 30 * time.Minute, LeafCount: 8, Genres: []string{"Drama"}}, true},
		{"sitcom", core.Title{Type: core.TypeShow, Runtime: 22 * time.Minute, LeafCount: 186, Genres: []string{"Comedy"}}, true},
		{"1000-episode anime", core.Title{Type: core.TypeShow, Runtime: 24 * time.Minute, LeafCount: 1180, Genres: []string{"Animation", "Action & Adventure", "Comedy"}}, false},
		{"long anime", core.Title{Type: core.TypeShow, Runtime: 24 * time.Minute, LeafCount: 196, Genres: []string{"Animation", "Comedy"}}, false},
		{"hour-long drama", core.Title{Type: core.TypeShow, Runtime: 55 * time.Minute, LeafCount: 8}, false},
		{"unknown episode count", core.Title{Type: core.TypeShow, Runtime: 25 * time.Minute}, false},
		{"daily news", core.Title{Type: core.TypeShow, Runtime: 30 * time.Minute, LeafCount: 20, Genres: []string{"News"}}, false},
	}
	for _, c := range cases {
		if got := isShort(&c.t); got != c.want {
			t.Errorf("%s: isShort = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLayoutCannotHidePlexRows(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var keys []core.RatingKey
	for j := 0; j < 6; j++ {
		keys = append(keys, movieKey(2, j))
	}
	cw := testRow(KindContinue, "continue", nil)
	cw.keepOrder, cw.homeOnly, cw.minItems, cw.source = true, true, 0, core.RowSourcePlex
	m := emptyModel(cw, testRow(KindTrending, "trending", items(cat, keys...)), testRow(KindTop10, "top10", items(cat, movieKey(3, 1), movieKey(3, 2), movieKey(3, 3), movieKey(3, 4))))
	// Validation refuses to turn Plex's row off; assembly ignores it too.
	off := &layoutIndex{global: map[string]*core.LayoutPage{"home": {Section: "home", Rows: []core.RowPolicy{{Row: KindContinue, Status: core.RowOff}}}}}
	rows := run(m, func(a *assembly) {
		a.cw = items(cat, movieKey(1, 1))
		a.layout = off
	})
	if r := rowByKind(rows, KindContinue); r == nil || r.Hidden {
		t.Fatalf("Continue Watching hidden: %+v", r)
	}
	// It can be bounded: never above slot 3.
	rows = run(m, func(a *assembly) {
		a.cw = items(cat, movieKey(1, 1))
		a.layout = mustIndex(t, &core.HomeLayout{Global: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{{Row: KindContinue, MinSlot: 3}}}}})
	})
	if r := rowByKind(rows, KindContinue); r.Slot != 3 || !strings.Contains(r.Placement, "never above slot 3") {
		t.Fatalf("bounded Continue Watching: %v %+v", kindsOf(rows), r)
	}
}
