package personalize

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func (h *harness) flushServes() {
	h.t.Helper()
	h.e.rk.writer.flush(context.Background())
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestServeLogCapturesAssemblyFeatures(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1, Username: "alice"}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	hubs, err := h.e.HomeCandidatesFor(ctx, &u, hc, 12)
	if err != nil || len(hubs) == 0 {
		t.Fatalf("home = %v %v", hubIDsOf(hubs), err)
	}
	h.e.RecordImpressions(&u, hc, hubs)
	h.flushServes()

	var surface, device, daypart string
	if err := h.db.QueryRow(`SELECT surface, device, daypart FROM personalize_serves`).Scan(&surface, &device, &daypart); err != nil {
		t.Fatal(err)
	}
	if surface != "home" || device != core.DeviceTV || daypart != hc.Daypart {
		t.Fatalf("serve = %s %s %s", surface, device, daypart)
	}
	n := h.count(`SELECT COUNT(*) FROM personalize_serve_items`)
	if n == 0 || n > 8*12 {
		t.Fatalf("logged items = %d, want 1..96", n)
	}
	if bad := h.count(`SELECT COUNT(*) FROM personalize_serve_items WHERE row_pos >= 8 OR item_pos >= 12 OR kind IN (?, ?) OR label != -1 OR fv != ?`,
		KindContinue, KindUpNext, rawFeatureVersion); bad != 0 {
		t.Fatalf("%d items outside the logged window, wrong kind or label", bad)
	}
	// Every snapshot decodes, and matches what assembly knew: a Science
	// Fiction title in this fan's Top 10 has a positive taste fit and a
	// non-zero baseline score.
	rows, err := h.db.Query(`SELECT item_key, hub_id, kind, baseline, features FROM personalize_serve_items`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sawTop := false
	for rows.Next() {
		var key int64
		var hub, kind string
		var baseline float64
		var blob []byte
		if err := rows.Scan(&key, &hub, &kind, &baseline, &blob); err != nil {
			t.Fatal(err)
		}
		var raw [numRaw]float32
		if !decodeFeatures(blob, raw[:]) {
			t.Fatalf("snapshot of %d does not decode (%d bytes)", key, len(blob))
		}
		ti := h.cat.titles[core.RatingKey(key)]
		if hub == "home.top10" && overlap(normGenres(ti.Genres), []string{"Science Fiction"}) {
			sawTop = true
			if raw[fFit] <= 0 || baseline == 0 || raw[fQuality] <= 0 {
				t.Fatalf("top-10 snapshot of %s: fit %.2f quality %.2f baseline %.2f", ti.Title, raw[fFit], raw[fQuality], baseline)
			}
			if raw[fNewToViewer] != 1 || raw[fSinceShown] < 4 {
				t.Fatalf("never-shown title: new %.0f since %.2f", raw[fNewToViewer], raw[fSinceShown])
			}
		}
	}
	if !sawTop {
		t.Fatal("no Science Fiction Top 10 item logged")
	}

	// The same page polled again within the window is the same serve.
	hc2 := hc
	hc2.Now = testNow.Add(5 * time.Minute)
	h.e.RecordImpressions(&u, hc2, hubs)
	h.flushServes()
	var serves, repeats int
	var last int64
	if err := h.db.QueryRow(`SELECT COUNT(*), MAX(repeats), MAX(last_at) FROM personalize_serves`).Scan(&serves, &repeats, &last); err != nil {
		t.Fatal(err)
	}
	if serves != 1 || repeats != 2 || last != ms(hc2.Now) {
		t.Fatalf("repeat poll: serves %d repeats %d last %v", serves, repeats, fromMS(last))
	}
	// After the window it is a new serve, and the viewer's exposure moved.
	hc3 := hc
	hc3.Now = testNow.Add(45 * time.Minute)
	h.e.RecordImpressions(&u, hc3, hubs)
	h.flushServes()
	if n := h.count(`SELECT COUNT(*) FROM personalize_serves`); n != 2 {
		t.Fatalf("serves after window = %d", n)
	}
	h.e.rk.mu.Lock()
	ux := h.e.rk.expo[1]
	h.e.rk.mu.Unlock()
	if ux == nil || ux.serves != 2 {
		t.Fatalf("exposure = %+v", ux)
	}

	// A page whose rows were not assembled by the engine is not logged.
	h.e.RecordImpressions(&core.User{ID: 99}, hc, hubs)
	h.flushServes()
	if n := h.count(`SELECT COUNT(*) FROM personalize_serves WHERE user_id = 99`); n != 0 {
		t.Fatalf("unassembled page logged %d serves", n)
	}
}

func TestServeLogSectionsAndDailyCap(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Ranker.MaxServesPerDay = 2 })
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	hc.SectionID = 1
	for i := 0; i < 4; i++ {
		hc.Now = testNow.Add(time.Duration(i) * 35 * time.Minute)
		hubs, err := h.e.SectionCandidatesFor(ctx, &u, core.TypeMovie, hc, 12)
		if err != nil || len(hubs) == 0 {
			t.Fatalf("movies = %v %v", hubIDsOf(hubs), err)
		}
		h.e.RecordImpressions(&u, hc, hubs)
	}
	h.flushServes()
	if n := h.count(`SELECT COUNT(*) FROM personalize_serves WHERE surface = 'movies'`); n != 2 {
		t.Fatalf("movies serves = %d, want the daily cap of 2", n)
	}
}

func TestServeLogRetention(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	rs := h.e.rk
	logAt := func(at time.Time, items int) {
		rec := &serveRec{userID: 1, at: at, surface: "home", hc: hubContext(at, core.DeviceTV)}
		for i := 0; i < items; i++ {
			rec.items = append(rec.items, serveItem{key: movieKey(0, i), hubID: "home.top10", kind: KindTop10, itemPos: i})
		}
		rs.logServe(rec, uint64(at.UnixNano()), dayOf(at, time.UTC))
	}
	logAt(testNow.AddDate(0, 0, -70), 3)
	logAt(testNow.AddDate(0, 0, -10), 4)
	logAt(testNow.AddDate(0, 0, -5), 4)
	logAt(testNow.AddDate(0, 0, -1), 4)
	h.flushServes()
	n, err := purgeServes(ctx, h.db, testNow, 60*24*time.Hour, 0)
	if err != nil || n != 4 {
		t.Fatalf("retention removed %d %v, want 3 items + 1 serve", n, err)
	}
	// The size cap drops the oldest pages first.
	if _, err := purgeServes(ctx, h.db, testNow, 60*24*time.Hour, 6); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT COUNT(*) FROM personalize_serve_items`); n > 6 || n == 0 {
		t.Fatalf("items after cap = %d", n)
	}
	var oldest int64
	if err := h.db.QueryRow(`SELECT MIN(first_at) FROM personalize_serves`).Scan(&oldest); err != nil || fromMS(oldest).Before(testNow.AddDate(0, 0, -5)) {
		t.Fatalf("oldest serve after cap = %v %v", fromMS(oldest), err)
	}
}

// logServeAt logs a serve of keys in one row directly (attribution tests).
func logServeAt(h *harness, uid int64, at time.Time, hub, surface string, keys ...core.RatingKey) {
	h.t.Helper()
	rec := &serveRec{userID: uid, at: at, surface: surface, hc: hubContext(at, core.DeviceTV)}
	for i, k := range keys {
		rec.items = append(rec.items, serveItem{key: k, hubID: hub, kind: parseHubKind(hub), rowPos: 1, itemPos: i})
	}
	h.e.rk.logServe(rec, uint64(at.UnixNano())^uint64(len(hub)), dayOf(at, time.UTC))
}

func labelOf(h *harness, hub string, key core.RatingKey) int {
	h.t.Helper()
	return h.count(`SELECT label FROM personalize_serve_items WHERE hub_id = ? AND item_key = ?`, hub, int64(key))
}

func TestServeAttribution(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	x, y, z := movieKey(1, 3), movieKey(1, 4), movieKey(2, 5)
	logServeAt(h, 1, testNow, "home.top10", "home", x, y)
	logServeAt(h, 1, testNow.Add(time.Hour), "home.because.42", "home", x, z)
	logServeAt(h, 2, testNow.Add(time.Hour), "home.top10", "home", x) // another viewer

	// Viewer 1 finishes x three hours later: the most recent serve with x
	// is the positive; the earlier serve of x is superseded, not negative.
	h.finish(1, x, testNow.Add(3*time.Hour))
	h.e.syncEvents(ctx)
	if l := labelOf(h, "home.because.42", x); l != labelPlayed {
		t.Fatalf("latest serve label = %d", l)
	}
	if l := h.count(`SELECT label FROM personalize_serve_items WHERE user_id = 1 AND hub_id = 'home.top10' AND item_key = ?`, int64(x)); l != labelSuperseded {
		t.Fatalf("earlier serve label = %d", l)
	}
	if n := h.count(`SELECT COUNT(*) FROM personalize_serve_items WHERE user_id = 2 AND label != -1`); n != 0 {
		t.Fatal("another viewer's serve was labelled")
	}
	// Nothing else is negative until the 48h window closes.
	if n, _ := finalizeNegatives(ctx, h.db, testNow.Add(40*time.Hour)); n != 0 {
		t.Fatalf("finalised %d before the window closed", n)
	}
	if n, _ := finalizeNegatives(ctx, h.db, testNow.Add(50*time.Hour)); n != 3 {
		t.Fatalf("finalised %d after the window, want y, z and viewer 2's x", n)
	}
	if labelOf(h, "home.top10", y) != labelNegative {
		t.Fatal("unplayed item is not negative")
	}
	// A play after the window labels nothing.
	h.finish(1, y, testNow.Add(60*time.Hour))
	h.e.syncEvents(ctx)
	if labelOf(h, "home.top10", y) != labelNegative {
		t.Fatal("play outside the window relabelled a serve")
	}
	// The completed play is a full-reward positive in the training set.
	xs, _, err := loadExamples(ctx, h.db, 1000)
	if err != nil {
		t.Fatal(err)
	}
	pos := 0
	for _, ex := range xs {
		if ex.y > 0 {
			pos++
			if ex.y != 1 || ex.kind != KindBecause {
				t.Fatalf("positive example = %+v", ex)
			}
		}
	}
	if pos != 1 || len(xs) != 4 {
		t.Fatalf("examples = %d, positives = %d", len(xs), pos)
	}
}

func TestServeAttributionPrefersAttributedRow(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	x := movieKey(3, 2)
	logServeAt(h, 1, testNow, "movie.top10", "movies", x)
	logServeAt(h, 1, testNow.Add(time.Hour), "home.trending", "home", x)
	logServeAt(h, 1, testNow.Add(2*time.Hour), "home.hiddengems", "home", x)
	h.flushServes()
	// The play was attributed to the Movies Top 10 row: that serve wins
	// although later serves also showed the title.
	ok, err := labelServe(ctx, h.db, 1, x, testNow.Add(-time.Hour), testNow.Add(3*time.Hour), "movie.top10", 77)
	if err != nil || !ok {
		t.Fatalf("label = %v %v", ok, err)
	}
	if labelOf(h, "movie.top10", x) != labelPlayed || h.count(`SELECT play_id FROM personalize_serve_items WHERE hub_id = 'movie.top10'`) != 77 {
		t.Fatal("attributed row not chosen")
	}
	// A second play of the same title (resume) does not relabel.
	if ok, _ := labelServe(ctx, h.db, 1, x, testNow.Add(-time.Hour), testNow.Add(4*time.Hour), "home.trending", 78); ok {
		t.Fatal("resume relabelled")
	}
	// With an attributed hub on another surface only, the surface decides.
	y := movieKey(3, 3)
	logServeAt(h, 1, testNow, "home.popular", "home", y)
	logServeAt(h, 1, testNow.Add(time.Hour), "movie.toprated", "movies", y)
	h.flushServes()
	if ok, err := labelServe(ctx, h.db, 1, y, testNow.Add(-time.Hour), testNow.Add(3*time.Hour), "home.top10", 79); err != nil || !ok {
		t.Fatal(err)
	}
	if labelOf(h, "home.popular", y) != labelPlayed {
		t.Fatal("surface preference not applied")
	}
}
