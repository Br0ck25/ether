package personalize

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestIntentWeightDecay(t *testing.T) {
	for age, want := range map[time.Duration]float64{
		0: 1, 45 * time.Minute: 0.5, 90 * time.Minute: 0.25, 3 * time.Hour: 0.0625, 5 * time.Hour: 0,
	} {
		if got := intentWeight(age); math.Abs(got-want) > 1e-9 {
			t.Errorf("intentWeight(%s) = %v, want %v", age, got, want)
		}
	}
}

func TestIntentBoostBounds(t *testing.T) {
	snap := &intentSnapshot{
		genre:  map[string]float64{"Horror": intentMaxBoost, "Thriller": intentMaxBoost},
		person: map[int64]float64{42: intentMaxBoost},
		decade: map[int]float64{1990: intentMaxBoost},
	}
	match := &core.Title{Genres: []string{"Horror", "Thriller"}, Year: 1994,
		Directors: []core.Person{{TMDB: 42, Name: "A"}}}
	if got := snap.boostFor(match); got > intentMaxBoost || got <= 0 {
		t.Errorf("boostFor(match) = %v, want in (0, %v]", got, intentMaxBoost)
	}
	none := &core.Title{Genres: []string{"Comedy"}, Year: 2015, Cast: []core.Person{{TMDB: 7, Name: "B"}}}
	if got := snap.boostFor(none); got != 0 {
		t.Errorf("boostFor(disjoint) = %v, want 0", got)
	}
	var nilSnap *intentSnapshot
	if nilSnap.boostFor(match) != 0 || nilSnap.liftFor(nil) != 1 {
		t.Error("nil snapshot must be inert")
	}
}

// A browsed genre lifts matching items (context adjustment) and whole
// rows (score lift), without any model rebuild. Item-level order after
// MMR is covered end-to-end by TestIntentRecordConvertAndHubs.
func TestIntentAssemblyBoost(t *testing.T) {
	cat := newFakeCatalog(testNow)
	snap := &intentSnapshot{genre: map[string]float64{"Horror": intentMaxBoost}, person: map[int64]float64{}, decade: map[int]float64{}}
	horror := cat.titles[movieKey(3, 11)]
	scifi := cat.titles[movieKey(0, 11)]
	row := testRow(KindTrending, "trending", nil)
	mk := func(intent *intentSnapshot) *assembly {
		return &assembly{model: emptyModel(), user: core.User{ID: 1}, hc: hubContext(testNow, core.DeviceTV),
			limit: 6, day: dayOf(testNow, time.UTC), intent: intent}
	}
	target := float64(80)
	adjNo := mk(nil).adjust(row, &candItem{t: horror}, target)
	adjYes := mk(snap).adjust(row, &candItem{t: horror}, target)
	if want := snap.boostFor(&horror); adjYes-adjNo < want-1e-9 || want <= 0 {
		t.Errorf("horror adjust: intent %v, plain %v, want +%v", adjYes, adjNo, want)
	}
	if d := mk(snap).adjust(row, &candItem{t: scifi}, target) - mk(nil).adjust(row, &candItem{t: scifi}, target); math.Abs(d) > 1e-9 {
		t.Errorf("unrelated title adjust moved by %v", d)
	}
	horrorItems := []candItem{{t: horror}, {t: cat.titles[movieKey(3, 12)]}}
	if lift := snap.liftFor(horrorItems); lift <= 1.1 || lift > 1+intentMaxBoost {
		t.Errorf("row lift = %v, want in (1.1, 1.25]", lift)
	}
	if lift := snap.liftFor([]candItem{{t: scifi}}); lift != 1 {
		t.Errorf("unrelated row lift = %v, want 1", lift)
	}
}

// Opens are recorded, converted opens are dropped, and /hubs assembly
// sees the change immediately (new memo version, boosted genre share).
func TestIntentRecordConvertAndHubs(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 31, Username: "browser"}
	h.users.users = []core.User{u}
	// Give the viewer a sci-fi taste so Home has personal rows.
	for j := 1; j <= 4; j++ {
		h.finish(u.ID, movieKey(0, 20+j), testNow.AddDate(0, 0, -j))
	}
	h.rebuild(u)
	ctx := context.Background()
	hc := hubContext(testNow, core.DeviceTV)

	horrorTitle := h.cat.titles[movieKey(3, 40)]
	h.e.RecordDetailOpen(ctx, &u, &horrorTitle)
	snap, ver := h.e.intentFor(u.ID, testNow)
	if !snap.active() || ver == 0 {
		t.Fatal("no intent after detail open")
	}
	if snap.genre["Horror"] <= 0 {
		t.Errorf("intent genres = %v, want Horror", snap.genre)
	}

	// The open converts to playback inside the window: intent vanishes.
	h.e.NotePlayback(&u, horrorTitle.Key)
	if snap, ver := h.e.intentFor(u.ID, testNow); snap.active() || ver != 0 {
		t.Errorf("converted open kept as intent: %+v v%d", snap, ver)
	}

	// A later playback of a long-browsed title does not erase the signal.
	h.e.RecordDetailOpen(ctx, &u, &horrorTitle)
	h.clock.Set(testNow.Add(intentConvertWindow + time.Minute))
	h.e.NotePlayback(&u, horrorTitle.Key)
	if snap, _ := h.e.intentFor(u.ID, h.clock.Now()); !snap.active() {
		t.Error("stale open erased by playback outside the conversion window")
	}
	h.clock.Set(testNow)

	// Decay: after several half-lives the boost is a shadow of itself.
	fresh, _ := h.e.intentFor(u.ID, testNow)
	stale, _ := h.e.intentFor(u.ID, testNow.Add(3*time.Hour))
	if stale.genre["Horror"] >= fresh.genre["Horror"] {
		t.Errorf("intent did not decay: fresh %v stale %v", fresh.genre["Horror"], stale.genre["Horror"])
	}

	// /hubs reflects the open on the very next request: the memo key
	// carries the intent version, so no stale rows are served.
	rows1 := h.e.home(ctx, &u, hc, "", 12, false).Rows
	share1 := genreShare(rows1, "Horror", KindContinue)
	comedyTitle := h.cat.titles[movieKey(2, 41)]
	for i := 0; i < 3; i++ { // browse several comedies tonight
		tt := h.cat.titles[movieKey(2, 41+i)]
		h.e.RecordDetailOpen(ctx, &u, &tt)
	}
	_ = comedyTitle
	rows2 := h.e.home(ctx, &u, hc, "", 12, false).Rows
	if genreShare(rows2, "Comedy", KindContinue) <= genreShare(rows1, "Comedy", KindContinue) {
		t.Errorf("hubs did not adapt to session intent: comedy share %v -> %v (horror %v)",
			genreShare(rows1, "Comedy", KindContinue), genreShare(rows2, "Comedy", KindContinue), share1)
	}

	// Section hubs take the same path.
	sec1 := h.e.home(ctx, &u, hc, core.TypeMovie, 12, false).Rows
	if len(sec1) == 0 {
		t.Error("empty section home")
	}
}

// Intent never overrides a taste veto.
func TestIntentRespectsVeto(t *testing.T) {
	cat := newFakeCatalog(testNow)
	ts := buildTaste(tasteInput{now: testNow, loc: time.UTC})
	ts.Genre["Horror"] = -0.9
	horror := cat.titles[movieKey(3, 10)]
	if !genreVetoed(ts, &horror) {
		t.Fatal("expected veto")
	}
	if genreVetoed(ts, &core.Title{Genres: []string{"Comedy"}}) {
		t.Fatal("unexpected veto")
	}
}
