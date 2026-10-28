package personalize

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// The owner's three dials per page (core.LayoutTuning), each 0..1 with 0.5
// as the engine default. At 0.5 every effect below is exactly zero, so an
// untuned page ranks as it always has. Effects are bounded: a dial never
// overrides the taste model, it shifts the balance.
//
//   - Discovery: scales bandit exploration (0.5×–1.5×), and moves items
//     between proven fit and popularity (0) and unfamiliar genres and
//     lesser-known titles (1); explore-leaning rows rise, familiar rows
//     (Because you watched, More from…, comfort) fall, and vice versa.
//   - Freshness: moves items between evergreen acclaimed titles (0) and
//     recent releases (1); fresh rows (new, trending, airing) and
//     evergreen rows (top rated, eras, complete series) trade places.
//   - Variety: sets MMR λ inside rows (0.95 focused … 0.55 varied) and a
//     page-level penalty for rows repeating an earlier row's dominant
//     genre (a bonus below 0.5: focus on the strongest tastes).
type tuning struct {
	discovery, freshness, variety float64
}

var defaultTuning = tuning{0.5, 0.5, 0.5}

// rowLean classifies a row kind for the dials.
type rowLean uint8

const (
	leanFresh rowLean = 1 << iota
	leanEvergreen
	leanExplore
	leanFamiliar
)

// resolveTuning resolves each dial: the viewer's page, then the global
// page, then the default.
func resolveTuning(d core.HomeLayout, user int64, section string) tuning {
	if section == "" {
		section = surfHome
	}
	t := defaultTuning
	var viewer *core.LayoutPage
	for _, v := range d.Viewers {
		if v.UserID == user {
			viewer = pageFor(v.Pages, section)
		}
	}
	for _, p := range []*core.LayoutPage{pageFor(d.Global, section), viewer} {
		if p == nil || p.Tuning == nil {
			continue
		}
		set := func(dst *float64, v *float64) {
			if v != nil && !math.IsNaN(*v) {
				*dst = clamp(*v, 0, 1)
			}
		}
		set(&t.discovery, p.Tuning.Discovery)
		set(&t.freshness, p.Tuning.Freshness)
		set(&t.variety, p.Tuning.Variety)
	}
	return t
}

// resolveDedupeHome resolves a page's DedupeHome switch (viewer page, then
// global page; default on). Home never dedupes against itself.
func resolveDedupeHome(d core.HomeLayout, user int64, section string) bool {
	if section == "" || section == surfHome {
		return false
	}
	on := true
	for _, p := range []*core.LayoutPage{pageFor(d.Global, section), viewerPage(d, user, section)} {
		if p != nil && p.DedupeHome != nil {
			on = *p.DedupeHome
		}
	}
	return on
}

func viewerPage(d core.HomeLayout, user int64, section string) *core.LayoutPage {
	for _, v := range d.Viewers {
		if v.UserID == user {
			return pageFor(v.Pages, section)
		}
	}
	return nil
}

// key is the tuning's memo-key form.
func (t tuning) key() string {
	return fmt.Sprintf("%.3f/%.3f/%.3f", t.discovery, t.freshness, t.variety)
}

// exploreScale multiplies the engine's Thompson exploration.
func (t tuning) exploreScale() float64 { return 0.5 + t.discovery }

// lambda is the MMR relevance weight inside rows.
func (t tuning) lambda() float64 { return clamp(mmrLambda-0.4*(t.variety-0.5), 0.5, 0.95) }

// rowMult shifts rows by their lean.
func (t tuning) rowMult(l rowLean) float64 {
	dd, df := t.discovery-0.5, t.freshness-0.5
	m := 1.0
	if l&leanExplore != 0 {
		m *= 1 + 0.8*dd
	}
	if l&leanFamiliar != 0 {
		m *= 1 - 0.6*dd
	}
	if l&leanFresh != 0 {
		m *= 1 + 0.8*df
	}
	if l&leanEvergreen != 0 {
		m *= 1 - 0.6*df
	}
	return m
}

// tuneItem is the dials' part of an item's context score (one hook in
// adjust), at most ±0.32.
func (a *assembly) tuneItem(it *candItem) float64 {
	t := &it.t
	dd, df := a.dials().discovery-0.5, a.dials().freshness-0.5
	if dd == 0 && df == 0 {
		return 0
	}
	adj := 0.0
	if dd != 0 {
		adj += dd * (0.30*novelty(a.model.taste, t) + 0.14*(1-popularity(t)) - 0.20*clamp(it.fit, -1, 1))
	}
	if df != 0 {
		adj += df * (0.36*recency(t, a.hc.Now) - 0.24*evergreen(t, a.hc.Now) + 0.12*a.model.buzz[tasteKey(t.Key)])
	}
	return adj
}

// novelty is how far a title sits from the viewer's favourite genres:
// 1 for genres they never watch, 0 for their strongest.
func novelty(ts *Taste, t *core.Title) float64 {
	if ts == nil {
		return 0.5
	}
	best := 0.0
	for _, g := range normGenres(t.Genres) {
		best = math.Max(best, ts.Genre[g])
	}
	return 1 - clamp(best, 0, 1)
}

// evergreen is an acclaimed title that has stood the test of time: its
// quality, scaled up over its first ten years.
func evergreen(t *core.Title, now time.Time) float64 {
	if t.Released.IsZero() || now.IsZero() {
		return 0
	}
	years := now.Sub(t.Released).Hours() / 24 / 365
	return quality(t) * clamp((years-2)/8, 0, 1)
}

// spreadRows reorders ranked rows so a page does not stack rows of one
// dominant genre (variety > 0.5), or lets the strongest tastes cluster
// (variety < 0.5): each pick's score is multiplied by (1−p)^k, k being how
// many rows already chosen share its dominant genre.
func (a *assembly) spreadRows(rows []*rankedRow) {
	p := 0.4 * (a.dials().variety - 0.5)
	if p == 0 || len(rows) < 2 {
		return
	}
	dom := make([]string, len(rows))
	for i, r := range rows {
		dom[i] = dominantGenre(r.items)
	}
	used := make([]bool, len(rows))
	count := map[string]int{}
	out := make([]*rankedRow, 0, len(rows))
	for len(out) < len(rows) {
		best, bestV := -1, 0.0
		for i, r := range rows {
			if used[i] {
				continue
			}
			v := r.score
			if dom[i] != "" {
				v *= math.Pow(1-p, float64(count[dom[i]]))
			}
			if best < 0 || v > bestV {
				best, bestV = i, v
			}
		}
		used[best] = true
		if dom[best] != "" {
			count[dom[best]]++
		}
		out = append(out, rows[best])
	}
	copy(rows, out)
}

// dominantGenre is the genre most of a row's leading titles share (at
// least half of the first six), or "".
func dominantGenre(items []candItem) string {
	n := min(6, len(items))
	if n < 2 {
		return ""
	}
	count := map[string]int{}
	for _, it := range items[:n] {
		for _, g := range normGenres(it.t.Genres) {
			count[g]++
		}
	}
	keys := make([]string, 0, len(count))
	for g := range count {
		keys = append(keys, g)
	}
	sort.Strings(keys)
	best, bestN := "", 0
	for _, g := range keys {
		if count[g] > bestN {
			best, bestN = g, count[g]
		}
	}
	if 2*bestN < n {
		return ""
	}
	return best
}

// occasionMult favours an occasion row at the times it suits.
func occasionMult(id string, hc core.HubContext) float64 {
	switch id {
	case occasionDateNight:
		if hc.Daypart == core.DaypartEvening || hc.Daypart == core.DaypartLate {
			if hc.Weekend {
				return 1.4
			}
			return 1.1
		}
		return 0.7
	case occasionFamilyNight:
		if hc.Weekend && (hc.Daypart == core.DaypartAfternoon || hc.Daypart == core.DaypartEvening) {
			return 1.35
		}
		if hc.Daypart == core.DaypartLate {
			return 0.6
		}
	case occasionShort:
		if !hc.Weekend && (hc.Daypart == core.DaypartEvening || hc.Daypart == core.DaypartLate) || hc.Device == core.DeviceMobile {
			return 1.15
		}
		if hc.Weekend && hc.Daypart == core.DaypartEvening && hc.Device == core.DeviceTV {
			return 0.7
		}
	}
	return 1
}

// dials is the assembly's resolved tuning (the default when unset).
func (a *assembly) dials() tuning {
	if a.tune == nil {
		return defaultTuning
	}
	return *a.tune
}
