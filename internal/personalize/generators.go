package personalize

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/recommend"
)

// generator proposes candidate rows of one kind.
type generator struct {
	kind string
	run  func(b *builder) []*candRow
}

// generators run in this order; order only matters for logging, since
// assembly ranks rows.
var generators = []generator{
	{KindContinue, (*builder).genContinue},
	{KindUpNext, (*builder).genUpNext},
	{KindTop10, (*builder).genTop10},
	{KindBecause, (*builder).genBecause},
	{KindPerson, (*builder).genPerson},
	{KindTrendingTaste, (*builder).genTrendingTaste},
	{KindHiddenGems, (*builder).genHiddenGems},
	{KindNewThisWeek, (*builder).genNewThisWeek},
	{KindComfort, (*builder).genComfort},
	{KindDifferent, (*builder).genDifferent},
	{KindShortOnTime, (*builder).genShortOnTime},
	{KindFranchise, (*builder).genFranchise},
	{KindLLM, (*builder).genLLM},
	{KindTraktRecs, (*builder).genTrakt},
	{KindTrending, (*builder).genTrending},
	{KindPopularServer, (*builder).genPopularServer},
	{KindNewReleases, (*builder).genNewReleases},
	{KindTopRated, (*builder).genTopRated},
	{KindPopular, (*builder).genPopular},
	{KindAiringToday, (*builder).genAiringToday},
}

func newRow(kind, id, title, reason string, items []candItem) *candRow {
	info := infoFor(kind)
	return &candRow{kind: kind, id: id, title: title, reason: reason, context: info.context, source: info.source,
		items: items, minItems: 4, scale: 1}
}

// ---- Continue Watching and Up Next --------------------------------------------

// genContinue mirrors the Plex front end's Continue Watching so the engine
// can rank it; the front end renders its own copy where the engine puts it.
func (b *builder) genContinue() []*candRow {
	if b.generic || b.e.state == nil {
		return nil
	}
	items := b.e.continueItems(b.ctx, &b.user, 50)
	r := newRow(KindContinue, "continue", "Continue Watching", "Titles you're part-way through, most recent first.", items)
	r.keepOrder, r.homeOnly, r.minItems = true, true, 0
	return []*candRow{r}
}

// continueItems lists in-progress titles, most recent first.
func (e *Engine) continueItems(ctx context.Context, user *core.User, limit int) []candItem {
	if e.state == nil || user == nil {
		return nil
	}
	sts, err := e.state.InProgress(ctx, user.ID, limit)
	if err != nil {
		e.log.Debug("personalize: in progress failed", "err", err)
		return nil
	}
	var out []candItem
	for i, ws := range sts {
		t, err := e.catalog.Get(ctx, ws.Key)
		if err != nil || t == nil {
			continue
		}
		reason := "You stopped part-way through"
		if ws.Duration > 0 {
			reason = fmt.Sprintf("%d%% watched", int(math.Round(100*float64(ws.ViewOffset)/float64(ws.Duration))))
		}
		out = append(out, candItem{t: *t, base: rankScore(i, len(sts)), qual: quality(t), reason: reason})
	}
	return out
}

func (b *builder) genUpNext() []*candRow {
	if b.generic || b.e.state == nil {
		return nil
	}
	hist, err := b.e.state.History(b.ctx, b.user.ID, time.Time{}, 300)
	if err != nil {
		return nil
	}
	seenShow := map[int64]bool{}
	var items []candItem
	for _, h := range hist {
		if !h.Completed || h.Key.Kind() != core.KindEpisode || seenShow[h.Key.TMDB()] {
			continue
		}
		seenShow[h.Key.TMDB()] = true
		next, err := b.e.catalog.NextEpisode(b.ctx, h.Key)
		if err != nil || next == nil {
			continue
		}
		if !next.Released.IsZero() && next.Released.After(b.now) {
			continue
		}
		if ws, err := b.e.state.Get(b.ctx, b.user.ID, next.Key); err == nil && (ws.ViewCount > 0 || ws.ViewOffset > 0) {
			continue
		}
		if !recommend.ContentRatingAllowed(next.ContentRating, b.user.MaxContentRating) {
			continue
		}
		show := next.ShowTitle
		if show == "" {
			show = "this show"
		}
		items = append(items, candItem{t: *next, base: 1 - float64(len(items))*0.02, qual: 0.6,
			reason: fmt.Sprintf("Next episode of %s", show)})
		if len(items) >= 30 {
			break
		}
	}
	r := newRow(KindUpNext, "ondeck", "Up Next", "The next episode of shows you're following.", items)
	r.keepOrder, r.minItems = true, 1
	return []*candRow{r}
}

// ---- personal pool ---------------------------------------------------------------

// seeds are liked titles, most recent first.
func (b *builder) seeds(n int) []Evidence {
	var out []Evidence
	for _, e := range b.taste.Evidence {
		if e.Weight >= 0.4 && (e.Kind == EvidenceFinished || e.Kind == EvidenceRated || (e.Kind == EvidenceWatching && e.Progress >= 0.5)) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// pool is the shared candidate pool for taste-ranked rows: similar to
// recent favourites, trending, popular, top rated, and the favourite
// genres' best.
func (b *builder) pool() []core.Title {
	if v, ok := b.memo["pool"]; ok {
		return v
	}
	var all []core.Title
	for _, s := range b.seeds(8) {
		all = append(all, b.similar(s.Key, 20)...)
	}
	all = append(all, b.both(func(t core.MediaType) []core.Title { return b.list("trending_week", t, 40) })...)
	all = append(all, b.both(func(t core.MediaType) []core.Title { return b.list("top_rated", t, 40) })...)
	all = append(all, b.both(func(t core.MediaType) []core.Title {
		return b.browse(core.BrowseQuery{Filter: core.Filter{Type: t}, Sort: core.SortPopularity, Desc: true, Limit: 60})
	})...)
	for _, g := range b.taste.TopGenres(3) {
		all = append(all, b.browse(core.BrowseQuery{Filter: core.Filter{Genres: []string{g}}, Sort: core.SortPopularity, Desc: true, Limit: 40})...)
		all = append(all, b.browse(core.BrowseQuery{Filter: core.Filter{Genres: []string{g}, MinRating: 7}, Sort: core.SortRating, Desc: true, Limit: 30})...)
	}
	b.memo["pool"] = all
	return all
}

// ---- taste rows ------------------------------------------------------------------

func (b *builder) genTop10() []*candRow {
	if !b.personal() {
		return nil
	}
	items := b.rank(b.pool(), func(t *core.Title, _, _ int, fit float64, why string) (float64, string) {
		if why == "" {
			why = "Highly rated and widely loved"
		}
		return 0.62*fit + 0.2*quality(t) + 0.1*popularity(t) + 0.08*recency(t, b.now), why
	})
	kept := items[:0:0]
	for _, it := range items {
		if it.fit > 0 {
			kept = append(kept, it)
		}
	}
	r := newRow(KindTop10, "top10", "Top 10 for You", "The ten titles your taste model rates highest right now.", capItems(kept, 40))
	r.maxItems, r.minItems = 10, 5
	return []*candRow{r}
}

func (b *builder) genBecause() []*candRow {
	if !b.personal() {
		return nil
	}
	var rows []*candRow
	for i, s := range b.seeds(6) {
		seed := s.Title
		sim := b.similar(s.Key, 30)
		items := b.rank(sim, func(t *core.Title, idx, n int, fit float64, _ string) (float64, string) {
			return 0.45*fit + 0.35*rankScore(idx, n) + 0.2*quality(t), "Because you watched " + seed.Title
		})
		if len(items) == 0 {
			continue
		}
		verb := "finished"
		if s.Kind == EvidenceRated {
			verb = fmt.Sprintf("rated %g/10", s.Rating)
		} else if s.Kind == EvidenceWatching {
			verb = "are watching"
		}
		r := newRow(KindBecause, fmt.Sprintf("because.%d", int64(s.Key)), "Because You Watched "+seed.Title,
			fmt.Sprintf("You %s %s%s; these are the closest matches you haven't seen.", verb, seed.Title, agoPhrase(s.At, b.now)),
			capItems(items, 40))
		r.scale = 1 - 0.07*float64(i)
		rows = append(rows, r)
	}
	return rows
}

func agoPhrase(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < 36*time.Hour:
		return " recently"
	case d < 14*24*time.Hour:
		return fmt.Sprintf(" %d days ago", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf(" %d weeks ago", int(d.Hours()/24/7))
	}
	return fmt.Sprintf(" %d months ago", int(d.Hours()/24/30))
}

func (b *builder) genPerson() []*candRow {
	if !b.personal() {
		return nil
	}
	var rows []*candRow
	for _, kv := range sortedByWeight(b.taste.Person, 0) {
		if len(rows) >= 4 {
			break
		}
		info := b.taste.People[kv.k]
		if kv.w < 0.35 || info.name == "" {
			continue
		}
		// How many titles the viewer finished with this person.
		credits := 0
		for _, e := range b.taste.Evidence {
			if e.Kind == EvidenceFinished && hasPerson(&e.Title, kv.k) {
				credits++
			}
		}
		works := b.browse(core.BrowseQuery{Filter: core.Filter{Person: kv.k}, Sort: core.SortPopularity, Desc: true, Limit: 40})
		name := info.name
		items := b.rank(works, func(t *core.Title, _, _ int, fit float64, _ string) (float64, string) {
			return 0.45*fit + 0.3*quality(t) + 0.25*popularity(t), personVerb(info.role, t, kv.k) + " " + name
		})
		if len(items) == 0 {
			continue
		}
		title := "More with " + name
		switch info.role {
		case "director":
			title = "More from " + name
		case "writer":
			title = "More Written by " + name
		}
		reason := fmt.Sprintf("%s is one of your most-watched %ss", name, roleNoun(info.role))
		if credits > 1 {
			reason = fmt.Sprintf("You've finished %d titles with %s", credits, name)
		}
		r := newRow(KindPerson, fmt.Sprintf("person.%d", kv.k), title, reason, capItems(items, 40))
		r.scale = 0.8 + 0.2*kv.w
		rows = append(rows, r)
	}
	return rows
}

func hasPerson(t *core.Title, id int64) bool {
	for _, ps := range [][]core.Person{t.Directors, t.Writers, t.Cast} {
		for _, p := range ps {
			if p.TMDB == id {
				return true
			}
		}
	}
	return false
}

func personVerb(role string, t *core.Title, id int64) string {
	for _, d := range t.Directors {
		if d.TMDB == id {
			return "Directed by"
		}
	}
	for _, w := range t.Writers {
		if w.TMDB == id {
			return "Written by"
		}
	}
	return "With"
}

func roleNoun(role string) string {
	switch role {
	case "director", "writer":
		return role
	}
	return "actor"
}

func (b *builder) genTrendingTaste() []*candRow {
	if !b.personal() {
		return nil
	}
	src := append(b.both(func(t core.MediaType) []core.Title { return b.list("trending_week", t, 40) }),
		b.both(func(t core.MediaType) []core.Title { return b.list("trending_day", t, 20) })...)
	items := b.rank(src, func(t *core.Title, idx, n int, fit float64, why string) (float64, string) {
		if why == "" {
			why = "Trending this week"
		} else {
			why = "Trending · " + why
		}
		return 0.55*fit + 0.35*rankScore(idx, n) + 0.1*quality(t), why
	})
	kept := items[:0:0]
	for _, it := range items {
		if it.fit > 0.08 {
			kept = append(kept, it)
		}
	}
	r := newRow(KindTrendingTaste, "trending.taste", "Trending in Your Taste",
		"This week's trending titles that match what you like.", capItems(kept, 40))
	r.minItems = 5
	return []*candRow{r}
}

func (b *builder) genHiddenGems() []*candRow {
	if !b.personal() {
		return nil
	}
	var src []core.Title
	for _, g := range b.taste.TopGenres(4) {
		src = append(src, b.browse(core.BrowseQuery{Filter: core.Filter{Genres: []string{g}, MinRating: 7.2}, Sort: core.SortRating, Desc: true, Limit: 60})...)
	}
	src = append(src, b.pool()...)
	// "Little-known" relative to this catalogue: under the 40th popularity
	// percentile of the candidates.
	var pops []float64
	for _, t := range src {
		if t.Popularity > 0 {
			pops = append(pops, t.Popularity)
		}
	}
	sort.Float64s(pops)
	popCap := math.Inf(1)
	if len(pops) >= 10 {
		popCap = pops[len(pops)*2/5]
	}
	var gems []core.Title
	for _, t := range src {
		if t.Rating >= 7.2 && (t.VoteCount == 0 || t.VoteCount >= 50) && t.Popularity <= popCap {
			gems = append(gems, t)
		}
	}
	items := b.rank(gems, func(t *core.Title, _, _ int, fit float64, why string) (float64, string) {
		r := fmt.Sprintf("Rated %.1f but little known", t.Rating)
		if why != "" {
			r += " · " + why
		}
		return 0.5*fit + 0.4*quality(t) + 0.1*(1-popularity(t)), r
	})
	kept := items[:0:0]
	for _, it := range items {
		if it.fit >= 0.12 {
			kept = append(kept, it)
		}
	}
	r := newRow(KindHiddenGems, "hiddengems", "Hidden Gems for You",
		"Highly rated titles few people have found, that fit your taste.", capItems(kept, 40))
	r.minItems = 5
	return []*candRow{r}
}

func (b *builder) genNewThisWeek() []*candRow {
	if !b.personal() {
		return nil
	}
	genres := b.taste.TopGenres(3)
	if len(genres) == 0 {
		return nil
	}
	var src []core.Title
	for _, g := range genres {
		src = append(src, b.browse(core.BrowseQuery{Filter: core.Filter{Genres: []string{g}}, Sort: core.SortReleased, Desc: true, Limit: 40})...)
	}
	within := func(days int) []core.Title {
		var out []core.Title
		for _, t := range src {
			if !t.Released.IsZero() && !t.Released.After(b.now) && b.now.Sub(t.Released) <= time.Duration(days)*24*time.Hour {
				out = append(out, t)
			}
		}
		return out
	}
	fresh := within(7)
	window := "this week"
	if len(fresh) < 6 {
		fresh, window = within(21), "in the last three weeks"
	}
	items := b.rank(fresh, func(t *core.Title, _, _ int, fit float64, why string) (float64, string) {
		r := "New " + window
		if why != "" {
			r += " · " + why
		}
		return 0.5*fit + 0.3*recency(t, b.now) + 0.2*quality(t), r
	})
	title := "New This Week in " + genres[0]
	if len(genres) > 1 {
		title += " & " + genres[1]
	}
	r := newRow(KindNewThisWeek, "newthisweek", title,
		fmt.Sprintf("Released %s in %s, the genres you watch most.", window, strings.Join(genres, ", ")), capItems(items, 40))
	return []*candRow{r}
}

func (b *builder) genComfort() []*candRow {
	if b.generic || b.taste == nil {
		return nil
	}
	var items []candItem
	for _, e := range b.taste.Evidence {
		if e.Kind != EvidenceFinished || e.Weight <= 0 {
			continue
		}
		long := e.Title.Type == core.TypeShow && e.Completions >= 6
		if !(e.Rewatched || e.Rating >= 8 || long) {
			continue
		}
		if b.now.Sub(e.At) < 45*24*time.Hour {
			continue // still fresh in memory
		}
		if !recommend.ContentRatingAllowed(e.Title.ContentRating, b.user.MaxContentRating) {
			continue
		}
		reason := "One of your favourites"
		switch {
		case e.Rewatched && e.Completions > 1 && e.Title.Type == core.TypeMovie:
			reason = fmt.Sprintf("You've watched this %d times", e.Completions)
		case e.Rating >= 8:
			reason = fmt.Sprintf("You rated it %g/10", e.Rating)
		case long:
			reason = fmt.Sprintf("You've watched %d episodes", e.Completions)
		}
		score := 0.5*clamp(e.Raw/2, 0, 1) + 0.2*e.Rating/10
		if e.Rewatched {
			score += 0.3
		}
		items = append(items, candItem{t: e.Title, base: score, fit: 1, qual: quality(&e.Title), reason: reason})
	}
	sortItems(items)
	r := newRow(KindComfort, "comfort", "Comfort Rewatches", "Favourites you've finished, rewatched or rated highly and haven't seen in a while.", items)
	r.minItems = 3
	return []*candRow{r}
}

func (b *builder) genDifferent() []*candRow {
	if !b.personal() || b.taste.Confidence < 0.2 {
		return nil
	}
	top := b.taste.TopGenres(3)
	topSet := map[string]bool{}
	for _, g := range top {
		topSet[g] = true
	}
	src := append(b.browse(core.BrowseQuery{Filter: core.Filter{MinRating: 7.5}, Sort: core.SortPopularity, Desc: true, Limit: 120}),
		b.both(func(t core.MediaType) []core.Title { return b.list("top_rated", t, 40) })...)
	var outside []core.Title
	for _, t := range src {
		ok := len(t.Genres) > 0
		for _, g := range normGenres(t.Genres) {
			if topSet[g] || b.taste.Genre[g] <= -0.3 {
				ok = false
				break
			}
		}
		if ok && t.Rating >= 7.5 {
			outside = append(outside, t)
		}
	}
	items := b.rank(outside, func(t *core.Title, _, _ int, fit float64, _ string) (float64, string) {
		maxG := 0.0
		for _, g := range normGenres(t.Genres) {
			maxG = math.Max(maxG, b.taste.Genre[g])
		}
		r := "Acclaimed"
		if len(t.Genres) > 0 {
			r = "Acclaimed " + strings.ToLower(t.Genres[0])
		}
		if len(top) > 0 {
			r += ", a change from your usual " + top[0]
		}
		return 0.55*quality(t) + 0.3*popularity(t) + 0.15*(1-maxG), r
	})
	kept := items[:0:0]
	for _, it := range items {
		if it.fit >= -0.15 {
			kept = append(kept, it)
		}
	}
	r := newRow(KindDifferent, "different", "Something Different",
		fmt.Sprintf("Exploration: acclaimed titles outside %s.", strings.Join(top, ", ")), capItems(kept, 40))
	r.minItems = 5
	return []*candRow{r}
}

// Runtime limits for "Short on Time".
const (
	shortMovieMinutes = 100
	shortShowMinutes  = 35
)

func isShort(t *core.Title) bool {
	m := minutes(t)
	if m <= 0 {
		return false
	}
	if t.Type == core.TypeShow {
		return m <= shortShowMinutes
	}
	return m <= shortMovieMinutes
}

func (b *builder) genShortOnTime() []*candRow {
	src := append([]core.Title{},
		b.browse(core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, MaxRuntime: shortMovieMinutes * time.Minute, MinRuntime: 60 * time.Minute}, Sort: core.SortPopularity, Desc: true, Limit: 60})...)
	src = append(src, b.browse(core.BrowseQuery{Filter: core.Filter{Type: core.TypeShow, MaxRuntime: shortShowMinutes * time.Minute, MinRuntime: 15 * time.Minute}, Sort: core.SortPopularity, Desc: true, Limit: 40})...)
	if b.personal() {
		src = append(b.pool(), src...)
	}
	var short []core.Title
	for _, t := range src {
		if isShort(&t) {
			short = append(short, t)
		}
	}
	fitW := 0.6
	if !b.personal() {
		fitW = 0
	}
	items := b.rank(short, func(t *core.Title, _, _ int, fit float64, why string) (float64, string) {
		r := fmt.Sprintf("%d min", int(minutes(t)))
		if t.Type == core.TypeShow {
			r = fmt.Sprintf("%d-minute episodes", int(minutes(t)))
		}
		if why != "" {
			r += " · " + why
		}
		return fitW*fit + 0.25*quality(t) + 0.15*popularity(t), r
	})
	r := newRow(KindShortOnTime, "shortontime", "Short on Time",
		"Shorter films and episodes that fit the time you usually have.", capItems(items, 60))
	r.minItems = 5
	return []*candRow{r}
}

func (b *builder) genFranchise() []*candRow {
	if b.generic || b.taste == nil {
		return nil
	}
	var rows []*candRow
	seen := map[int64]bool{}
	for _, e := range b.taste.Evidence {
		cid := e.Title.CollectionID
		if cid == 0 || e.Weight <= 0 || seen[cid] || len(rows) >= 4 {
			continue
		}
		seen[cid] = true
		name, members, err := b.e.catalog.Collection(b.ctx, cid)
		if err != nil || len(members) < 2 {
			continue
		}
		b.loadStatuses(members)
		sort.SliceStable(members, func(i, j int) bool {
			if !members[i].Released.Equal(members[j].Released) && !members[i].Released.IsZero() && !members[j].Released.IsZero() {
				return members[i].Released.Before(members[j].Released)
			}
			if members[i].Year != members[j].Year {
				return members[i].Year < members[j].Year
			}
			return members[i].Key < members[j].Key
		})
		watched, released := 0, 0
		var items []candItem
		for _, m := range members {
			if !m.Released.IsZero() && m.Released.After(b.now) {
				continue
			}
			released++
			ev := b.taste.evidenceFor(m.Key)
			if b.statuses[m.Key].ViewCount > 0 || (ev != nil && ev.Kind == EvidenceFinished) {
				watched++
				continue
			}
			if !recommend.ContentRatingAllowed(m.ContentRating, b.user.MaxContentRating) {
				continue
			}
			fit, _ := b.fit(&m)
			items = append(items, candItem{t: m, base: 1 - 0.03*float64(len(items)), fit: fit, qual: quality(&m)})
		}
		if watched == 0 || len(items) == 0 {
			continue
		}
		if name == "" {
			name = e.Title.CollectionName
		}
		for i := range items {
			items[i].reason = "Next in " + name
			if i > 0 {
				items[i].reason = "Still to watch in " + name
			}
		}
		title := "Finish " + name
		if !strings.HasSuffix(strings.ToLower(name), "collection") {
			title = "Finish the " + name + " Films"
		}
		r := newRow(KindFranchise, fmt.Sprintf("franchise.%d", cid), title,
			fmt.Sprintf("You've seen %d of %d %s films.", watched, released, name), items)
		r.keepOrder, r.minItems, r.collKey = true, 1, core.CollectionKey(cid)
		r.scale = 0.7 + 0.3*float64(watched)/float64(released)
		rows = append(rows, r)
	}
	return rows
}

// ---- external sources -------------------------------------------------------------

// llmSync is optionally implemented by the LLM source (internal/llm.Service)
// for background callers that can wait for a fresh answer.
type llmSync interface {
	ThemedRowsSync(ctx context.Context, user *core.User, seeds []core.Title, limit int) ([]core.Hub, error)
}

func (b *builder) genLLM() []*candRow {
	if b.e.llm == nil || !b.personal() {
		return nil
	}
	var seeds []core.Title
	for _, s := range b.seeds(8) {
		seeds = append(seeds, s.Title)
	}
	if len(seeds) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.e.opts.LLMTimeout)
	defer cancel()
	var hubs []core.Hub
	var err error
	if s, ok := b.e.llm.(llmSync); ok {
		hubs, err = s.ThemedRowsSync(ctx, &b.user, seeds, 30)
	} else {
		hubs, err = b.e.llm.ThemedRows(ctx, &b.user, seeds, 30)
	}
	if err != nil {
		b.e.log.Debug("personalize: llm rows failed", "err", err)
		return nil
	}
	var rows []*candRow
	for _, h := range hubs {
		items := b.rank(h.Items, func(t *core.Title, idx, n int, fit float64, _ string) (float64, string) {
			return 0.7*rankScore(idx, n) + 0.3*fit, "Picked by your AI model for this theme"
		})
		id := h.ID
		if !strings.HasPrefix(id, "llm.") {
			id = "llm." + id
		}
		rows = append(rows, newRow(KindLLM, id, h.Title, "A themed row your LLM suggested from your recent favourites.", items))
	}
	return rows
}

func (b *builder) genTrakt() []*candRow {
	if b.e.trakt == nil || b.generic {
		return nil
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.e.opts.SourceTimeout)
	defer cancel()
	var rows []*candRow
	if recs, err := b.e.trakt.Recommendations(ctx, b.user.ID, 40); err == nil && len(recs) > 0 {
		items := b.rank(recs, func(t *core.Title, idx, n int, fit float64, _ string) (float64, string) {
			return 0.6*rankScore(idx, n) + 0.4*fit, "Recommended by Trakt"
		})
		rows = append(rows, newRow(KindTraktRecs, "trakt.recommendations", "Trakt Recommendations", "From your connected Trakt account.", items))
	}
	if wl, err := b.e.trakt.Watchlist(ctx, b.user.ID, 60); err == nil && len(wl) > 0 {
		b.loadStatuses(wl)
		var items []candItem
		for _, t := range wl {
			ws := b.statuses[t.Key]
			if ws.ViewCount > 0 || !recommend.ContentRatingAllowed(t.ContentRating, b.user.MaxContentRating) {
				continue
			}
			items = append(items, candItem{t: t, base: rankScore(len(items), len(wl)), qual: quality(&t), reason: "On your Trakt watchlist"})
		}
		r := newRow(KindTraktWatch, "trakt.watchlist", "Your Trakt Watchlist", "Your own list on Trakt, in your order.", items)
		r.keepOrder, r.minItems = true, 1
		rows = append(rows, r)
	}
	return rows
}

// ---- generic rows (every viewer, and new viewers' whole Home) ------------------------

func (b *builder) genTrending() []*candRow {
	src := b.both(func(t core.MediaType) []core.Title { return b.list("trending_week", t, 40) })
	items := b.rank(src, func(t *core.Title, idx, n int, fit float64, _ string) (float64, string) {
		return 0.75*rankScore(idx, n) + 0.25*fit, "Trending this week"
	})
	r := newRow(KindTrending, "trending", "Trending This Week", "What's trending on TMDB this week.", capItems(items, 40))
	r.minItems = 5
	return []*candRow{r}
}

func (b *builder) genPopularServer() []*candRow {
	plays, err := b.e.store.plays(b.ctx, 0, b.now.AddDate(0, 0, -30))
	if err != nil || len(plays) == 0 {
		return nil
	}
	viewers := map[core.RatingKey]map[int64]bool{}
	count := map[core.RatingKey]int{}
	for _, p := range plays {
		if p.userID == b.user.ID && !b.generic {
			continue
		}
		if p.abandoned {
			continue
		}
		if viewers[p.tasteKey] == nil {
			viewers[p.tasteKey] = map[int64]bool{}
		}
		viewers[p.tasteKey][p.userID] = true
		count[p.tasteKey]++
	}
	keys := make([]core.RatingKey, 0, len(viewers))
	for k := range viewers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		vi, vj := len(viewers[keys[i]]), len(viewers[keys[j]])
		if vi != vj {
			return vi > vj
		}
		if count[keys[i]] != count[keys[j]] {
			return count[keys[i]] > count[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var titles []core.Title
	for _, k := range keys {
		if len(titles) >= 40 {
			break
		}
		if t, err := b.e.catalog.Get(b.ctx, k); err == nil && t != nil {
			titles = append(titles, *t)
		}
	}
	items := b.rank(titles, func(t *core.Title, idx, n int, fit float64, _ string) (float64, string) {
		v := len(viewers[t.Key])
		r := "Watched by another viewer here"
		if v > 1 {
			r = fmt.Sprintf("Watched by %d viewers here", v)
		}
		return 0.7*rankScore(idx, n) + 0.3*fit, r
	})
	name := b.e.serverName()
	r := newRow(KindPopularServer, "popular.server", "Popular on "+name,
		"What other viewers on this server watched most in the last 30 days.", items)
	return []*candRow{r}
}

func (b *builder) genNewReleases() []*candRow {
	src := b.both(func(t core.MediaType) []core.Title {
		return b.browse(core.BrowseQuery{Filter: core.Filter{Type: t}, Sort: core.SortReleased, Desc: true, Limit: 40})
	})
	items := b.rank(src, func(t *core.Title, _, _ int, fit float64, _ string) (float64, string) {
		return 0.6*recency(t, b.now) + 0.25*fit + 0.15*quality(t), "Recently released"
	})
	r := newRow(KindNewReleases, "newreleases", "New Releases", "The newest releases in the catalogue.", capItems(items, 40))
	r.minItems = 5
	return []*candRow{r}
}

func (b *builder) listRow(kind, id, title, reason, list, itemReason string, types ...core.MediaType) *candRow {
	var src []core.Title
	if len(types) == 2 {
		src = b.both(func(t core.MediaType) []core.Title { return b.list(list, t, 40) })
	} else {
		src = b.list(list, types[0], 40)
	}
	items := b.rank(src, func(t *core.Title, idx, n int, fit float64, _ string) (float64, string) {
		return 0.7*rankScore(idx, n) + 0.3*fit, itemReason
	})
	r := newRow(kind, id, title, reason, capItems(items, 40))
	r.sectionOnly, r.minItems = true, 5
	return r
}

func (b *builder) genTopRated() []*candRow {
	return []*candRow{b.listRow(KindTopRated, "toprated", "Top Rated", "TMDB's top rated titles.", "top_rated", "Top rated on TMDB", core.TypeMovie, core.TypeShow)}
}

func (b *builder) genPopular() []*candRow {
	return []*candRow{b.listRow(KindPopular, "popular", "Popular", "TMDB's most popular titles.", "popular", "Popular right now", core.TypeMovie, core.TypeShow)}
}

func (b *builder) genAiringToday() []*candRow {
	return []*candRow{b.listRow(KindAiringToday, "airing", "Airing Today", "Shows with a new episode today.", "airing_today", "New episode today", core.TypeShow)}
}
