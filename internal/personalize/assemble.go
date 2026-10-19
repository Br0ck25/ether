package personalize

import (
	"math"
	"sort"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/recommend"
)

// assembly turns a precomputed userModel into the rows one request
// receives. It is pure: same inputs, same rows. Everything time-dependent
// (rotation, exploration) is seeded by the local date.
type assembly struct {
	model   *userModel
	owner   []*candRow // owner collection rows for this context
	cw      []candItem // live Continue Watching items (Home only)
	user    core.User  // the requesting viewer
	hc      core.HubContext
	section core.MediaType // "" = Home
	limit   int            // items per row
	rules   []core.LayoutRule
	global  map[string]arm // server-wide evidence by prior kind
	arms    map[string]arm // this viewer's evidence by kind
	allowed func(core.MediaType) bool
	maxRows int
	explore float64
	day     int
}

// rankedRow is a candidate row during assembly.
type rankedRow struct {
	c        *candRow
	items    []candItem
	hubID    string
	score    float64
	d        decision
	strength float64
}

// mmrLambda trades relevance against redundancy inside a row.
const mmrLambda = 0.75

// maxOwnerRows caps owner collection rows on one screen.
const maxOwnerRows = 4

func (a *assembly) run() []core.HomeRow {
	plan := planFor(a.rules, a.user.ID, sectionName(a.section), a.hc)
	var rows []*rankedRow
	cands := append([]*candRow{}, a.model.rows...)
	cands = append(cands, a.owner...)
	// New viewers' Homes also get the catalogue-wide rows that otherwise
	// live in sections (Top Rated, Popular…): there is nothing personal to
	// put there yet.
	newViewer := a.model.taste == nil || a.model.taste.Confidence < minPersonalConfidence
	for _, c := range cands {
		if a.section == "" && c.sectionOnly && !newViewer || a.section != "" && c.homeOnly {
			continue
		}
		if c.kind == KindContinue {
			if a.section != "" {
				continue
			}
			live := *c
			live.items = a.cw
			c = &live
		}
		items := a.contextItems(c)
		if len(items) == 0 && c.kind != KindContinue {
			continue
		}
		rr := &rankedRow{c: c, items: items, hubID: a.hubID(c)}
		rr.d = plan.decide(rr.hubID, c.kind)
		rr.score, rr.strength = a.rowScore(c, items)
		if rr.d.boost {
			rr.score *= 1.5
		}
		rows = append(rows, rr)
	}

	var visible, pinned, hidden []*rankedRow
	for _, r := range rows {
		switch {
		case r.d.hidden:
			hidden = append(hidden, r)
		case r.d.pinned:
			pinned = append(pinned, r)
		default:
			visible = append(visible, r)
		}
	}
	sort.SliceStable(visible, func(i, j int) bool {
		if visible[i].d.bury != visible[j].d.bury {
			return !visible[i].d.bury
		}
		if visible[i].score != visible[j].score {
			return visible[i].score > visible[j].score
		}
		return visible[i].hubID < visible[j].hubID
	})
	// Per-kind caps (pinned rows count but are never dropped).
	perKind := map[string]int{}
	owners := 0
	for _, r := range pinned {
		perKind[r.c.kind]++
		if r.c.source == core.RowSourceOwner {
			owners++
		}
	}
	capped := visible[:0:0]
	for _, r := range visible {
		info := infoFor(r.c.kind)
		if perKind[r.c.kind] >= info.cap {
			continue
		}
		if r.c.source == core.RowSourceOwner && owners >= maxOwnerRows {
			continue
		}
		perKind[r.c.kind]++
		if r.c.source == core.RowSourceOwner {
			owners++
		}
		capped = append(capped, r)
	}
	ordered := placePinned(capped, pinned, func(r *rankedRow) int { return r.d.position })

	maxRows := a.maxRows
	if plan.maxRows > 0 {
		maxRows = plan.maxRows
	}
	out := a.fill(ordered, maxRows)
	for _, r := range hidden {
		items := r.items
		if len(items) > a.limit {
			items = items[:a.limit]
		}
		hr := a.homeRow(r, items, len(r.items) > len(items))
		hr.Hidden = true
		out = append(out, hr)
	}
	return out
}

// anchored rows are defined by their exact titles (a franchise's
// remaining films, the next episodes, an owner's collection, the viewer's
// own watchlist): they keep their titles and similarity-driven rows give
// way.
func anchored(c *candRow) bool { return c.keepOrder && c.kind != KindContinue }

// fill picks each row's items with cross-row dedupe, so a title appears
// once per screen:
//
//  1. The front end's own rows keep their items.
//  2. Anchored rows keep their titles (the first anchored row wins a
//     title they share).
//  3. The other rows draft the remaining titles in turns, in display
//     order: each round, every row takes its best title nobody has taken.
//     Higher rows pick first, but no row is starved by a greedy one above
//     it (Top 10 cannot swallow every Because-you-watched title).
//
// Rows left with fewer than their minimum are dropped and the draft is
// rerun without them, so their titles go back to the others. maxRows
// counts engine and owner rows; the next row moves up when one drops.
func (a *assembly) fill(ordered []*rankedRow, maxRows int) []core.HomeRow {
	excluded := map[*rankedRow]bool{}
	const attempts = 6
	for attempt := 0; ; attempt++ {
		var sel []*rankedRow
		counted := 0
		for _, r := range ordered {
			if excluded[r] {
				continue
			}
			if r.c.source != core.RowSourcePlex {
				if maxRows > 0 && counted >= maxRows {
					continue
				}
				counted++
			}
			sel = append(sel, r)
		}
		picked := make(map[*rankedRow][]candItem, len(sel))
		claimed := map[core.RatingKey]bool{}
		for _, r := range sel {
			if r.c.source == core.RowSourcePlex {
				items := r.items
				if n := a.rowCap(r.c); len(items) > n {
					items = items[:n]
				}
				picked[r] = items
				for _, it := range items {
					claimed[it.t.Key] = true
				}
			}
		}
		for _, r := range sel {
			if r.c.source == core.RowSourcePlex || !anchored(r.c) {
				continue
			}
			for _, it := range r.items {
				if len(picked[r]) >= a.rowCap(r.c) {
					break
				}
				if !claimed[it.t.Key] {
					claimed[it.t.Key] = true
					picked[r] = append(picked[r], it)
				}
			}
		}
		cursor := make(map[*rankedRow]int, len(sel))
		for progress := true; progress; {
			progress = false
			for _, r := range sel {
				if r.c.source == core.RowSourcePlex || anchored(r.c) || len(picked[r]) >= a.rowCap(r.c) {
					continue
				}
				for cursor[r] < len(r.items) {
					it := r.items[cursor[r]]
					cursor[r]++
					if claimed[it.t.Key] {
						continue
					}
					claimed[it.t.Key] = true
					picked[r] = append(picked[r], it)
					progress = true
					break
				}
			}
		}
		thin := false
		for _, r := range sel {
			if r.c.source == core.RowSourcePlex {
				continue
			}
			min := r.c.minItems
			if r.d.pinned && min > 1 {
				min = 1
			}
			if n := len(picked[r]); n == 0 || n < min {
				excluded[r] = true
				thin = true
			}
		}
		if thin && attempt < attempts {
			continue
		}
		out := make([]core.HomeRow, 0, len(sel))
		for _, r := range sel {
			items := picked[r]
			if r.c.source != core.RowSourcePlex && (len(items) == 0 || excluded[r] && thin) {
				continue
			}
			out = append(out, a.homeRow(r, items, len(r.items) > len(items)))
		}
		return out
	}
}

// rowCap is the most items a row shows on this screen.
func (a *assembly) rowCap(c *candRow) int {
	if c.maxItems > 0 && c.maxItems < a.limit {
		return c.maxItems
	}
	return a.limit
}

// typeOK reports whether an item belongs on this screen.
func (a *assembly) typeOK(t *core.Title) bool {
	switch a.section {
	case core.TypeMovie:
		if t.Type != core.TypeMovie {
			return false
		}
	case core.TypeShow:
		if t.Type != core.TypeShow && t.Type != core.TypeSeason && t.Type != core.TypeEpisode {
			return false
		}
	}
	if a.allowed != nil {
		typ := t.Type
		if typ == core.TypeSeason || typ == core.TypeEpisode {
			typ = core.TypeShow
		}
		if !a.allowed(typ) {
			return false
		}
	}
	return recommend.ContentRatingAllowed(t.ContentRating, a.user.MaxContentRating)
}

// contextItems filters a row's items for this screen and viewer, scores
// them for the context and orders them (MMR-diversified unless the row's
// order is meaningful).
func (a *assembly) contextItems(c *candRow) []candItem {
	target := a.model.taste.targetMinutes(a.hc)
	out := make([]candItem, 0, len(c.items))
	for _, it := range c.items {
		if !a.typeOK(&it.t) {
			continue
		}
		if c.kind == KindShortOnTime {
			if m := minutes(&it.t); m <= 0 || m > math.Max(target*1.25, 30) {
				continue
			}
		}
		if !c.keepOrder {
			it.base += a.adjust(c, &it, target)
		}
		out = append(out, it)
	}
	if c.keepOrder {
		return out
	}
	sortItems(out)
	n := a.limit
	if c.maxItems > 0 && c.maxItems < n {
		n = c.maxItems
	}
	// Diversify the head; the tail is slack for cross-row dedupe.
	return mmr(out, min(len(out), 3*n+6), mmrLambda)
}

// adjust is the context-dependent part of an item's score.
func (a *assembly) adjust(c *candRow, it *candItem, target float64) float64 {
	t := &it.t
	adj := 0.0
	// Shown repeatedly without a play: demote so Home never goes stale.
	adj -= 0.12 * a.model.fatigue[t.Key]
	// What this viewer watches at this time of day.
	if lifts := a.model.taste.DaypartGenre[a.hc.Daypart]; len(lifts) > 0 {
		gs := normGenres(t.Genres)
		if len(gs) > 0 {
			sum := 0.0
			for _, g := range gs {
				sum += lifts[g]
			}
			adj += 0.08 * sum / float64(len(gs))
		}
	}
	// Runtime fit to the time the viewer has.
	if m := minutes(t); m > 0 && target > 0 {
		fitR := 1.0
		if m > target {
			d := (m - target) / target
			fitR = math.Exp(-2 * d * d)
		}
		switch {
		case c.kind == KindShortOnTime:
			adj += 0.35 * fitR
		case a.hc.Device == core.DeviceMobile || (!a.hc.Weekend && a.hc.Daypart == core.DaypartLate):
			adj += 0.05 * fitR
		}
	}
	// Daily rotation: a small, stable-per-day nudge so near-ties reorder.
	adj += 0.06 * (unit(a.user.ID, a.day, t.Key) - 0.5)
	return adj
}

// rowScore predicts the row's engagement for this viewer and context:
//
//	score = bandit play rate × context multiplier × content strength
//	        × row scale × daily rotation
func (a *assembly) rowScore(c *candRow, items []candItem) (score, strength float64) {
	info := infoFor(c.kind)
	rng := seeded("row", a.user.ID, a.day, a.hc.Bucket(), c.kind)
	_, rate := rowRate(info.prior, a.global[priorKind(c.kind)], a.arms[c.kind], a.explore, rng)

	strength = 0.8
	if c.kind != KindContinue && c.kind != KindUpNext {
		n := min(5, len(items))
		if n > 0 {
			sum := 0.0
			for _, it := range items[:n] {
				sum += 0.7*it.fit + 0.3*it.qual
			}
			strength = clamp(sum/float64(n), 0, 1)
		}
	}
	if c.kind == KindContinue && len(items) == 0 {
		return 0, 0
	}
	mult := ctxMult(c.kind, a.hc) * a.typeMix(items) * (0.7 + 0.6*strength)
	scale := c.scale
	if scale <= 0 {
		scale = 1
	}
	// Rotation between same-kind rows (which Because-you-watched rows show
	// today) and among close rows.
	rot := 1 + 0.16*(unit("rot", a.user.ID, a.day, c.id)-0.5)
	return rate * mult * scale * rot, strength
}

// ctxMult adjusts a row kind for the request context.
func ctxMult(kind string, hc core.HubContext) float64 {
	m := 1.0
	weeknight := !hc.Weekend
	switch kind {
	case KindShortOnTime:
		if weeknight {
			m *= 1.3
		}
		if hc.Device == core.DeviceMobile {
			m *= 1.4
		}
		if weeknight && hc.Daypart == core.DaypartLate {
			m *= 1.15
		}
		if hc.Weekend && hc.Daypart == core.DaypartEvening && hc.Device == core.DeviceTV {
			m *= 0.55
		}
	case KindComfort:
		if hc.Daypart == core.DaypartLate {
			m *= 1.3
		}
		if hc.Weekend && hc.Daypart == core.DaypartMorning {
			m *= 1.15
		}
	case KindNewThisWeek, KindHiddenGems:
		if hc.Weekend {
			m *= 1.15
		}
	case KindDifferent:
		if hc.Weekend {
			m *= 1.2
		}
		if hc.Daypart == core.DaypartLate {
			m *= 0.8
		}
	case KindTop10:
		if hc.Daypart == core.DaypartEvening {
			m *= 1.1
		}
	case KindUpNext:
		if weeknight && (hc.Daypart == core.DaypartEvening || hc.Daypart == core.DaypartLate) {
			m *= 1.15
		}
		if hc.Device == core.DeviceMobile {
			m *= 1.1
		}
	case KindContinue:
		if hc.Device == core.DeviceTV {
			m *= 1.1
		}
	}
	return m
}

// typeMix favours film-heavy rows where this viewer watches films in this
// context (and on weekend-evening TVs), and episode-heavy rows on phones
// and weeknights.
func (a *assembly) typeMix(items []candItem) float64 {
	n := min(10, len(items))
	if n == 0 {
		return 1
	}
	movies := 0
	for _, it := range items[:n] {
		if it.t.Type == core.TypeMovie {
			movies++
		}
	}
	dm := 2*float64(movies)/float64(n) - 1 // -1 all episodes/shows … +1 all films
	s := a.model.taste.movieShareFor(a.hc)
	m := 1 + 0.2*dm*(2*s-1)
	switch {
	case a.hc.Device == core.DeviceTV && a.hc.Weekend && a.hc.Daypart == core.DaypartEvening:
		m *= 1 + 0.25*dm
	case a.hc.Device == core.DeviceMobile:
		m *= 1 - 0.2*dm
	case !a.hc.Weekend && a.hc.Daypart == core.DaypartLate:
		m *= 1 - 0.1*dm
	}
	return m
}

// hubID is the row's Plex hub identifier on this screen.
func (a *assembly) hubID(c *candRow) string {
	if c.hub != nil && c.hub.ID != "" {
		return c.hub.ID
	}
	if c.kind == KindContinue {
		return "home.continue"
	}
	return sectionPrefix(a.section) + "." + c.id
}

func (a *assembly) homeRow(r *rankedRow, items []candItem, more bool) core.HomeRow {
	c := r.c
	prefix := sectionPrefix(a.section)
	h := core.Hub{ID: r.hubID, Title: c.title, Context: "hub." + prefix + "." + c.context, Style: "shelf", Promoted: true, More: more, Key: c.collKey}
	if c.hub != nil {
		if c.hub.Context != "" {
			h.Context = c.hub.Context
		}
		if c.hub.Style != "" {
			h.Style = c.hub.Style
		}
		if c.hub.Key != 0 {
			h.Key = c.hub.Key
		}
		h.More = h.More || c.hub.More
	}
	titles := make([]core.Title, len(items))
	reasons := make(map[core.RatingKey]string, len(items))
	for i, it := range items {
		titles[i] = it.t
		if it.reason != "" {
			reasons[it.t.Key] = it.reason
		}
	}
	h.Items = titles
	h.Type = rowType(titles, a.section)
	return core.HomeRow{
		HubID: r.hubID, Title: c.title, Kind: c.kind, Reason: c.reason, Score: round3(r.score),
		Pinned: r.d.pinned, Source: c.source, Items: titles, ItemReasons: reasons, Hub: h,
	}
}

// rowType is the hub's item type: the common type of its items, or mixed
// ("") on Home.
func rowType(items []core.Title, section core.MediaType) core.MediaType {
	if len(items) == 0 {
		return section
	}
	t := items[0].Type
	for _, it := range items[1:] {
		if it.Type != t {
			if section != "" {
				return section
			}
			return ""
		}
	}
	return t
}

// ---- MMR ------------------------------------------------------------------

// features are the parts of a title MMR compares.
type features struct {
	genres    map[string]bool
	coll      int64
	directors map[int64]bool
	typ       core.MediaType
}

func featuresOf(t *core.Title) features {
	f := features{genres: map[string]bool{}, coll: t.CollectionID, directors: map[int64]bool{}, typ: t.Type}
	for _, g := range normGenres(t.Genres) {
		f.genres[g] = true
	}
	for _, d := range t.Directors {
		if d.TMDB != 0 {
			f.directors[d.TMDB] = true
		}
	}
	return f
}

// similarity is redundancy between two titles, 0..1: genre overlap
// (Jaccard), same franchise, same director.
func similarity(a, b features) float64 {
	inter := 0
	for g := range a.genres {
		if b.genres[g] {
			inter++
		}
	}
	union := len(a.genres) + len(b.genres) - inter
	s := 0.0
	if union > 0 {
		s = 0.6 * float64(inter) / float64(union)
	}
	if a.coll != 0 && a.coll == b.coll {
		s += 0.5
	}
	for d := range a.directors {
		if b.directors[d] {
			s += 0.25
			break
		}
	}
	return math.Min(s, 1)
}

// mmr reorders the best k items by Maximal Marginal Relevance: each pick
// maximises λ·relevance − (1−λ)·max similarity to the items already
// picked, so a row is not one franchise or one genre wall-to-wall.
// Deterministic: ties break by score, then key.
func mmr(items []candItem, k int, lambda float64) []candItem {
	if k <= 0 || len(items) == 0 {
		return nil
	}
	if k > len(items) {
		k = len(items)
	}
	// Consider a bounded window beyond k so diversity can pull in slightly
	// lower-scored items without scanning everything.
	window := min(len(items), 2*k+10)
	cand := append([]candItem(nil), items[:window]...)
	lo, hi := cand[0].base, cand[0].base
	for _, c := range cand {
		lo, hi = math.Min(lo, c.base), math.Max(hi, c.base)
	}
	// Relevance is normalised over at least half the score scale, so a
	// narrow band of near-equal scores does not turn tiny gaps into
	// decisive ones.
	span := math.Max(hi-lo, 0.5)
	feats := make([]features, len(cand))
	for i := range cand {
		feats[i] = featuresOf(&cand[i].t)
	}
	maxSim := make([]float64, len(cand))
	used := make([]bool, len(cand))
	out := make([]candItem, 0, k)
	for len(out) < k {
		best, bestVal := -1, math.Inf(-1)
		for i := range cand {
			if used[i] {
				continue
			}
			v := lambda*(cand[i].base-lo)/span - (1-lambda)*maxSim[i]
			if best < 0 || v > bestVal || (v == bestVal && (cand[i].base > cand[best].base ||
				(cand[i].base == cand[best].base && cand[i].t.Key < cand[best].t.Key))) {
				best, bestVal = i, v
			}
		}
		if best < 0 {
			break
		}
		used[best] = true
		out = append(out, cand[best])
		for i := range cand {
			if !used[i] {
				if s := similarity(feats[i], feats[best]); s > maxSim[i] {
					maxSim[i] = s
				}
			}
		}
	}
	return out
}
