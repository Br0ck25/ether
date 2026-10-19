package personalize

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func mustIndex(t *testing.T, doc *core.HomeLayout) *layoutIndex {
	t.Helper()
	norm, err := normalizeHomeLayout(doc)
	if err != nil {
		t.Fatal(err)
	}
	return newLayoutIndex(norm)
}

func slotOf(rows []core.HomeRow, kind string) int {
	for _, r := range rows {
		if r.Kind == kind && !r.Hidden {
			return r.Slot
		}
	}
	return 0
}

func home(rows ...core.RowPolicy) []core.LayoutPage {
	return []core.LayoutPage{{Section: "home", Rows: rows}}
}

func problemFields(t *testing.T, err error) map[string]bool {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	out := map[string]bool{}
	for _, f := range ve.InvalidFields() {
		out[f[0]] = true
	}
	return out
}

func TestNormalizeHomeLayoutValidates(t *testing.T) {
	_, err := normalizeHomeLayout(&core.HomeLayout{
		Global: []core.LayoutPage{
			{Section: "kitchen"},
			{Section: "home", MaxRows: 99, DeviceMaxRows: map[string]int{"fridge": 3, "mobile": -1}, Rows: []core.RowPolicy{
				{Row: ""},
				{Row: "[", Status: "sometimes"},
				{Row: KindTop10, Slot: 2, MinSlot: 3},
				{Row: KindTrending, Slot: 7},
				{Row: KindHiddenGems, MinSlot: 6, MaxSlot: 2, Priority: 9, MaxPerPage: 40, MaxDaysPerWeek: 9},
				{Row: KindTop10Today, Status: core.RowOff, Slot: 4},
				{Row: KindContinue, Status: core.RowOff, When: &core.LayoutWhen{Days: []int{1}}},
				{Row: KindRecentlyAdded},
				{Row: KindComfort, When: &core.LayoutWhen{Days: []int{8}, Dayparts: []string{"brunch"}, Months: []int{13}, StartDate: "soon", EndDate: "2026-01-01"}},
				{Row: KindComfort},
				{Row: KindPopular, Slot: 7},
			}},
			{Section: "home"},
			{Section: "movies", Rows: []core.RowPolicy{{Row: KindContinue, Slot: 1}}},
		},
		Viewers: []core.ViewerLayout{{UserID: 0}, {UserID: 3}, {UserID: 3}},
	})
	fields := problemFields(t, err)
	for _, want := range []string{
		"global[0].section", "global[1].maxRows", "global[1].deviceMaxRows", "global[1].deviceMaxRows.mobile",
		"global[1].rows[0].row", "global[1].rows[1].row", "global[1].rows[1].status", "global[1].rows[2].slot",
		"global[1].rows[10].slot", "global[1].rows[4].minSlot", "global[1].rows[4].priority", "global[1].rows[4].maxPerPage",
		"global[1].rows[4].maxDaysPerWeek", "global[1].rows[5].slot", "global[1].rows[6].status", "global[1].rows[6].when",
		"global[1].rows[7].row", "global[1].rows[8].when.days", "global[1].rows[8].when.dayparts", "global[1].rows[8].when.months",
		"global[1].rows[8].when.startDate", "global[1].rows[9].row", "global[2].section", "global[3].rows[0].row",
		"viewers[0].userId", "viewers[2].userId",
	} {
		if !fields[want] {
			t.Errorf("missing problem for %s (got %v)", want, fields)
		}
	}
	_, err = normalizeHomeLayout(&core.HomeLayout{Global: home(core.RowPolicy{Row: KindComfort, When: &core.LayoutWhen{StartDate: "2026-12-24", EndDate: "2026-12-01"}})})
	if !problemFields(t, err)["global[0].rows[0].when.endDate"] {
		t.Fatal("end before start accepted")
	}

	got, err := normalizeHomeLayout(&core.HomeLayout{
		Global: []core.LayoutPage{
			{Section: "movies", MaxRows: 8},
			{Section: "", DeviceMaxRows: map[string]int{"mobile": 6, "tv": 0}, Rows: []core.RowPolicy{
				{Row: KindTrending},
				{Row: KindTop10, Slot: 3},
				{Row: KindContinue, Slot: 1, Status: core.RowEligible},
				{Row: KindComfort, Priority: 1, MaxDaysPerWeek: 7, When: &core.LayoutWhen{Days: []int{6, 0, 6}}},
				{Row: KindShortOnTime, When: &core.LayoutWhen{}},
			}},
			{Section: "shows"},
		},
		Viewers: []core.ViewerLayout{
			{UserID: 9, Pages: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{{Row: KindTrending, Status: core.RowOff}}}}},
			{UserID: 4, Pages: []core.LayoutPage{{Section: "home"}}},
			{UserID: 2, Pages: []core.LayoutPage{{Section: "home", MaxRows: 5}}},
		},
		Notes: []string{"old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Global) != 2 || got.Global[0].Section != "home" || got.Global[1].Section != "movies" {
		t.Fatalf("pages in canonical order, empty ones dropped: %+v", got.Global)
	}
	g := got.Global[0]
	if len(g.DeviceMaxRows) != 1 || g.DeviceMaxRows["mobile"] != 6 {
		t.Fatalf("device lengths = %v", g.DeviceMaxRows)
	}
	if g.Rows[0].Row != KindContinue || g.Rows[0].Status != core.RowAlways || g.Rows[1].Row != KindTop10 || g.Rows[1].Status != core.RowAlways ||
		g.Rows[2].Row != KindTrending || g.Rows[2].Status != core.RowEligible {
		t.Fatalf("anchors first by slot, statuses filled: %+v", g.Rows)
	}
	if c := g.Rows[3]; c.Priority != 0 || c.MaxDaysPerWeek != 0 || len(c.When.Days) != 2 || c.When.Days[0] != 0 || g.Rows[4].When != nil {
		t.Fatalf("neutral values cleared, schedules tidied: %+v %+v", c, g.Rows[4])
	}
	if len(got.Viewers) != 2 || got.Viewers[0].UserID != 2 || got.Viewers[1].UserID != 9 || got.Notes != nil {
		t.Fatalf("viewers sorted, empty layers dropped, notes cleared: %+v", got)
	}
}

func TestPolicyResolution(t *testing.T) {
	ix := mustIndex(t, &core.HomeLayout{
		Global: []core.LayoutPage{{Section: "home", MaxRows: 12, DeviceMaxRows: map[string]int{"mobile": 8}, Rows: []core.RowPolicy{
			{Row: "because.*", Status: core.RowOff},
			{Row: KindBecause, MaxPerPage: 2},
			{Row: "because.42", Status: core.RowAlways},
			{Row: KindTop10, Slot: 2},
		}}},
		Viewers: []core.ViewerLayout{
			{UserID: 5, Pages: []core.LayoutPage{{Section: "home", DeviceMaxRows: map[string]int{"tv": 6}, Rows: []core.RowPolicy{{Row: "because.*", Status: core.RowAlways}}}}},
			{UserID: 6, Pages: []core.LayoutPage{{Section: "home", MaxRows: 9}}},
		},
	})
	everyone := ix.page(1, "home")
	// Within a layer: hub id beats kind beats glob.
	if p := everyone.policyFor("home.because.42", KindBecause); p.p.Row != "because.42" || p.layer != core.LayerGlobal {
		t.Fatalf("hub id match = %+v", p)
	}
	if p := everyone.policyFor("home.because.7", KindBecause); p.p.Row != KindBecause {
		t.Fatalf("kind match = %+v", p)
	}
	if p := everyone.policyFor("home.trending", KindTrending); p.ok() {
		t.Fatalf("no policy = %+v", p)
	}
	// A viewer's override beats any Global policy, even a more specific one.
	if p := ix.page(5, "home").policyFor("home.because.42", KindBecause); p.p.Row != "because.*" || p.layer != core.LayerOverride {
		t.Fatalf("override = %+v", p)
	}
	// Other sections are untouched.
	if p := ix.page(5, "movies").policyFor("movie.top10", KindTop10); p.ok() {
		t.Fatalf("home policy leaked into movies: %+v", p)
	}
	// Page length: viewer device, viewer page, Global device, Global page, default.
	for _, c := range []struct {
		user   int64
		device string
		want   int
		layer  string
	}{
		{5, "tv", 6, core.LayerOverride}, {5, "mobile", 8, core.LayerGlobal}, {6, "mobile", 9, core.LayerOverride},
		{1, "mobile", 8, core.LayerGlobal}, {1, "web", 12, core.LayerGlobal},
	} {
		if n, layer := ix.page(c.user, "home").maxRows(c.device, 14); n != c.want || layer != c.layer {
			t.Errorf("user %d on %s: %d (%s), want %d (%s)", c.user, c.device, n, layer, c.want, c.layer)
		}
	}
	if n, layer := ix.page(1, "shows").maxRows("tv", 10); n != 10 || layer != "" {
		t.Fatalf("default page length = %d %q", n, layer)
	}
	var nilIx *layoutIndex
	if p := nilIx.page(1, "home").policyFor("home.top10", KindTop10); p.ok() {
		t.Fatal("nil layout has policies")
	}
}

func TestWhenMatchesCalendar(t *testing.T) {
	dec := core.NewHubContext(time.Date(2026, 12, 20, 9, 0, 0, 0, time.UTC), "Plex for Roku", "Roku", "") // a Sunday morning
	oct := core.NewHubContext(time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC), "", "", "")
	for _, c := range []struct {
		w        core.LayoutWhen
		dec, oct bool
	}{
		{core.LayoutWhen{Months: []int{12}}, true, false},
		{core.LayoutWhen{Dayparts: []string{core.DaypartMorning}}, true, false},
		{core.LayoutWhen{Dayparts: []string{core.DaypartEvening, core.DaypartLate}}, false, true},
		{core.LayoutWhen{StartDate: "2026-12-01", EndDate: "2026-12-26"}, true, false},
		{core.LayoutWhen{StartDate: "2026-12-21"}, false, false},
		{core.LayoutWhen{EndDate: "2026-10-14"}, false, true},
		{core.LayoutWhen{Months: []int{12}, Devices: []string{core.DeviceTV}, Days: []int{0}}, true, false},
	} {
		w := c.w
		if got := whenMatches(&w, dec); got != c.dec {
			t.Errorf("%+v in December: %v", w, got)
		}
		if got := whenMatches(&w, oct); got != c.oct {
			t.Errorf("%+v in October: %v", w, got)
		}
	}
	if got := describeWhen(&core.LayoutWhen{Days: []int{1, 2, 3, 4, 5}, FromHour: ip(18), ToHour: ip(23), Devices: []string{"tv"}, Months: []int{12}}); got != "Weekdays · 18:00–23:00 · tv · Dec" {
		t.Fatalf("describeWhen = %q", got)
	}
}

func TestPlaceSlots(t *testing.T) {
	slots := func(out []slotOutcome) []int {
		s := make([]int, len(out))
		for i, o := range out {
			s[i] = o.slot
		}
		return s
	}
	eq := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	// Rank order with no constraints.
	if got := slots(placeSlots(make([]slotReq, 4))); !eq(got, []int{1, 2, 3, 4}) {
		t.Fatalf("plain = %v", got)
	}
	// Anchors take their slots; the rest flow around them in rank order.
	out := placeSlots([]slotReq{{}, {}, {anchor: 1}, {}, {anchor: 3}})
	if got := slots(out); !eq(got, []int{2, 4, 1, 5, 3}) {
		t.Fatalf("anchors = %v", got)
	}
	// Two anchors on one slot: the override wins, the Global one moves down.
	out = placeSlots([]slotReq{{anchor: 2, pri: 1}, {anchor: 2, pri: 2}, {}})
	if got := slots(out); !eq(got, []int{3, 2, 1}) || !out[0].displaced || out[1].displaced {
		t.Fatalf("anchor conflict = %v %+v", got, out)
	}
	// An anchor beyond the page sits last.
	out = placeSlots([]slotReq{{}, {anchor: 9}, {}})
	if got := slots(out); !eq(got, []int{1, 3, 2}) || !out[1].clamped || out[1].displaced {
		t.Fatalf("clamped anchor = %v %+v", got, out)
	}
	// Never below slot 2 pulls a low-ranked row up; never above slot 3
	// holds a top-ranked row down.
	if got := slots(placeSlots([]slotReq{{}, {}, {}, {hi: 2}})); !eq(got, []int{1, 3, 4, 2}) {
		t.Fatalf("maxSlot = %v", got)
	}
	if got := slots(placeSlots([]slotReq{{lo: 3}, {}, {}, {}})); !eq(got, []int{3, 1, 2, 4}) {
		t.Fatalf("minSlot = %v", got)
	}
	// Greedy rank order would put row 0 at slot 1 and then miss both
	// deadlines; the feasibility check keeps every bound.
	out = placeSlots([]slotReq{{}, {hi: 2}, {lo: 2, hi: 2}, {anchor: 3}})
	if got := slots(out); !eq(got, []int{4, 1, 2, 3}) {
		t.Fatalf("feasible arrangement = %v %+v", got, out)
	}
	for _, o := range out {
		if o.missedHi || o.missedLo {
			t.Fatalf("bounds missed: %+v", out)
		}
	}
	// Impossible bounds are honoured as far as possible and reported.
	out = placeSlots([]slotReq{{hi: 1}, {hi: 1}, {}})
	if got := slots(out); !eq(got, []int{1, 2, 3}) || !out[1].missedHi || out[0].missedHi {
		t.Fatalf("infeasible = %v %+v", got, out)
	}
}

func TestConvertLegacyRules(t *testing.T) {
	rules := []core.LayoutRule{
		{Scope: core.LayoutScope{UserIDs: []int64{3, 4}}, Section: "home", MaxRows: 6, Rows: []core.LayoutRowRule{
			{HubID: KindShortOnTime, Action: core.LayoutPin, Position: ip(1)},
		}},
		{Scope: core.LayoutScope{All: true}, Section: "home", MaxRows: 12, Rows: []core.LayoutRowRule{
			{HubID: KindContinue, Action: core.LayoutPin, Position: ip(0)},
			{HubID: KindTop10, Action: core.LayoutPin, Position: ip(0)},
			{HubID: "because.*", Action: core.LayoutHide},
			{HubID: KindTrending, Action: core.LayoutBoost},
			{HubID: KindPopular, Action: core.LayoutBury},
			{HubID: KindComfort, Action: core.LayoutHide, When: &core.LayoutWhen{Days: []int{1, 2, 3, 4, 5}}},
			{HubID: KindHiddenGems, Action: core.LayoutHide, When: &core.LayoutWhen{FromHour: ip(22), ToHour: ip(6)}},
			{HubID: KindNewThisWeek, Action: core.LayoutBoost, When: &core.LayoutWhen{Devices: []string{"tv"}}},
			{HubID: KindDifferent, Action: core.LayoutHide, When: &core.LayoutWhen{Days: []int{0}, Devices: []string{"tv"}}},
			{HubID: KindPlaylists, Action: core.LayoutBury},
		}},
		{Scope: core.LayoutScope{All: true}, Section: "movies", Rows: []core.LayoutRowRule{{HubID: KindTop10, Action: core.LayoutHide}}},
	}
	doc, notes := convertLegacyRules(rules)
	norm, err := normalizeHomeLayout(doc)
	if err != nil {
		t.Fatal(err)
	}
	ix := newLayoutIndex(norm)
	g := ix.global["home"]
	if g.MaxRows != 12 || len(ix.global) != 2 {
		t.Fatalf("global pages = %+v", norm.Global)
	}
	pol := func(p *core.LayoutPage, row string) core.RowPolicy {
		t.Helper()
		for _, r := range p.Rows {
			if r.Row == row {
				return r
			}
		}
		t.Fatalf("no policy for %s in %+v", row, p.Rows)
		return core.RowPolicy{}
	}
	if p := pol(g, KindContinue); p.Slot != 1 || p.Status != core.RowAlways {
		t.Fatalf("pin at 0 = %+v", p)
	}
	if p := pol(g, KindTop10); p.Slot != 2 { // pinned to an occupied position: next free slot
		t.Fatalf("second pin at 0 = %+v", p)
	}
	if pol(g, "because.*").Status != core.RowOff || pol(g, KindTrending).Priority != 1.5 || pol(g, KindPopular).Priority != 0.3 {
		t.Fatalf("hide / boost / bury = %+v", g.Rows)
	}
	if p := pol(g, KindComfort); p.Status != core.RowEligible || len(p.When.Days) != 2 || p.When.Days[0] != 0 || p.When.Days[1] != 6 {
		t.Fatalf("weekday hide became a weekend schedule: %+v", p)
	}
	if p := pol(g, KindHiddenGems); *p.When.FromHour != 6 || *p.When.ToHour != 22 {
		t.Fatalf("overnight hide became a daytime schedule: %+v", p.When)
	}
	if p := pol(g, KindNewThisWeek); p.Priority != 1.5 || p.When != nil {
		t.Fatalf("scheduled boost = %+v", p)
	}
	if pol(g, KindDifferent).Status != core.RowOff {
		t.Fatal("two-dimension hide should be off at all times")
	}
	if pol(ix.global["movies"], KindTop10).Status != core.RowOff {
		t.Fatal("movies rule lost")
	}
	for _, uid := range []int64{3, 4} {
		v := ix.viewers[uid]["home"]
		if v == nil || v.MaxRows != 6 || pol(v, KindShortOnTime).Slot != 2 {
			t.Fatalf("viewer %d = %+v", uid, v)
		}
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"now applies at all times", "could not be expressed as a schedule", "occupied position", "Plex builds that row"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q:\n%s", want, joined)
		}
	}
}

// ---- assembly with the layered layout --------------------------------------------

func layeredModel(cat *fakeCatalog) *userModel {
	mk := func(kind, id string, g int) *candRow {
		var keys []core.RatingKey
		for j := 0; j < 6; j++ {
			keys = append(keys, movieKey(g, j))
		}
		return testRow(kind, id, items(cat, keys...))
	}
	b1, b2 := mk(KindBecause, "because.1", 4), mk(KindBecause, "because.2", 5)
	return emptyModel(mk(KindTop10, "top10", 0), mk(KindTrending, "trending", 1), mk(KindNewReleases, "newreleases", 2),
		mk(KindShortOnTime, "shortontime", 3), b1, b2)
}

func TestAssemblyOverridesBeatGlobal(t *testing.T) {
	cat := newFakeCatalog(testNow)
	m := layeredModel(cat)
	doc := &core.HomeLayout{
		Global: home(
			core.RowPolicy{Row: KindTrending, Slot: 1},
			core.RowPolicy{Row: KindShortOnTime, Status: core.RowOff},
		),
		Viewers: []core.ViewerLayout{{UserID: 1, Pages: home(
			core.RowPolicy{Row: KindTop10, Slot: 1},
			core.RowPolicy{Row: KindShortOnTime, Status: core.RowAlways},
		)}},
	}
	ix := mustIndex(t, doc)
	// Viewer 2 gets the Global template.
	rows := run(m, func(a *assembly) { a.layout, a.user = ix, core.User{ID: 2, Title: "Sam"} })
	if r := rowByKind(rows, KindTrending); r.Slot != 1 || r.Layer != core.LayerGlobal {
		t.Fatalf("global anchor for Sam: %+v", r)
	}
	if r := rowByKind(rows, KindShortOnTime); !r.Hidden || r.Layer != core.LayerGlobal || r.HiddenReason != "Off in Global Home." {
		t.Fatalf("global off for Sam: %+v", r)
	}
	// Viewer 1's overrides: Short on Time back on, Top 10 takes slot 1 and
	// the Global anchor yields to the next slot.
	rows = run(m, func(a *assembly) { a.layout, a.user = ix, core.User{ID: 1, Title: "Alex"} })
	st := rowByKind(rows, KindShortOnTime)
	if st.Hidden || st.Layer != core.LayerOverride || st.PolicyLayer != core.LayerOverride || !strings.Contains(st.Placement, "Always on the page for Alex (override)") {
		t.Fatalf("override always: %+v", st)
	}
	top := rowByKind(rows, KindTop10)
	if top.Slot != 1 || top.Layer != core.LayerOverride || top.Placement != "Anchored to slot 1 for Alex (override)." {
		t.Fatalf("override anchor: %+v", top)
	}
	tr := rowByKind(rows, KindTrending)
	if tr.Slot != 2 || tr.Layer != core.LayerGlobal || !strings.Contains(tr.Placement, "another anchor holds that slot, so it sits at slot 2") {
		t.Fatalf("displaced global anchor: %+v", tr)
	}
}

func TestAssemblyCapsAndFrequency(t *testing.T) {
	cat := newFakeCatalog(testNow)
	m := layeredModel(cat)
	// Two Because-you-watched rows by default; a policy caps them at one.
	rows := run(m, nil)
	if n := countKind(rows, KindBecause); n != 2 {
		t.Fatalf("default because rows = %d", n)
	}
	rows = run(m, func(a *assembly) {
		a.layout = mustIndex(t, &core.HomeLayout{Global: home(core.RowPolicy{Row: KindBecause, MaxPerPage: 1})})
	})
	if n := countKind(rows, KindBecause); n != 1 {
		t.Fatalf("capped because rows = %d", n)
	}
	var capped *core.HomeRow
	for i := range rows {
		if rows[i].Kind == KindBecause && rows[i].Hidden {
			capped = &rows[i]
		}
	}
	if capped == nil || !strings.Contains(capped.HiddenReason, "At most 1 “Because You Watched” row per page in Global Home") {
		t.Fatalf("capped row = %+v", capped)
	}

	// Frequency: at most 3 days a week.
	day := func(back int) int { return dayOf(testNow.AddDate(0, 0, -back), time.UTC) }
	freq := mustIndex(t, &core.HomeLayout{Global: home(core.RowPolicy{Row: KindShortOnTime, MaxDaysPerWeek: 3})})
	for _, c := range []struct {
		days  []int
		shown bool
	}{
		{nil, true},
		{[]int{day(1), day(2)}, true},
		{[]int{day(1), day(3), day(5)}, false},
		{[]int{day(1), day(3), day(5), day(0)}, true}, // already shown today: stays for the day
		{[]int{day(7), day(8), day(9)}, true},         // outside the week
	} {
		rows := run(m, func(a *assembly) {
			a.layout = freq
			a.days = map[string][]int{KindShortOnTime: c.days}
		})
		r := rowByKind(rows, KindShortOnTime)
		if r.Hidden == c.shown {
			t.Errorf("days %v: hidden=%v (%s)", c.days, r.Hidden, r.HiddenReason)
		}
		if r.Hidden && !strings.Contains(r.HiddenReason, "at most 3 days a week, and it was shown on 3 of the last 6 days") {
			t.Errorf("reason = %q", r.HiddenReason)
		}
	}
}

func countKind(rows []core.HomeRow, kind string) int {
	n := 0
	for _, r := range rows {
		if r.Kind == kind && !r.Hidden {
			n++
		}
	}
	return n
}

func TestAssemblyBoundsAndPageLength(t *testing.T) {
	cat := newFakeCatalog(testNow)
	m := layeredModel(cat)
	stats := map[string]arm{KindNewReleases: {shows: 400, reward: 2}, KindTop10: {shows: 100, reward: 40}}
	plain := run(m, func(a *assembly) { a.global = stats })
	if slotOf(plain, KindNewReleases) < 4 {
		t.Fatalf("new releases should rank low without a bound: %v", kindsOf(plain))
	}
	// Never below slot 2.
	rows := run(m, func(a *assembly) {
		a.global = stats
		a.layout = mustIndex(t, &core.HomeLayout{Global: home(core.RowPolicy{Row: KindNewReleases, MaxSlot: 2})})
	})
	nr := rowByKind(rows, KindNewReleases)
	if nr.Slot > 2 || !strings.Contains(nr.Placement, "never below slot 2 in Global Home") || nr.Layer != core.LayerPersonalized {
		t.Fatalf("bounded row: %+v", nr)
	}
	// Never above slot 4 on a 3-row page: an eligible row cannot be shown.
	rows = run(m, func(a *assembly) {
		a.global = stats
		a.layout = mustIndex(t, &core.HomeLayout{Global: []core.LayoutPage{{Section: "home", MaxRows: 2,
			Rows: []core.RowPolicy{{Row: KindTop10, MinSlot: 5}}}}})
	})
	top := rowByKind(rows, KindTop10)
	if !top.Hidden || !strings.Contains(top.HiddenReason, "Never above slot 5 in Global Home") {
		t.Fatalf("unplaceable row: %+v", top)
	}
	if n := len(kindsOf(rows)); n != 3 { // two engine rows plus Continue Watching
		t.Fatalf("page = %v", kindsOf(rows))
	}
	// Device page lengths.
	short := mustIndex(t, &core.HomeLayout{Global: []core.LayoutPage{{Section: "home", MaxRows: 5, DeviceMaxRows: map[string]int{core.DeviceMobile: 2}}}})
	if n := len(kindsOf(run(m, func(a *assembly) { a.layout = short; a.hc = hubContext(testNow, core.DeviceMobile) }))); n != 3 {
		t.Fatalf("mobile page = %d rows", n)
	}
	if n := len(kindsOf(run(m, func(a *assembly) { a.layout = short }))); n != 6 {
		t.Fatalf("tv page = %d rows", n)
	}
	// Always rows exceed a short page; eligible rows give way.
	always := mustIndex(t, &core.HomeLayout{Global: []core.LayoutPage{{Section: "home", MaxRows: 1, Rows: []core.RowPolicy{
		{Row: KindTrending, Status: core.RowAlways}, {Row: KindShortOnTime, Status: core.RowAlways}}}}})
	got := kindsOf(run(m, func(a *assembly) { a.layout = always }))
	if len(got) != 3 || rowByKind(run(m, func(a *assembly) { a.layout = always }), KindTop10).Hidden != true {
		t.Fatalf("always rows = %v", got)
	}
}

// ---- engine: persistence, migration, previews, impact ------------------------------

func TestLayoutMigratesLegacyRules(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO personalize_layout (id, rules, updated_at) VALUES (1, ?, 1)`,
		`[{"id":"r1","scope":"all","section":"home","rows":[{"hubId":"continue-watching","action":"pin","position":0},{"hubId":"trending","action":"hide"}],"maxRows":10},
		  {"id":"r2","scope":{"userIds":[7]},"section":"home","rows":[{"hubId":"trending","action":"pin","position":2}]}]`); err != nil {
		t.Fatal(err)
	}
	clk := newClock(testNow)
	open := func() *Engine {
		e, err := New(Options{DB: db, Catalog: newFakeCatalog(testNow), Logger: quietLogger(), Now: clk.Now, Location: time.UTC, RefreshEvery: -1})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	e := open()
	doc, _ := e.HomeLayout(ctx)
	e.Close()
	if len(doc.Global) != 1 || doc.Global[0].MaxRows != 10 || doc.Global[0].Rows[0].Row != KindContinue || doc.Global[0].Rows[0].Slot != 1 ||
		doc.Global[0].Rows[1].Status != core.RowOff || len(doc.Viewers) != 1 || doc.Viewers[0].UserID != 7 ||
		doc.Viewers[0].Pages[0].Rows[0].Slot != 3 || !doc.UpdatedAt.Equal(testNow) {
		t.Fatalf("converted = %+v", doc)
	}
	if len(doc.Notes) == 0 || !strings.Contains(doc.Notes[0], "Converted 2 layout rules") {
		t.Fatalf("notes = %v", doc.Notes)
	}
	// Converted once: a later start loads the saved document, notes and
	// all, and an owner save clears the notes.
	if _, err := db.Exec(`UPDATE personalize_layout SET rules = '[]'`); err != nil {
		t.Fatal(err)
	}
	clk.Set(testNow.Add(time.Hour))
	e = open()
	defer e.Close()
	again, _ := e.HomeLayout(ctx)
	if len(again.Global) != 1 || len(again.Notes) != len(doc.Notes) || !again.UpdatedAt.Equal(testNow) {
		t.Fatalf("reloaded = %+v", again)
	}
	saved, err := e.SetHomeLayout(ctx, again)
	if err != nil || saved.Notes != nil || !saved.UpdatedAt.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("save = %+v %v", saved, err)
	}
	if _, err := e.SetHomeLayout(ctx, &core.HomeLayout{Global: home(core.RowPolicy{Row: KindTop10, Slot: 99})}); err == nil {
		t.Fatal("invalid layout saved")
	}
	// The returned document is a copy.
	saved.Global[0].Rows[0].Row = "mutated"
	if cur, _ := e.HomeLayout(ctx); cur.Global[0].Rows[0].Row == "mutated" {
		t.Fatal("HomeLayout shares its document")
	}
	if conv, err := e.ConvertLayoutRules([]core.LayoutRule{{Rows: []core.LayoutRowRule{{HubID: KindTop10, Action: "pin"}}}}); err != nil || conv.Global[0].Rows[0].Slot != 1 {
		t.Fatalf("convert = %+v %v", conv, err)
	}
	if _, err := e.ConvertLayoutRules([]core.LayoutRule{{Rows: []core.LayoutRowRule{{HubID: KindTop10, Action: "explode"}}}}); err == nil {
		t.Fatal("invalid legacy rules converted")
	}
}

func TestPreviewLayoutDraftsAndHubsAgree(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1, Username: "alice", Title: "Alice"}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)

	saved, err := h.e.PreviewHome(ctx, &u, hc, "", 12)
	if err != nil {
		t.Fatal(err)
	}
	var trending string
	for _, r := range saved.Rows {
		if !r.Hidden && r.Source == core.RowSourceEngine && r.Kind != KindTop10 {
			trending = r.Kind
			break
		}
	}
	if trending == "" {
		t.Fatalf("no engine rows: %v", kindsOf(saved.Rows))
	}
	draft := &core.HomeLayout{Global: home(core.RowPolicy{Row: trending, Slot: 1}), Viewers: []core.ViewerLayout{{UserID: 1, Pages: home(core.RowPolicy{Row: KindTop10, Status: core.RowOff})}}}
	p, err := h.e.PreviewLayout(ctx, &u, hc, "", 12, draft)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowByKind(p.Rows, trending); r.Slot != 1 || r.Layer != core.LayerGlobal {
		t.Fatalf("draft anchor not previewed: %+v", r)
	}
	if r := rowByKind(p.Rows, KindTop10); r != nil && !r.Hidden {
		t.Fatalf("draft override not previewed: %+v", r)
	}
	// The draft is not saved: /hubs still serves the saved layout, and the
	// preview of the saved layout is exactly what /hubs serves.
	hubs, _ := h.e.HomeHubsFor(ctx, &u, hc, 12)
	again, _ := h.e.PreviewHome(ctx, &u, hc, "", 12)
	var want []string
	for _, r := range again.Rows {
		if !r.Hidden && r.HubID != "home.playlists" {
			want = append(want, r.HubID)
		}
	}
	if got := hubIDsOf(hubs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("/hubs %v != preview %v", got, want)
	}
	// Saving it changes /hubs, through the same path.
	if _, err := h.e.SetHomeLayout(ctx, draft); err != nil {
		t.Fatal(err)
	}
	hubs, _ = h.e.HomeHubsFor(ctx, &u, hc, 12)
	if len(hubs) == 0 || hubs[0].ID != rowByKind(p.Rows, trending).HubID {
		t.Fatalf("saved anchor not served first: %v", hubIDsOf(hubs))
	}
	for _, hb := range hubs {
		if hb.ID == "home.top10" {
			t.Fatal("override ignored by /hubs")
		}
	}
	// The typical viewer: the generic model, no viewer layer.
	typ, err := h.e.PreviewLayout(ctx, nil, hc, "", 12, draft)
	if err != nil {
		t.Fatal(err)
	}
	if len(kindsOf(typ.Rows)) < 3 {
		t.Fatalf("typical viewer rows = %v", kindsOf(typ.Rows))
	}
	for _, r := range typ.Rows {
		if r.Layer == core.LayerOverride {
			t.Fatalf("typical viewer got an override: %+v", r)
		}
	}
	if _, err := h.e.PreviewLayout(ctx, nil, hc, "", 12, &core.HomeLayout{Global: home(core.RowPolicy{Row: ""})}); err == nil {
		t.Fatal("invalid draft previewed")
	}
}

func TestLayoutImpact(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	sciFiFan(h, 1)
	sciFiFan(h, 2)
	users := []core.User{{ID: 1, Title: "Alice"}, {ID: 2, Username: "bob"}}
	for _, u := range users {
		h.rebuild(u)
	}
	hc := hubContext(testNow, core.DeviceTV)
	p, _ := h.e.PreviewHome(ctx, &users[0], hc, "", 12)
	var victim *core.HomeRow
	for i := range p.Rows {
		if !p.Rows[i].Hidden && p.Rows[i].Source == core.RowSourceEngine {
			victim = &p.Rows[i]
			break
		}
	}
	draft := &core.HomeLayout{
		Global:  home(core.RowPolicy{Row: victim.Kind, Status: core.RowOff}),
		Viewers: []core.ViewerLayout{{UserID: 2, Pages: home(core.RowPolicy{Row: victim.Kind, Status: core.RowEligible})}},
	}
	im, err := h.e.LayoutImpact(ctx, draft, hc, "", users)
	if err != nil {
		t.Fatal(err)
	}
	if len(im) != 2 || !im[0].Changed || len(im[0].Removed) == 0 || im[0].Overrides != 0 || im[0].Title != "Alice" {
		t.Fatalf("alice = %+v", im[0])
	}
	if im[1].Overrides != 1 || im[1].Title != "bob" || containsStr(im[1].Removed, victim.Title) {
		t.Fatalf("bob keeps the row through his override: %+v", im[1])
	}
	if _, err := h.e.LayoutImpact(ctx, &core.HomeLayout{Global: home(core.RowPolicy{Row: "["})}, hc, "", users); err == nil {
		t.Fatal("invalid draft accepted")
	}
}

// lookupCollections serves owner rows and, by id, collections placed in
// the Home Designer.
type lookupCollections struct {
	mu      sync.Mutex
	rows    []core.CollectionRow
	byID    map[string]core.CollectionRow
	lookups [][]string
	ignored []bool
}

func (f *lookupCollections) HomeRowsFor(context.Context, *core.User, core.HubContext) ([]core.CollectionRow, error) {
	return f.rows, nil
}

func (f *lookupCollections) CollectionRowsByID(_ context.Context, _ *core.User, _ core.HubContext, ids []string, ignoreAudience bool) ([]core.CollectionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, ids)
	f.ignored = append(f.ignored, ignoreAudience)
	var out []core.CollectionRow
	for _, id := range ids {
		if r, ok := f.byID[id]; ok {
			r.HomeRow, r.SectionRow = true, true
			out = append(out, r)
		}
	}
	return out, nil
}

func TestLayoutPlacesCollections(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var titles []core.Title
	for j := 0; j < 8; j++ {
		titles = append(titles, cat.titles[movieKey(6, j)])
	}
	coll := &lookupCollections{byID: map[string]core.CollectionRow{
		"7": {CollectionID: "7", Hub: core.Hub{ID: "collection.7", Title: "Holiday Classics", Items: titles}},
	}}
	h := newHarness(t, func(o *Options) { o.Collections = coll })
	ctx := context.Background()
	u := core.User{ID: 3, Title: "Kids"}
	hc := hubContext(testNow, core.DeviceTV)
	draft := &core.HomeLayout{Viewers: []core.ViewerLayout{{UserID: 3, Pages: home(core.RowPolicy{Row: "collection:7", Slot: 2})}}}
	p, err := h.e.PreviewLayout(ctx, &u, hc, "", 12, draft)
	if err != nil {
		t.Fatal(err)
	}
	r := rowByKind(p.Rows, "collection:7")
	if r == nil || r.Hidden || r.Slot != 2 || r.Layer != core.LayerOverride || !strings.Contains(r.Reason, "placed on this page in the Home Designer") {
		t.Fatalf("placed collection = %+v (rows %v)", r, kindsOf(p.Rows))
	}
	coll.mu.Lock()
	if len(coll.lookups) == 0 || coll.lookups[0][0] != "7" || !coll.ignored[0] {
		t.Fatalf("lookups = %v ignored %v", coll.lookups, coll.ignored)
	}
	coll.mu.Unlock()
	// Other viewers do not get it; a Global placement asks with the
	// audience respected.
	other := core.User{ID: 4}
	if p, _ := h.e.PreviewLayout(ctx, &other, hc, "", 12, draft); rowByKind(p.Rows, "collection:7") != nil {
		t.Fatal("per-viewer collection leaked to another viewer")
	}
	global := &core.HomeLayout{Global: home(core.RowPolicy{Row: "collection:7", Status: core.RowAlways})}
	if p, _ := h.e.PreviewLayout(ctx, &other, hc, "", 12, global); rowByKind(p.Rows, "collection:7") == nil {
		t.Fatal("global placement missing")
	}
	coll.mu.Lock()
	defer coll.mu.Unlock()
	if coll.ignored[len(coll.ignored)-1] {
		t.Fatal("a Global placement must respect the collection's audience")
	}
}
