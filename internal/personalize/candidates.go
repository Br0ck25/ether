package personalize

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/recommend"
)

// candItem is one title in a candidate row with its context-free score.
type candItem struct {
	t      core.Title
	base   float64 // generator score, roughly 0..1
	fit    float64 // taste fit, roughly -1..1
	qual   float64 // quality 0..1
	reason string
}

// candRow is a row a generator proposes. Assembly decides whether it is
// shown, where, and with which of its items.
type candRow struct {
	kind    string
	id      string // hub id stem without the section prefix: "top10", "because.123"
	title   string
	reason  string
	context string // Plex hub context stem
	source  string
	items   []candItem
	// minItems is the fewest items worth showing the row with (default 4).
	minItems int
	// maxItems caps the row regardless of the client's count (Top 10).
	maxItems int
	// keepOrder rows have a meaningful order (franchise release order,
	// recency) that ranking must not change.
	keepOrder bool
	// scale multiplies the row's predicted engagement (seed recency for
	// Because-you-watched rows).
	scale float64
	// collKey is a backing collection ("More" opens it).
	collKey core.RatingKey
	// homeOnly / sectionOnly restrict where the row may appear; owner
	// collection rows carry their own targeting.
	homeOnly, sectionOnly bool
	// hub is set for rows that arrive as finished hubs (owner collections,
	// Continue Watching); assembly keeps their identity.
	hub *core.Hub
}

// userModel is everything precomputed for one viewer: the taste model and
// the candidate rows. Assembly for a request context is pure CPU over it.
type userModel struct {
	user    core.User
	generic bool
	taste   *Taste
	rows    []*candRow
	fatigue map[core.RatingKey]float64 // item key -> 0..1
	arms    map[string]arm             // this viewer's per-kind row stats
	builtAt time.Time
	took    time.Duration
	// version identifies the build (memo keys).
	version uint64
}

// builder builds one userModel.
type builder struct {
	e       *Engine
	ctx     context.Context
	user    core.User
	generic bool
	taste   *Taste
	touched map[core.RatingKey]bool // taste keys with any history, status or play
	now     time.Time

	statuses map[core.RatingKey]core.WatchStatus
	memo     map[string][]core.Title
	fits     map[core.RatingKey]fitMemo
	rowsOut  []*candRow
}

type fitMemo struct {
	fit float64
	why string
}

// minPersonalConfidence is the taste confidence below which personal rows
// (Top 10, Because you watched…) are not generated: popularity rows do a
// better job for brand-new viewers.
const minPersonalConfidence = 0.12

func (b *builder) personal() bool {
	return !b.generic && b.taste != nil && b.taste.Confidence >= minPersonalConfidence
}

// ---- catalogue access (memoised; failures degrade to empty) -----------------

func (b *builder) memoized(key string, fetch func() ([]core.Title, error)) []core.Title {
	if v, ok := b.memo[key]; ok {
		return v
	}
	items, err := fetch()
	if err != nil {
		b.e.log.Debug("personalize: candidate source failed", "source", key, "err", err)
		items = nil
	}
	b.memo[key] = items
	return items
}

func (b *builder) similar(k core.RatingKey, n int) []core.Title {
	return b.memoized(fmt.Sprintf("similar:%d:%d", k, n), func() ([]core.Title, error) {
		return b.e.catalog.Similar(b.ctx, k, n)
	})
}

func (b *builder) list(name string, t core.MediaType, n int) []core.Title {
	return b.memoized(fmt.Sprintf("list:%s:%s:%d", name, t, n), func() ([]core.Title, error) {
		return b.e.catalog.List(b.ctx, name, t, n)
	})
}

func (b *builder) browse(q core.BrowseQuery) []core.Title {
	if b.user.MaxContentRating != "" {
		q.Filter.MaxContentRating = b.user.MaxContentRating
	}
	return b.memoized(fmt.Sprintf("browse:%+v", q), func() ([]core.Title, error) {
		page, err := b.e.catalog.Browse(b.ctx, q)
		return page.Items, err
	})
}

func (b *builder) both(fetch func(t core.MediaType) []core.Title) []core.Title {
	return interleave(fetch(core.TypeMovie), fetch(core.TypeShow))
}

func interleave(a, c []core.Title) []core.Title {
	out := make([]core.Title, 0, len(a)+len(c))
	for i := 0; i < len(a) || i < len(c); i++ {
		if i < len(a) {
			out = append(out, a[i])
		}
		if i < len(c) {
			out = append(out, c[i])
		}
	}
	return out
}

// loadStatuses fetches watch statuses for keys not yet known.
func (b *builder) loadStatuses(items []core.Title) {
	if b.e.state == nil || b.generic {
		return
	}
	var need []core.RatingKey
	for _, t := range items {
		if _, ok := b.statuses[t.Key]; !ok {
			need = append(need, t.Key)
			b.statuses[t.Key] = core.WatchStatus{Key: t.Key}
		}
	}
	if len(need) == 0 {
		return
	}
	m, err := b.e.state.GetMany(b.ctx, b.user.ID, need)
	if err != nil {
		b.e.log.Debug("personalize: statuses failed", "err", err)
		return
	}
	for k, ws := range m {
		b.statuses[k] = ws
	}
}

// fresh reports whether a title may be recommended for discovery: a film
// or show, released, within the viewer's limits, never watched or started.
func (b *builder) fresh(t *core.Title) bool {
	if t.Type != core.TypeMovie && t.Type != core.TypeShow {
		return false
	}
	if !t.Released.IsZero() && t.Released.After(b.now) {
		return false
	}
	if !recommend.ContentRatingAllowed(t.ContentRating, b.user.MaxContentRating) {
		return false
	}
	if b.touched[tasteKey(t.Key)] {
		return false
	}
	ws := b.statuses[t.Key]
	return ws.ViewCount == 0 && ws.ViewOffset == 0
}

func (b *builder) fit(t *core.Title) (float64, string) {
	if m, ok := b.fits[t.Key]; ok {
		return m.fit, m.why
	}
	f, why := b.taste.Fit(t)
	b.fits[t.Key] = fitMemo{f, why}
	return f, why
}

// scorer turns a candidate (its index in the source list of n, its fit
// and the taste reason) into a score and an item reason.
type scorer func(t *core.Title, idx, n int, fit float64, why string) (float64, string)

// rank keeps fresh, unique titles and scores them, best first.
func (b *builder) rank(items []core.Title, score scorer) []candItem {
	b.loadStatuses(items)
	seen := map[core.RatingKey]bool{}
	var out []candItem
	n := len(items)
	for i := range items {
		t := &items[i]
		if seen[t.Key] || !b.fresh(t) {
			continue
		}
		seen[t.Key] = true
		fit, why := b.fit(t)
		s, reason := score(t, i, n, fit, why)
		if math.IsNaN(s) || math.IsInf(s, 0) {
			continue
		}
		out = append(out, candItem{t: *t, base: s, fit: fit, qual: quality(t), reason: reason})
	}
	sortItems(out)
	return out
}

func sortItems(items []candItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].base != items[j].base {
			return items[i].base > items[j].base
		}
		return items[i].t.Key < items[j].t.Key
	})
}

func capItems(items []candItem, n int) []candItem {
	if n > 0 && len(items) > n {
		return items[:n]
	}
	return items
}

// ---- item features -------------------------------------------------------------

// quality is a Bayesian-shrunk TMDB rating mapped to 0..1 (5.0 → 0,
// 8.5 → 1), so a 9.0 from 12 votes does not beat an 8.2 from 20,000.
func quality(t *core.Title) float64 {
	if t.Rating <= 0 {
		return 0.3
	}
	v := float64(t.VoteCount)
	if v <= 0 {
		v = 50
	}
	const m, c = 150.0, 6.4
	bayes := (v*t.Rating + m*c) / (v + m)
	return clamp((bayes-5)/3.5, 0, 1)
}

// popularity maps TMDB popularity onto 0..1 on a log scale.
func popularity(t *core.Title) float64 {
	return clamp(math.Log1p(math.Max(t.Popularity, 0))/math.Log1p(500), 0, 1)
}

// recency is 1 for today's releases, decaying with a ~4-month scale.
func recency(t *core.Title, now time.Time) float64 {
	if t.Released.IsZero() {
		return 0.3
	}
	days := now.Sub(t.Released).Hours() / 24
	if days < 0 {
		return 0
	}
	return math.Exp(-days / 120)
}

// rankScore is 1 for the first of n items, falling linearly to ~0.
func rankScore(idx, n int) float64 {
	if n <= 1 {
		return 1
	}
	return 1 - float64(idx)/float64(n)
}

func minutes(t *core.Title) float64 { return t.Runtime.Minutes() }
