package personalize

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// The learned ranker: a feedback-driven correction on top of the
// hand-tuned scores, YouTube/TikTok-style but scaled to a household.
//
//   - Every assembly captures a compact feature snapshot per (row, title)
//     (rankPass); the pages actually served are logged with those
//     snapshots (servelog.go).
//   - A play labels the serve it came from positive, graded by completion;
//     serves whose 48 hour window closes without a play become negatives.
//   - A pooled logistic regression (ranker_train.go) is retrained on a
//     schedule with a time-ordered hold-out. Row and item positions are
//     features during training and fixed at inference, so the model learns
//     what viewers want rather than what was simply shown first.
//   - It only influences ranking once it has enough data and beats the
//     hand-tuned baseline on the hold-out; then it is blended into item
//     scores (adjust) and row scores (rowScore) with a bounded weight that
//     grows with data volume and lift.
//   - A small, logged UCB exploration bonus lifts under-exposed titles.

// RankerOptions tune the learned ranker. Zero values take the defaults.
type RankerOptions struct {
	// LogRows × LogItems is the part of a served page logged (default 8×12).
	LogRows, LogItems int
	// MaxServesPerDay caps logged pages per viewer per day (default 48).
	MaxServesPerDay int
	// MaxItems caps logged items overall (default 1,000,000).
	MaxItems int
	// Retention of the serve log (default 60 days).
	Retention time.Duration
	// TrainEvery is the retraining period (default 6h). Negative disables
	// the background loop; it is also off when Options.RefreshEvery is
	// negative, unless set explicitly.
	TrainEvery time.Duration
	// RetrainLabels retrains early once this many new labels arrived
	// (default 1000).
	RetrainLabels int
	// TrainHour is the local hour training prefers (default 4, the quiet
	// small hours; negative = any hour). A model older than two days
	// trains at the next quiet moment regardless. Training never starts
	// while Options.Busy reports viewers streaming.
	TrainHour int
	// MinExamples / MinPositives gate activation (default 2000 / 30).
	MinExamples, MinPositives int
	// MinLift is the held-out AUC margin over the baseline required to
	// activate (default 0.02).
	MinLift float64
	// MaxBlend bounds the model's share of an item score (default 0.35).
	MaxBlend float64
	// Disabled turns logging, training and inference off.
	Disabled bool
}

func (o *RankerOptions) defaults(refreshEvery time.Duration) {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&o.LogRows, 8)
	def(&o.LogItems, 12)
	def(&o.MaxServesPerDay, 48)
	def(&o.MaxItems, 1_000_000)
	def(&o.RetrainLabels, 1000)
	if o.TrainHour == 0 {
		o.TrainHour = 4
	}
	def(&o.MinExamples, 2000)
	def(&o.MinPositives, 30)
	if o.Retention <= 0 {
		o.Retention = 60 * 24 * time.Hour
	}
	if o.TrainEvery == 0 {
		o.TrainEvery = 6 * time.Hour
		if refreshEvery < 0 {
			o.TrainEvery = -1
		}
	}
	if o.MinLift <= 0 {
		o.MinLift = 0.02
	}
	if o.MaxBlend <= 0 || o.MaxBlend > 0.5 {
		o.MaxBlend = 0.35
	}
}

// Inference bounds.
const (
	// maxItemDelta bounds how far the model can move one item's score.
	maxItemDelta = 0.2
	// rowLiftBound bounds the row-score multiplier to 1±rowLiftBound.
	rowLiftBound = 0.2
	// exploreBonus is the score lift of an exploration pick; exploreRate
	// scales the UCB probability; exploreMinServes is how many logged
	// pages a viewer needs before exploration starts.
	exploreBonus     = 0.04
	exploreRate      = 0.3
	exploreMaxProb   = 0.5
	exploreMinServes = 20
	// neverShownDays is the "days since last shown" of a title never shown.
	neverShownDays = 60
)

// ---- features --------------------------------------------------------------------

// rawFeatureVersion versions the snapshot layout; bump when rawFeatures
// changes so old snapshots are not misread. Version 1 snapshots (the first
// v1Features features) still train, with the later features at zero.
const rawFeatureVersion = 2

// v1Features is the snapshot length of version 1 (before crowd, consensus
// and buzz).
const v1Features = 17

// rawFeature indexes the snapshot captured at assembly time.
type rawFeature int

const (
	fFit rawFeature = iota
	fQuality
	fBase
	fPopularity
	fAge
	fTrending
	fRuntime
	fMovie
	fDaypartGenre
	fIntent
	fFatigue
	fExposures
	fSinceShown
	fServerRate
	fInProgress
	fNewToViewer
	fWatched
	fCrowd     // crowd co-engagement score (crowd.go)
	fConsensus // independent favourites recommending the title
	fBuzz      // trend momentum (buzz.go)
	numRaw
)

// rawFeatures names and labels the snapshot features (order = rawFeature).
var rawFeatures = [numRaw]struct{ name, label string }{
	{"fit", "Taste fit"},
	{"quality", "Quality (ratings and votes)"},
	{"base", "Row generator score"},
	{"popularity", "Popularity percentile"},
	{"age", "Age since release (log days)"},
	{"trending", "Trending now"},
	{"runtime", "Runtime vs the viewer's usual time (log ratio)"},
	{"movie", "Film rather than series"},
	{"daypart_genre", "Genre match for this time of day"},
	{"intent", "Session intent match"},
	{"fatigue", "Shown on several days without a play"},
	{"exposures", "Times served before (log)"},
	{"since_shown", "Days since last shown (log)"},
	{"server_rate", "Server-wide play rate"},
	{"in_progress", "In progress"},
	{"new_to_viewer", "New to the viewer"},
	{"watched", "Already finished (rewatch)"},
	{"crowd", "Loved by fans of what they watch"},
	{"consensus", "Recommended by several of their favourites"},
	{"buzz", "Trending now"},
}

// itemSnap is one (row, title)'s snapshot from an assembly.
type itemSnap struct {
	raw        [numRaw]float32
	baseline   float32 // hand-tuned item score (before the model)
	explore    float32 // exploration bonus applied
	propensity float32 // probability the exploration bonus was applied
}

type featKey struct {
	hub string
	key core.RatingKey
}

// ---- engine state --------------------------------------------------------------

type passKey struct {
	user    int64
	surface string
	bucket  string
}

type passEntry struct {
	snaps map[featKey]*itemSnap
	at    time.Time
}

type sigKey struct {
	user    int64
	surface string
}

type lastServe struct {
	id  int64
	sig uint64
	at  time.Time
}

type dailyKey struct {
	user int64
	day  int
}

// rankerState is the engine's ranker: the current model, recent feature
// snapshots, exposure statistics and the serve-log writer.
type rankerState struct {
	opts   RankerOptions
	db     *sql.DB
	writer *serveWriter
	model  atomic.Pointer[rankModel]
	nextID atomic.Int64

	mu         sync.Mutex
	passes     map[passKey]*passEntry
	lastSig    map[sigKey]lastServe
	daily      map[dailyKey]int
	expo       map[int64]*userExpo
	serverRate map[core.RatingKey]float64

	trainMu    sync.Mutex
	lastLabels int // labelled examples at the last training
}

// startRanker loads the persisted model and statistics and starts the
// serve-log writer and the training loop (when enabled). Failures are
// logged: ranking simply stays hand-tuned.
func (e *Engine) startRanker() {
	o := e.opts.Ranker
	o.defaults(e.opts.RefreshEvery)
	if o.Disabled {
		return
	}
	rs := &rankerState{opts: o, db: e.opts.DB, passes: map[passKey]*passEntry{}, lastSig: map[sigKey]lastServe{},
		daily: map[dailyKey]int{}, expo: map[int64]*userExpo{}, serverRate: map[core.RatingKey]float64{}}
	rs.writer = newServeWriter(e.opts.DB, e.log, e.opts.ImpressionBuffer, e.opts.FlushEvery, e.ctx.Done())
	ctx := context.Background()
	var maxID sql.NullInt64
	if err := e.opts.DB.QueryRowContext(ctx, `SELECT MAX(id) FROM personalize_serves`).Scan(&maxID); err != nil {
		e.log.Warn("personalize: ranker unavailable", "err", err)
		return
	}
	rs.nextID.Store(maxID.Int64)
	if m, err := loadRankModel(ctx, e.opts.DB); err != nil {
		e.log.Warn("personalize: loading ranker model failed", "err", err)
	} else if m != nil {
		m.compile()
		rs.model.Store(m)
		rs.lastLabels = m.Labels
	}
	e.rk = rs
	e.refreshRankerStats(ctx)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); rs.writer.run(e.ctx) }()
	if o.TrainEvery > 0 {
		e.wg.Add(1)
		go e.rankerLoop(e.ctx)
	}
}

// rankerLoop finalises labels, refreshes statistics, applies retention and
// retrains on schedule.
func (e *Engine) rankerLoop(ctx context.Context) {
	defer e.wg.Done()
	rs := e.rk
	every := min(rs.opts.TrainEvery, time.Hour)
	t := time.NewTicker(every)
	defer t.Stop()
	var lastPurge, lastTrain time.Time
	if m := rs.model.Load(); m != nil {
		lastTrain = m.TrainedAt
	}
	for {
		now := e.now()
		if now.Sub(lastPurge) > 24*time.Hour {
			lastPurge = now
			if n, err := purgeServes(ctx, rs.db, now, rs.opts.Retention, rs.opts.MaxItems); err != nil {
				e.log.Warn("personalize: serve log retention failed", "err", err)
			} else if n > 0 {
				e.log.Info("personalize: serve log retention removed rows", "rows", n)
			}
		}
		if _, err := finalizeNegatives(ctx, rs.db, now); err != nil && ctx.Err() == nil {
			e.log.Debug("personalize: finalising ranker labels failed", "err", err)
		}
		e.refreshRankerStats(ctx)
		labels, err := countLabels(ctx, rs.db)
		if err == nil {
			rs.trainMu.Lock()
			fresh := labels - rs.lastLabels
			rs.trainMu.Unlock()
			due := now.Sub(lastTrain) >= rs.opts.TrainEvery && fresh > 0
			if (due || fresh >= rs.opts.RetrainLabels) && labels >= rankMinTrain && e.quietToTrain(now, lastTrain) {
				lastTrain = now
				if _, err := e.TrainRanker(ctx); err != nil && ctx.Err() == nil {
					e.log.Warn("personalize: training ranker failed", "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// quietToTrain reports whether training may start now: never while viewers
// are streaming, and in the overnight window unless the model is already
// two days old (or has never been trained).
func (e *Engine) quietToTrain(now, lastTrain time.Time) bool {
	if e.opts.Busy != nil && e.opts.Busy() {
		return false
	}
	h := e.rk.opts.TrainHour
	if h < 0 || lastTrain.IsZero() || now.Sub(lastTrain) >= 48*time.Hour {
		return true
	}
	local := now.In(e.loc).Hour()
	return (local-h+24)%24 < 3 // a three-hour window from TrainHour
}

// refreshRankerStats reloads exposure counts and server play rates.
func (e *Engine) refreshRankerStats(ctx context.Context) {
	rs := e.rk
	if rs == nil {
		return
	}
	ex, rates, err := loadExposure(ctx, rs.db, e.now().AddDate(0, 0, -30))
	if err != nil {
		if ctx.Err() == nil {
			e.log.Debug("personalize: ranker stats failed", "err", err)
		}
		return
	}
	rs.mu.Lock()
	rs.expo, rs.serverRate = ex, rates
	rs.mu.Unlock()
}

// snapshots returns the feature snapshots of the latest assembly for a
// viewer, surface and context bucket.
func (rs *rankerState) snapshots(user int64, surface, bucket string) map[featKey]*itemSnap {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if p := rs.passes[passKey{user, surface, bucket}]; p != nil {
		return p.snaps
	}
	return nil
}

// ---- per-assembly pass -----------------------------------------------------------

// rankPass is one assembly's view of the ranker: the active model (nil =
// hand-tuned only), the viewer's exposure, and the snapshots captured.
type rankPass struct {
	rs      *rankerState
	key     passKey
	publish bool
	now     time.Time
	model   *rankModel // active model, nil when inactive
	expo    *userExpo
	rates   map[core.RatingKey]float64
	snaps   map[featKey]*itemSnap
	hubs    map[*candRow]string

	pops     []float64 // sorted candidate popularity (lazy)
	trending map[core.RatingKey]bool
	inprog   map[core.RatingKey]bool
	target   float64
	ready    bool
}

// newRankPass starts the ranker's part of one assembly. Draft previews do
// not publish their snapshots.
func (e *Engine) newRankPass(u core.User, hc core.HubContext, section core.MediaType, draft bool) *rankPass {
	rs := e.rk
	if rs == nil {
		return nil
	}
	p := &rankPass{rs: rs, key: passKey{u.ID, sectionName(section), hc.Bucket()}, publish: !draft, now: hc.Now,
		snaps: map[featKey]*itemSnap{}, hubs: map[*candRow]string{}}
	if p.now.IsZero() {
		p.now = e.now()
	}
	if m := rs.model.Load(); m != nil && m.Active {
		p.model = m
	}
	rs.mu.Lock()
	p.expo, p.rates = rs.expo[u.ID], rs.serverRate
	rs.mu.Unlock()
	return p
}

// publish keeps the snapshots of the titles the assembly returned, for the
// serve log to pick up when the page is served.
func (p *rankPass) publishRows(rows []core.HomeRow) {
	if p == nil || !p.publish {
		return
	}
	keep := make(map[featKey]*itemSnap, 256)
	for _, r := range rows {
		for _, t := range r.Items {
			k := featKey{r.HubID, t.Key}
			if s, ok := p.snaps[k]; ok {
				keep[k] = s
			}
		}
	}
	rs := p.rs
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.passes) >= 512 {
		oldest, at := passKey{}, time.Time{}
		for k, v := range rs.passes {
			if at.IsZero() || v.at.Before(at) {
				oldest, at = k, v.at
			}
		}
		delete(rs.passes, oldest)
	}
	rs.passes[p.key] = &passEntry{snaps: keep, at: p.now}
}

// prepare computes the per-assembly lookups on first use.
func (p *rankPass) prepare(a *assembly) {
	if p.ready {
		return
	}
	p.ready = true
	p.trending = map[core.RatingKey]bool{}
	p.inprog = map[core.RatingKey]bool{}
	all := append(append([]*candRow{}, a.model.rows...), a.owner...)
	for _, c := range all {
		trend := c.kind == KindTrending || c.kind == KindTrendingTaste || c.kind == KindTop10Today || c.kind == KindCrowdTrending
		for i := range c.items {
			p.pops = append(p.pops, c.items[i].t.Popularity)
			if trend {
				p.trending[c.items[i].t.Key] = true
			}
		}
	}
	sort.Float64s(p.pops)
	for _, it := range a.cw {
		p.inprog[tasteKey(it.t.Key)] = true
	}
	p.target = a.model.taste.targetMinutes(a.hc)
}

func (p *rankPass) hubOf(a *assembly, c *candRow) string {
	if h, ok := p.hubs[c]; ok {
		return h
	}
	h := a.hubID(c)
	p.hubs[c] = h
	return h
}

// features computes a title's raw features in this assembly.
func (p *rankPass) features(a *assembly, it *candItem) [numRaw]float32 {
	p.prepare(a)
	t := &it.t
	var f [numRaw]float64
	f[fFit], f[fQuality], f[fBase] = it.fit, it.qual, it.base
	if n := len(p.pops); n > 0 {
		f[fPopularity] = float64(sort.SearchFloat64s(p.pops, t.Popularity)) / float64(n)
	}
	switch {
	case !t.Released.IsZero():
		f[fAge] = math.Log1p(math.Max(0, p.now.Sub(t.Released).Hours()/24))
	case t.Year > 0:
		f[fAge] = math.Log1p(math.Max(0, p.now.Sub(time.Date(t.Year, 7, 1, 0, 0, 0, 0, time.UTC)).Hours()/24))
	default:
		f[fAge] = math.Log1p(3650)
	}
	if p.trending[t.Key] {
		f[fTrending] = 1
	}
	if m := minutes(t); m > 0 && p.target > 0 {
		f[fRuntime] = clamp(math.Log(m/p.target), -2, 2)
	}
	if t.Type == core.TypeMovie {
		f[fMovie] = 1
	}
	if gs := normGenres(t.Genres); len(gs) > 0 {
		f[fDaypartGenre] = a.model.taste.ContextGenreLift(a.hc, gs)
	}
	if a.intent.active() {
		f[fIntent] = a.intent.boostFor(t) / intentMaxBoost
	}
	f[fFatigue] = a.model.fatigue[t.Key]
	x := expo{}
	if p.expo != nil {
		x = p.expo.items[t.Key]
	}
	f[fExposures] = math.Log1p(float64(x.n))
	since := float64(neverShownDays)
	if !x.last.IsZero() {
		since = clamp(p.now.Sub(x.last).Hours()/24, 0, neverShownDays)
	}
	f[fSinceShown] = math.Log1p(since)
	rate, ok := p.rates[tasteKey(t.Key)]
	if !ok {
		rate = smoothedRate(0, 0)
	}
	f[fServerRate] = rate
	ev := a.model.taste.evidenceFor(t.Key)
	if p.inprog[tasteKey(t.Key)] || ev != nil && ev.Kind == EvidenceWatching {
		f[fInProgress] = 1
	}
	if ev != nil && ev.Kind == EvidenceFinished {
		f[fWatched] = 1
	}
	if (ev == nil || ev.Kind == EvidenceIgnored) && x.n == 0 {
		f[fNewToViewer] = 1
	}
	if cf, ok := a.model.crowd[tasteKey(t.Key)]; ok {
		f[fCrowd] = float64(cf.score)
		f[fConsensus] = float64(min(cf.consensus, 6))
	}
	f[fBuzz] = a.model.buzz[tasteKey(t.Key)]
	var out [numRaw]float32
	for i, v := range f {
		out[i] = float32(v)
	}
	return out
}

// explore is the UCB exploration treatment of a title: with probability
// q = exploreRate × Exploration × sqrt(ln(N+1) / (n+1)) (N pages served to
// the viewer, n of them with this title; capped), seeded per viewer, day
// and title so a page is stable through the day, the title gets
// exploreBonus. It returns the bonus applied and q.
func (p *rankPass) explore(a *assembly, key core.RatingKey) (bonus, q float64) {
	if p.expo == nil || p.expo.serves < exploreMinServes || a.explore <= 0 {
		return 0, 0
	}
	n := p.expo.items[key].n
	q = clamp(exploreRate*a.explore*math.Sqrt(math.Log(float64(p.expo.serves)+1)/float64(n+1)), 0, exploreMaxProb)
	if unit("explore", a.user.ID, a.day, key) < q {
		bonus = exploreBonus
	}
	return bonus, q
}

func (p *rankPass) capture(hub string, key core.RatingKey, raw [numRaw]float32, baseline, bonus, q float64) {
	p.snaps[featKey{hub, key}] = &itemSnap{raw: raw, baseline: float32(baseline), explore: float32(bonus), propensity: float32(q)}
}

// skipKind: rows the ranker neither logs nor scores (the viewer is already
// watching their titles).
func skipKind(kind string) bool { return kind == KindContinue || kind == KindUpNext }

// rankerItem is the learned part of an item's context score (the adjust
// hook): it captures the title's features, blends in the active model and
// adds the exploration bonus. adj is the hand-tuned adjustment so far.
func (a *assembly) rankerItem(c *candRow, it *candItem, adj float64) float64 {
	p := a.rk
	if p == nil || skipKind(c.kind) {
		return 0
	}
	raw := p.features(a, it)
	score := it.base + adj
	delta := 0.0
	if p.model != nil {
		delta = p.model.itemDelta(raw[:], c.kind, p.key.surface, score)
	}
	bonus, q := p.explore(a, it.t.Key)
	p.capture(p.hubOf(a, c), it.t.Key, raw, score, bonus, q)
	return delta + bonus
}

// rankerRow is the learned multiplier of a row's score (the rowScore hook):
// 1 ± rowLiftBound from the model's mean score of the row's top titles. It
// also captures features for titles of fixed-order rows, which adjust
// never sees.
func (a *assembly) rankerRow(c *candRow, items []candItem) float64 {
	p := a.rk
	if p == nil || skipKind(c.kind) || len(items) == 0 {
		return 1
	}
	hub := p.hubOf(a, c)
	n := min(len(items), 3*a.limit+6)
	for i := range items[:n] {
		k := featKey{hub, items[i].t.Key}
		if _, ok := p.snaps[k]; !ok {
			p.capture(hub, items[i].t.Key, p.features(a, &items[i]), items[i].base, 0, 0)
		}
	}
	if p.model == nil {
		return 1
	}
	top := min(5, len(items))
	sum := 0.0
	for i := range items[:top] {
		s := p.snaps[featKey{hub, items[i].t.Key}]
		sum += p.model.modelScore(s.raw[:], c.kind, p.key.surface)
	}
	return p.model.rowLift(sum / float64(top))
}

// ---- owner insight -------------------------------------------------------------

// RankerInsight reports the learned ranker for the owner UI: when it last
// trained, on how much data, its held-out AUC against the hand-tuned
// baseline, whether it is active, and its weights by magnitude.
func (e *Engine) RankerInsight(ctx context.Context) (core.RankerInsight, error) {
	out := core.RankerInsight{Features: []core.RankerFeature{}}
	if e.rk == nil {
		return out, nil
	}
	m := e.rk.model.Load()
	if m == nil {
		return out, ctx.Err()
	}
	at := m.TrainedAt
	out.TrainedAt = &at
	out.Examples, out.Positives = m.Examples, m.Positives
	out.AUC, out.BaselineAUC = round3(m.AUC), round3(m.BaselineAUC)
	out.Active = m.Active
	for i, name := range m.Names {
		out.Features = append(out.Features, core.RankerFeature{Name: name, Label: featureLabel(name), Weight: round3(m.Weights[i])})
	}
	sort.SliceStable(out.Features, func(i, j int) bool {
		wi, wj := math.Abs(out.Features[i].Weight), math.Abs(out.Features[j].Weight)
		if wi != wj {
			return wi > wj
		}
		return out.Features[i].Name < out.Features[j].Name
	})
	return out, nil
}

// featureLabel is the owner-facing label of a design feature.
func featureLabel(name string) string {
	for _, f := range rawFeatures {
		if f.name == name {
			return f.label
		}
	}
	switch {
	case strings.HasPrefix(name, "pos:row:"):
		return "Shown in row " + slotLabel(name, "pos:row:", posRowSlots) + " (position bias; not used when ranking)"
	case strings.HasPrefix(name, "pos:item:"):
		return "Shown at position " + slotLabel(name, "pos:item:", posItemSlots) + " in its row (position bias; not used when ranking)"
	case strings.HasPrefix(name, "kind:"):
		k := strings.TrimPrefix(name, "kind:")
		switch k {
		case "collection":
			return "Row: owner collection"
		case "other":
			return "Row: other"
		}
		return "Row: " + infoFor(k).title
	case strings.HasPrefix(name, "surface:"):
		s := strings.TrimPrefix(name, "surface:")
		return "Page: " + strings.ToUpper(s[:1]) + s[1:]
	}
	return name
}

// slotLabel is the 1-based slot of a position feature ("8+" for the last).
func slotLabel(name, prefix string, slots int) string {
	n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	if err != nil {
		return "?"
	}
	if n >= slots-1 {
		return strconv.Itoa(n+1) + "+"
	}
	return strconv.Itoa(n + 1)
}
