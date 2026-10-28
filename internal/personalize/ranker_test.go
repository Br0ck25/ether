package personalize

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

var synthKinds = []string{KindTop10, KindBecause, KindTrending, KindHiddenGems, KindNewReleases}

// synthExamples generates n time-ordered labelled serve items with random
// features; prob returns each example's play probability.
func synthExamples(n int, seed int64, prob func(r *rand.Rand, x *rankExample) float64) []rankExample {
	r := rand.New(rand.NewSource(seed))
	xs := make([]rankExample, n)
	for i := range xs {
		x := &xs[i]
		x.at = testNow.Add(-time.Duration(n-i) * time.Minute)
		x.user = int64(1 + r.Intn(3))
		x.raw[fFit] = float32(r.Float64()*2 - 1)
		x.raw[fQuality] = float32(r.Float64())
		x.raw[fBase] = float32(r.Float64())
		x.raw[fPopularity] = float32(r.Float64())
		x.raw[fAge] = float32(r.Float64() * 9)
		x.raw[fRuntime] = float32(r.Float64()*2 - 1)
		x.raw[fFatigue] = float32(r.Float64())
		x.raw[fSinceShown] = float32(r.Float64() * 4)
		x.raw[fServerRate] = float32(0.01 + 0.02*r.Float64())
		if r.Float64() < 0.5 {
			x.raw[fMovie] = 1
		}
		if r.Float64() < 0.2 {
			x.raw[fTrending] = 1
		}
		x.kind = synthKinds[r.Intn(len(synthKinds))]
		x.surface = "home"
		x.rowPos, x.itemPos = r.Intn(8), r.Intn(12)
		x.baseline = r.Float64()
		if r.Float64() < prob(r, x) {
			x.y = 1
		}
	}
	return xs
}

// plantedProb: viewers play titles that fit their taste and that they have
// not been shown over and over.
func plantedProb(_ *rand.Rand, x *rankExample) float64 {
	return sigmoid(-3.2 + 2.4*float64(x.raw[fFit]) - 1.6*float64(x.raw[fFatigue]))
}

func weightOf(m *rankModel, name string) float64 {
	for i, n := range m.Names {
		if n == name {
			return m.Weights[i]
		}
	}
	return math.NaN()
}

func defaultRankerOpts() RankerOptions {
	var o RankerOptions
	o.defaults(time.Hour)
	return o
}

func TestRankerRecoversPlantedSignal(t *testing.T) {
	xs := synthExamples(8000, 1, plantedProb)
	m := fitRanker(xs, defaultRankerOpts(), testNow)
	if m == nil {
		t.Fatal("no model")
	}
	if m.AUC < 0.7 || math.Abs(m.BaselineAUC-0.5) > 0.08 {
		t.Fatalf("AUC %.3f baseline %.3f", m.AUC, m.BaselineAUC)
	}
	if !m.Active || m.Blend <= 0 || m.Blend > 0.35 || m.RowWeight <= 0 || m.RowWeight > rowLiftBound {
		t.Fatalf("active %v blend %.3f row %.3f", m.Active, m.Blend, m.RowWeight)
	}
	fit, fat := weightOf(m, "fit"), weightOf(m, "fatigue")
	if fit <= 0 || fat >= 0 {
		t.Fatalf("fit %.3f fatigue %.3f", fit, fat)
	}
	for i, n := range m.Names {
		if n != "fit" && n != "fatigue" && math.Abs(m.Weights[i]) > 0.6*math.Abs(fat) {
			t.Fatalf("noise feature %s weight %.3f vs fatigue %.3f", n, m.Weights[i], fat)
		}
	}
	// Calibration: the mean predicted probability tracks the play rate
	// despite class weighting.
	var pred, rate float64
	for i := range xs {
		pred += m.prob(xs[i].raw[:], xs[i].kind, xs[i].surface)
		rate += xs[i].y
	}
	pred, rate = pred/float64(len(xs)), rate/float64(len(xs))
	if math.Abs(pred-rate) > 0.5*rate {
		t.Fatalf("mean prediction %.4f vs play rate %.4f", pred, rate)
	}
	// Deterministic.
	m2 := fitRanker(synthExamples(8000, 1, plantedProb), defaultRankerOpts(), testNow)
	if m2.AUC != m.AUC || m2.Weights[0] != m.Weights[0] {
		t.Fatal("training is not deterministic")
	}
}

func TestRankerPositionDebiasing(t *testing.T) {
	// Plays depend only on where a title was shown, and the hand-tuned
	// ranking put higher-quality titles higher, so quality correlates
	// with plays without causing them.
	xs := synthExamples(20000, 3, func(r *rand.Rand, x *rankExample) float64 {
		x.raw[fQuality] = float32(clamp(1-float64(x.rowPos)/8+0.3*r.NormFloat64(), 0, 1))
		return sigmoid(-1.2 - 0.6*float64(x.rowPos) - 0.2*float64(x.itemPos))
	})
	m := fitRanker(xs, defaultRankerOpts(), testNow)
	if m == nil {
		t.Fatal("no model")
	}
	posRow, q := weightOf(m, posRowName(posRowSlots-1)), weightOf(m, "quality")
	if posRow >= 0 || math.Abs(q) > 0.15*math.Abs(posRow) {
		t.Fatalf("row position %.3f, quality %.3f: position leaked into quality", posRow, q)
	}
	// At inference positions are fixed: the model has nothing to rank by,
	// does not beat the baseline and stays inactive.
	if m.Active {
		t.Fatalf("position-only model activated (AUC %.3f, baseline %.3f)", m.AUC, m.BaselineAUC)
	}
	lo, hi := xs[0], xs[0]
	lo.raw[fQuality], hi.raw[fQuality] = 0.05, 0.95
	lo.rowPos, hi.rowPos = 7, 0
	d := math.Abs(m.modelScore(hi.raw[:], hi.kind, "home") - m.modelScore(lo.raw[:], lo.kind, "home"))
	if d > 0.1 {
		t.Fatalf("inference score moved %.3f with a position-correlated feature", d)
	}

	// Contrast: without debiasing (positions dropped), quality soaks up
	// the position effect.
	naive := make([]rankExample, len(xs))
	copy(naive, xs)
	for i := range naive {
		naive[i].rowPos, naive[i].itemPos = 0, 0
	}
	mn := fitRanker(naive, defaultRankerOpts(), testNow)
	if qn := weightOf(mn, "quality"); qn < 4*math.Abs(q) || qn < 0.2 {
		t.Fatalf("naive quality weight %.3f vs debiased %.3f", qn, q)
	}
	if dn := mn.modelScore(hi.raw[:], hi.kind, "home") - mn.modelScore(lo.raw[:], lo.kind, "home"); dn < 3*d {
		t.Fatalf("naive score gap %.3f vs debiased %.3f", dn, d)
	}
}

func TestRankerGating(t *testing.T) {
	o := defaultRankerOpts()
	// Too little data: inactive (or nothing to fit).
	if m := fitRanker(synthExamples(1200, 3, plantedProb), o, testNow); m != nil && m.Active {
		t.Fatalf("activated on 1200 examples (AUC %.3f)", m.AUC)
	}
	if m := fitRanker(synthExamples(50, 3, plantedProb), o, testNow); m != nil {
		t.Fatal("fitted on 50 examples")
	}
	// No signal: inactive.
	if m := fitRanker(synthExamples(8000, 4, func(*rand.Rand, *rankExample) float64 { return 0.05 }), o, testNow); m == nil || m.Active {
		t.Fatalf("noise model = %+v", m)
	}
	// The hand-tuned baseline already captures the signal: no lift, so
	// ranking stays hand-tuned.
	xs := synthExamples(8000, 5, func(r *rand.Rand, x *rankExample) float64 {
		p := plantedProb(r, x)
		x.baseline = p
		return p
	})
	m := fitRanker(xs, o, testNow)
	if m == nil || m.Active || m.AUC < 0.7 {
		t.Fatalf("no-lift model: %+v", m)
	}
	// Few positives: inactive.
	o2 := o
	o2.MinPositives = 100000
	if m := fitRanker(synthExamples(8000, 1, plantedProb), o2, testNow); m == nil || m.Active {
		t.Fatal("activated below MinPositives")
	}
}

func TestRankerBlendIsBounded(t *testing.T) {
	m := &rankModel{Active: true, Names: []string{"fit"}, Mean: []float64{0}, Std: []float64{1}, Weights: []float64{50},
		Blend: 0.35, RowWeight: rowLiftBound, LogitStd: 1}
	m.compile()
	var raw [numRaw]float32
	for _, fit := range []float32{-1, -0.2, 0, 0.3, 1} {
		raw[fFit] = fit
		for _, s := range []float64{-1, 0, 0.5, 1, 3} {
			d := m.itemDelta(raw[:], KindTop10, "home", s)
			if math.Abs(d) > maxItemDelta+1e-12 || math.Abs(d) > m.Blend+1e-12 {
				t.Fatalf("delta %.3f for fit %.1f score %.1f", d, fit, s)
			}
		}
	}
	for _, mean := range []float64{0, 0.25, 0.5, 1} {
		if l := m.rowLift(mean); l < 1-rowLiftBound-1e-12 || l > 1+rowLiftBound+1e-12 {
			t.Fatalf("row lift %.3f", l)
		}
	}
	if m.rowLift(0.5) != 1 {
		t.Fatal("typical row is not neutral")
	}
	raw[fFit] = 1
	hi := m.itemDelta(raw[:], KindTop10, "home", 0.5)
	raw[fFit] = -1
	lo := m.itemDelta(raw[:], KindTop10, "home", 0.5)
	if hi <= 0 || lo >= 0 {
		t.Fatalf("blend direction: hi %.3f lo %.3f", hi, lo)
	}
}

func TestRankerExplorationBonus(t *testing.T) {
	a := &assembly{user: core.User{ID: 1}, day: 20261014, explore: 0.5}
	ux := &userExpo{serves: 200, items: map[core.RatingKey]expo{}}
	p := &rankPass{expo: ux}
	under, over := movieKey(0, 1), movieKey(0, 2)
	ux.items[over] = expo{n: 150}
	_, qu := p.explore(a, under)
	_, qo := p.explore(a, over)
	if qu <= qo || qu > exploreMaxProb || qo <= 0 {
		t.Fatalf("propensity under-exposed %.3f, over-exposed %.3f", qu, qo)
	}
	// The bonus is applied with the logged propensity.
	applied := 0
	const n = 4000
	for i := 0; i < n; i++ {
		b, q := p.explore(a, core.MovieKey(int64(100000+i)))
		if q != qu {
			t.Fatalf("propensity %.3f vs %.3f", q, qu)
		}
		if b > 0 {
			applied++
			if b != exploreBonus {
				t.Fatalf("bonus %.3f", b)
			}
		}
	}
	if f := float64(applied) / n; math.Abs(f-qu) > 0.04 {
		t.Fatalf("applied share %.3f vs propensity %.3f", f, qu)
	}
	// Stable through the day.
	b1, _ := p.explore(a, under)
	b2, _ := p.explore(a, under)
	if b1 != b2 {
		t.Fatal("exploration is not stable within a day")
	}
	// Off for new viewers and when the owner disables exploration.
	if b, q := (&rankPass{expo: &userExpo{serves: 5, items: map[core.RatingKey]expo{}}}).explore(a, under); b != 0 || q != 0 {
		t.Fatal("explored with too few serves")
	}
	a.explore = 0
	if b, q := p.explore(a, under); b != 0 || q != 0 {
		t.Fatal("explored with exploration off")
	}
}

// insertExamples writes synthetic labelled serves into the serve log.
func insertExamples(t *testing.T, h *harness, xs []rankExample) {
	t.Helper()
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i, x := range xs {
		id := int64(i + 1)
		if _, err := tx.Exec(`INSERT INTO personalize_serves (id, user_id, first_at, last_at, surface) VALUES (?,?,?,?,?)`,
			id, x.user, ms(x.at), ms(x.at), x.surface); err != nil {
			t.Fatal(err)
		}
		label := labelNegative
		if x.y > 0 {
			label = labelPlayed
		}
		if _, err := tx.Exec(`INSERT INTO personalize_serve_items (serve_id, user_id, item_key, taste_key, hub_id, kind, row_pos, item_pos,
			baseline, fv, features, label) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, x.user, int64(movieKey(0, i%40)), int64(movieKey(0, i%40)), "home."+x.kind, x.kind, x.rowPos, x.itemPos,
			x.baseline, rawFeatureVersion, encodeFeatures(x.raw[:]), label); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRankerTrainPersistAndServe(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	in, err := h.e.RankerInsight(ctx)
	if err != nil || in.Active || in.TrainedAt != nil || in.Features == nil || len(in.Features) != 0 {
		t.Fatalf("untrained insight = %+v %v", in, err)
	}
	insertExamples(t, h, synthExamples(6000, 7, plantedProb))
	in, err = h.e.TrainRanker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Active || in.TrainedAt == nil || in.Examples != 6000 || in.Positives == 0 || in.AUC < 0.7 || in.AUC <= in.BaselineAUC {
		t.Fatalf("insight = %+v", in)
	}
	if len(in.Features) != len(designNames()) {
		t.Fatalf("features = %d", len(in.Features))
	}
	for i := 1; i < len(in.Features); i++ {
		if math.Abs(in.Features[i].Weight) > math.Abs(in.Features[i-1].Weight) {
			t.Fatal("features not sorted by |weight|")
		}
	}
	if top := in.Features[0]; top.Name != "fit" || top.Label != "Taste fit" || top.Weight <= 0 {
		t.Fatalf("top feature = %+v", top)
	}
	for _, f := range in.Features {
		if f.Label == "" || f.Label == f.Name {
			t.Fatalf("feature %q has no friendly label", f.Name)
		}
	}

	// A new engine on the same database loads the model.
	e2, err := New(Options{DB: h.db, Catalog: h.cat, Logger: quietLogger(), Now: h.clock.Now, Location: time.UTC, RefreshEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	in2, err := e2.RankerInsight(ctx)
	if err != nil || !in2.Active || in2.AUC != in.AUC || in2.Features[0] != in.Features[0] || !in2.TrainedAt.Equal(*in.TrainedAt) {
		t.Fatalf("reloaded insight = %+v %v", in2, err)
	}
	m1, m2 := h.e.rk.model.Load(), e2.rk.model.Load()
	var raw [numRaw]float32
	raw[fFit], raw[fFatigue] = 0.4, 0.2
	if m1.prob(raw[:], KindTop10, "home") != m2.prob(raw[:], KindTop10, "home") {
		t.Fatal("reloaded model scores differently")
	}

	// The active model reaches assembly, moves scores within bounds and
	// pages still assemble.
	sciFiFan(h, 1)
	u := core.User{ID: 1}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	p := h.e.newRankPass(u, hc, "", false)
	if p.model == nil {
		t.Fatal("active model not used by assembly")
	}
	rows := h.e.home(ctx, &u, hc, "", 12, false).Rows
	if len(rows) < 3 {
		t.Fatalf("rows with active model = %v", kindsOf(rows))
	}
}

func TestRankerInactiveLeavesRankingUnchanged(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	before := h.e.home(ctx, &u, hc, "", 12, false).Rows
	// An inactive model (failed the gate) must not change anything.
	m := &rankModel{Active: false, Names: []string{"fit"}, Mean: []float64{0}, Std: []float64{1}, Weights: []float64{-50}, LogitStd: 1, Blend: 0.35, RowWeight: 0.2}
	m.compile()
	h.e.rk.model.Store(m)
	h.e.memoMu.Lock()
	h.e.memo = map[memoKey]*memoEntry{}
	h.e.memoMu.Unlock()
	after := h.e.home(ctx, &u, hc, "", 12, false).Rows
	if len(before) != len(after) {
		t.Fatalf("rows %v vs %v", kindsOf(before), kindsOf(after))
	}
	for i := range before {
		if before[i].HubID != after[i].HubID || len(before[i].Items) != len(after[i].Items) {
			t.Fatalf("row %d: %s vs %s", i, before[i].HubID, after[i].HubID)
		}
		for j := range before[i].Items {
			if before[i].Items[j].Key != after[i].Items[j].Key {
				t.Fatalf("row %s item %d changed", before[i].HubID, j)
			}
		}
	}
	// An active, adversarial model moves things, but within bounds: every
	// row's score stays near before (the row multiplier is bounded to
	// ±rowLiftBound; reordered items shift content strength a little).
	m2 := *m
	m2.Active = true
	m2.compile()
	h.e.rk.model.Store(&m2)
	h.e.memoMu.Lock()
	h.e.memo = map[memoKey]*memoEntry{}
	h.e.memoMu.Unlock()
	adv := h.e.home(ctx, &u, hc, "", 12, false).Rows
	if len(adv) < len(before)/2 {
		t.Fatalf("adversarial model wrecked the page: %v", kindsOf(adv))
	}
	score := map[string]float64{}
	for _, r := range before {
		score[r.HubID] = r.Score
	}
	for _, r := range adv {
		if s, ok := score[r.HubID]; ok && s > 0 && (r.Score > s*1.35+0.002 || r.Score < s*0.65-0.002) {
			t.Fatalf("row %s score %.3f vs %.3f", r.HubID, r.Score, s)
		}
	}
}
