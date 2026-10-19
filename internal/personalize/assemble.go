package personalize

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/recommend"
)

// assembly turns a precomputed userModel into the rows one request
// receives. It is pure: same inputs, same rows. Everything time-dependent
// (rotation, exploration) is seeded by the local date.
//
// It composes the page in the Home Designer's three layers: the Global
// template and the viewer's overrides (layout) decide which rows may
// appear, which must, where some are anchored and within which bounds
// the rest may move; personalization ranks the eligible rows for this
// viewer and context and fills the page.
type assembly struct {
	model   *userModel
	owner   []*candRow // owner collection rows for this context
	cw      []candItem // live Continue Watching items (Home only)
	user    core.User  // the requesting viewer
	hc      core.HubContext
	section core.MediaType // "" = Home
	limit   int            // items per row
	layout  *layoutIndex   // nil = engine defaults
	global  map[string]arm // server-wide evidence by prior kind
	arms    map[string]arm // this viewer's evidence by kind
	allowed func(core.MediaType) bool
	maxRows int // engine default page length
	explore float64
	day     int
	// days are the local days (YYYYMMDD) each row kind was shown to this
	// viewer in the last week (frequency caps).
	days map[string][]int
	// explain lists the rows that are not on the page, with reasons
	// (owner previews); Plex never receives them.
	explain bool
}

// rankedRow is a candidate row during assembly.
type rankedRow struct {
	c        *candRow
	items    []candItem
	hubID    string
	score    float64
	strength float64
	pol      effPolicy
	must     bool // Always or anchored: on the page whenever it has titles
	// hidden says why the row is not on the page ("" = it is);
	// hiddenLayer is the layer responsible.
	hidden      string
	hiddenLayer string
	slot        int
	placement   string
}

func (r *rankedRow) hide(layer, why string) {
	r.hidden, r.hiddenLayer = why, layer
}

// mmrLambda trades relevance against redundancy inside a row.
const mmrLambda = 0.75

// maxOwnerRows caps owner collection rows on one screen (unless a policy
// sets its own cap or places them).
const maxOwnerRows = 4

func (a *assembly) run() []core.HomeRow {
	pv := a.layout.page(a.user.ID, sectionName(a.section))
	var rows, hidden []*rankedRow
	cands := append([]*candRow{}, a.model.rows...)
	cands = append(cands, a.owner...)
	// New viewers get the curated cold-start rows (Top 10 today, genre
	// showcases) and acclaimed section rows on Home: there is little
	// personal to put there yet.
	newViewer := a.newViewer()
	if a.section == "" && !hasKind(cands, KindContinue) {
		// The engine always places Continue Watching on Home (the generic
		// model has no row for it), so slots mean the same for everyone.
		cands = append(cands, &candRow{kind: KindContinue, id: "continue", title: "Continue Watching",
			reason: "Titles you're part-way through, most recent first.", context: "continue",
			source: core.RowSourcePlex, keepOrder: true, homeOnly: true, scale: 1})
	}
	for _, c := range cands {
		if a.section == "" && c.sectionOnly && !(newViewer && c.coldHome) || a.section != "" && c.homeOnly {
			continue
		}
		if c.coldOnly && !newViewer {
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
		rr.pol = pv.policyFor(rr.hubID, c.kind)
		rr.score, rr.strength = a.rowScore(c, items)
		if p := rr.pol.p; p != nil {
			if p.Priority > 0 {
				rr.score *= p.Priority
			}
			rr.must = p.Status == core.RowAlways || p.Slot > 0
			// The front end always shows its own rows: policies place
			// them but cannot remove them.
			if c.source != core.RowSourcePlex {
				switch {
				case p.Status == core.RowOff:
					rr.hide(rr.pol.layer, "Off "+a.where(rr.pol.layer)+".")
				case !whenMatches(p.When, a.hc):
					rr.hide(rr.pol.layer, "Scheduled for "+describeWhen(p.When)+" "+a.where(rr.pol.layer)+"; not now.")
				case p.MaxDaysPerWeek > 0:
					if shown, ok := a.frequencyOK(c.kind, p.MaxDaysPerWeek); !ok {
						rr.hide(rr.pol.layer, fmt.Sprintf("Frequency cap %s: at most %d days a week, and it was shown on %d of the last 6 days.",
							a.where(rr.pol.layer), p.MaxDaysPerWeek, shown))
					}
				}
			}
		}
		if rr.hidden != "" {
			hidden = append(hidden, rr)
			continue
		}
		rows = append(rows, rr)
	}

	// Rows that must be on the page claim their caps first, then the rest
	// by predicted engagement.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].must != rows[j].must {
			return rows[i].must
		}
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		return rows[i].hubID < rows[j].hubID
	})
	perGroup := map[string]int{}
	clusters := map[string]bool{}
	owners := 0
	pool := rows[:0:0]
	for _, r := range rows {
		group, limit, label := a.capFor(r)
		if perGroup[group] >= limit {
			r.hide(r.pol.layer, fmt.Sprintf("At most %d %s per page%s; higher-ranked ones took the places.", limit, label, a.capWhere(r)))
			hidden = append(hidden, r)
			continue
		}
		if r.c.group != "" && !r.must {
			if clusters[r.c.kind+"/"+r.c.group] {
				r.hide("", "One showcase per genre family; another from this family ranked higher.")
				hidden = append(hidden, r)
				continue
			}
			clusters[r.c.kind+"/"+r.c.group] = true
		}
		ownerCapped := r.c.source == core.RowSourceOwner && !r.must && (r.pol.p == nil || r.pol.p.MaxPerPage == 0)
		if ownerCapped && owners >= maxOwnerRows {
			r.hide("", fmt.Sprintf("At most %d collection rows per page; higher-ranked ones took the places.", maxOwnerRows))
			hidden = append(hidden, r)
			continue
		}
		perGroup[group]++
		if r.c.source == core.RowSourceOwner {
			owners++
		}
		pool = append(pool, r)
	}

	maxRows, maxLayer := pv.maxRows(a.hc.Device, a.maxRows)
	out, dropped := a.fill(pool, maxRows, maxLayer)
	hidden = append(hidden, dropped...)
	if !a.explain {
		return out
	}
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

func hasKind(rows []*candRow, kind string) bool {
	for _, r := range rows {
		if r.kind == kind {
			return true
		}
	}
	return false
}

// capFor is the per-page cap that applies to a row: its policy's
// MaxPerPage (counted over the rows that policy matches), else the
// engine's cap for its kind.
func (a *assembly) capFor(r *rankedRow) (group string, limit int, label string) {
	if p := r.pol.p; p != nil && p.MaxPerPage > 0 {
		return "policy:" + r.pol.group(), p.MaxPerPage, rowsLabel(p.Row, p.MaxPerPage)
	}
	info := infoFor(r.c.kind)
	return "kind:" + r.c.kind, info.cap, rowsLabel(r.c.kind, info.cap)
}

func (a *assembly) capWhere(r *rankedRow) string {
	if p := r.pol.p; p != nil && p.MaxPerPage > 0 {
		return " " + a.where(r.pol.layer)
	}
	return ""
}

func rowsLabel(row string, n int) string {
	title := infoFor(row).title
	if title == row || strings.ContainsAny(row, "*?[") {
		title = "“" + row + "”"
	} else {
		title = "“" + strings.TrimRight(title, "…") + "”"
	}
	if n == 1 {
		return title + " row"
	}
	return title + " rows"
}

// where names a layer for explanations.
func (a *assembly) where(layer string) string {
	switch layer {
	case core.LayerGlobal:
		return "in Global Home"
	case core.LayerOverride:
		return "for " + a.viewerName() + " (override)"
	}
	return "by default"
}

func (a *assembly) viewerName() string {
	if a.user.ID == genericID {
		return "the typical viewer"
	}
	if a.user.Title != "" {
		return a.user.Title
	}
	if a.user.Username != "" {
		return a.user.Username
	}
	return "this viewer"
}

// frequencyOK applies a days-per-week cap: a row shown today stays for
// the day; otherwise it may appear if it was shown on fewer than max of
// the previous six days.
func (a *assembly) frequencyOK(kind string, maxDays int) (int, bool) {
	days := a.days[kind]
	if containsInt(days, a.day) {
		return 0, true
	}
	shown := 0
	for k := 1; k <= 6; k++ {
		if containsInt(days, dayOf(a.hc.Now.AddDate(0, 0, -k), a.hc.Now.Location())) {
			shown++
		}
	}
	return shown, shown < maxDays
}

// anchored rows are defined by their exact titles (a franchise's
// remaining films, the next episodes, an owner's collection, the viewer's
// own watchlist): they keep their titles and similarity-driven rows give
// way.
func anchored(c *candRow) bool { return c.keepOrder && c.kind != KindContinue }

// fill chooses the page and each row's items, with cross-row dedupe so a
// title appears once per screen:
//
//  1. The page: every row that must be there (Always, anchored), then the
//     eligible rows by predicted engagement until the page length
//     (maxRows; Plex's own rows not counted). The slot solver
//     (placeSlots) arranges them: anchors, bounds, rank order.
//  2. The front end's own rows keep their items; anchored-content rows
//     (franchise, Up Next, owner collections, watchlist) keep their
//     titles (the first such row wins a title they share).
//  3. The other rows draft the remaining titles in turns, in display
//     order: each round, every row takes its best title nobody has taken.
//     Higher rows pick first, but no row is starved by a greedy one above
//     it (Top 10 cannot swallow every Because-you-watched title).
//
// Rows left with fewer than their minimum are dropped and the page is
// rebuilt without them, so their titles go back to the others and the
// next eligible row moves up. It returns the page and the rows that did
// not make it, with reasons.
func (a *assembly) fill(pool []*rankedRow, maxRows int, maxLayer string) ([]core.HomeRow, []*rankedRow) {
	excluded := map[*rankedRow]string{}
	const attempts = 6
	for attempt := 0; ; attempt++ {
		var sel, cut []*rankedRow
		counted := 0
		for _, r := range pool {
			if excluded[r] != "" {
				continue
			}
			if r.c.source != core.RowSourcePlex {
				if !r.must && maxRows > 0 && counted >= maxRows {
					cut = append(cut, r)
					continue
				}
				counted++
			}
			sel = append(sel, r)
		}
		order, tooLow := a.place(sel, attempt >= attempts)
		if len(tooLow) > 0 && attempt < attempts {
			for _, r := range tooLow {
				excluded[r] = fmt.Sprintf("Never above slot %d %s, and the page has fewer rows.", r.pol.p.MinSlot, a.where(r.pol.layer))
			}
			continue
		}
		picked := make(map[*rankedRow][]candItem, len(order))
		claimed := map[core.RatingKey]bool{}
		for _, r := range order {
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
		for _, r := range order {
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
		cursor := make(map[*rankedRow]int, len(order))
		for progress := true; progress; {
			progress = false
			for _, r := range order {
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
		for _, r := range order {
			if r.c.source == core.RowSourcePlex {
				continue
			}
			min := r.c.minItems
			if r.must && min > 1 {
				min = 1
			}
			if n := len(picked[r]); n == 0 || n < min {
				excluded[r] = fmt.Sprintf("Too few titles left after removing ones shown in rows above (%d, needs %d).", n, max(min, 1))
				thin = true
			}
		}
		if thin && attempt < attempts {
			continue
		}
		out := make([]core.HomeRow, 0, len(order))
		var dropped []*rankedRow
		for _, r := range order {
			items := picked[r]
			if r.c.source != core.RowSourcePlex && (len(items) == 0 || excluded[r] != "") {
				r.hide("", excluded[r])
				dropped = append(dropped, r)
				continue
			}
			out = append(out, a.homeRow(r, items, len(r.items) > len(items)))
		}
		for _, r := range pool {
			if why := excluded[r]; why != "" && r.hidden == "" {
				layer := ""
				if strings.HasPrefix(why, "Never above") {
					layer = r.pol.layer
				}
				r.hide(layer, why)
				dropped = append(dropped, r)
			}
		}
		for _, r := range cut {
			where := "the engine default"
			if maxLayer != "" {
				where = a.where(maxLayer)
			}
			r.hide(maxLayer, fmt.Sprintf("Didn't make the page: %d rows at most (%s), and higher-ranked rows filled it.", maxRows, where))
			dropped = append(dropped, r)
		}
		return out, dropped
	}
}

// place arranges the selected rows into slots. Eligible rows whose
// "never above" bound lies beyond the end of the page are returned
// instead (they cannot be shown honestly), unless force is set (the last
// attempt), when they sit as low as the page allows.
func (a *assembly) place(sel []*rankedRow, force bool) ([]*rankedRow, []*rankedRow) {
	ranked := append([]*rankedRow(nil), sel...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].hubID < ranked[j].hubID
	})
	n := len(ranked)
	var tooLow []*rankedRow
	for _, r := range ranked {
		if p := r.pol.p; p != nil && !r.must && p.MinSlot > n {
			tooLow = append(tooLow, r)
		}
	}
	if len(tooLow) > 0 && !force {
		return nil, tooLow
	}
	reqs := make([]slotReq, n)
	bounded := false
	for i, r := range ranked {
		if p := r.pol.p; p != nil {
			reqs[i] = slotReq{anchor: p.Slot, lo: min(p.MinSlot, n), hi: p.MaxSlot, pri: 1}
			if r.pol.layer == core.LayerOverride {
				reqs[i].pri = 2
			}
			bounded = bounded || p.MinSlot > 0 || p.MaxSlot > 0
		}
	}
	outcome := placeSlots(reqs)
	natural := outcome
	if bounded {
		free := make([]slotReq, n)
		for i, q := range reqs {
			free[i] = slotReq{anchor: q.anchor, pri: q.pri}
		}
		natural = placeSlots(free)
	}
	order := make([]*rankedRow, n)
	for i, r := range ranked {
		o := outcome[i]
		r.slot = o.slot
		r.placement = a.placementOf(r, i+1, n, o, natural[i].slot)
		order[o.slot-1] = r
	}
	return order, nil
}

// placementOf says why a row sits at its slot.
func (a *assembly) placementOf(r *rankedRow, rank, n int, o slotOutcome, natural int) string {
	p := r.pol.p
	if p != nil && p.Slot > 0 {
		s := fmt.Sprintf("Anchored to slot %d %s", p.Slot, a.where(r.pol.layer))
		switch {
		case o.clamped && !o.displaced:
			s += fmt.Sprintf("; the page has %d rows, so it is last", n)
		case o.displaced:
			s += fmt.Sprintf("; another anchor holds that slot, so it sits at slot %d", o.slot)
		}
		return s + "."
	}
	var b strings.Builder
	if r.must {
		fmt.Fprintf(&b, "Always on the page %s; ", a.where(r.pol.layer))
	} else if r.c.source == core.RowSourcePlex {
		b.WriteString("Plex's own row; ")
	}
	fmt.Fprintf(&b, "%s of %d by predicted engagement (score %.2f)", ordinal(rank), n, r.score)
	if p != nil && p.Priority > 0 {
		fmt.Fprintf(&b, ", priority ×%g %s", p.Priority, a.where(r.pol.layer))
	}
	switch {
	case o.missedHi:
		fmt.Fprintf(&b, "; could not honour never below slot %d: anchors and other bounds hold the slots above", p.MaxSlot)
	case o.missedLo:
		fmt.Fprintf(&b, "; could not honour never above slot %d: too few rows to fill the slots above", p.MinSlot)
	case p != nil && p.MaxSlot > 0 && o.slot < natural:
		fmt.Fprintf(&b, "; moved up to slot %d: never below slot %d %s", o.slot, p.MaxSlot, a.where(r.pol.layer))
	case p != nil && p.MinSlot > 0 && o.slot > natural:
		fmt.Fprintf(&b, "; held down to slot %d: never above slot %d %s", o.slot, p.MinSlot, a.where(r.pol.layer))
	case o.slot != rank:
		fmt.Fprintf(&b, "; at slot %d around anchored and bounded rows", o.slot)
	}
	b.WriteString(".")
	return b.String()
}

// rowCap is the most items a row shows on this screen.
func (a *assembly) rowCap(c *candRow) int {
	if c.maxItems > 0 && c.maxItems < a.limit {
		return c.maxItems
	}
	return a.limit
}

// newViewer reports whether the viewer's taste is still too thin for a
// personal Home.
func (a *assembly) newViewer() bool {
	return a.model.taste == nil || a.model.taste.Confidence < coldStartConfidence
}

// coldBoost lifts the curated cold-start rows on a new viewer's Home so
// it leads with Top 10 today, genre showcases and new releases.
func coldBoost(kind string) float64 {
	switch kind {
	case KindTop10Today:
		return 1.8
	case KindShowcase:
		return 1.5
	case KindNewReleases:
		return 1.6
	}
	return 1
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
		if c.kind == KindShortOnTime && !isShort(&it.t) {
			continue // the context target orders the row (adjust), isShort bounds it
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
			// Films or episodes, as this viewer watches in this context.
			share := 2*a.model.taste.movieShareFor(a.hc) - 1
			if t.Type == core.TypeMovie {
				adj += 0.1 * share
			} else {
				adj -= 0.1 * share
			}
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
	if a.newViewer() {
		mult *= coldBoost(c.kind)
	}
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
	hr := core.HomeRow{
		HubID: r.hubID, Title: c.title, Kind: c.kind, Reason: c.reason, Score: round3(r.score),
		Source: c.source, Items: titles, ItemReasons: reasons, Hub: h,
		Layer: core.LayerPersonalized, Slot: r.slot, Placement: r.placement, HiddenReason: r.hidden,
	}
	if p := r.pol.p; p != nil {
		pol := clonePolicy(*p)
		hr.Policy, hr.PolicyLayer, hr.AnchorSlot = &pol, r.pol.layer, p.Slot
		hr.Pinned = p.Slot > 0
		switch {
		case r.pol.layer == core.LayerOverride:
			hr.Layer = core.LayerOverride
		case r.must || p.Status == core.RowOff:
			hr.Layer = core.LayerGlobal
		}
	}
	if r.hidden != "" {
		hr.Slot, hr.Placement = 0, ""
		if r.hiddenLayer != "" {
			hr.Layer = r.hiddenLayer
		}
	}
	return hr
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
