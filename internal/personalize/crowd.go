package personalize

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/braedonsaunders/ether/internal/core"
)

// The crowd graph: item-based collaborative filtering from public
// co-engagement. Ether has a household of viewers, not millions; TMDB has
// millions. Its /recommendations list for a title is what people who
// engaged with it also engaged with, so aggregating those lists over a
// viewer's strongest evidence answers Netflix's question, "what do people
// with this viewer's taste love?", with no configuration:
//
//	crowd(c) = 1 − exp(−S·(1 + 0.25·(n−1)) / 1.6)
//	S        = Σ over independent seed groups g of max_{s∈g} w_s · 0.965^rank_s(c)
//	           + 0.3 · household co-watch(c)
//
// w_s is the seed's evidence strength (finish, rating, rewatch; decayed)
// times a recency factor, normalised so the strongest seed is 1; n is the
// number of independent seed groups (a franchise's films count once)
// recommending c. One strong seed's top pick scores ≈0.46, two agreeing
// seeds ≈0.79, three ≈0.94: consensus is the "people like you loved this"
// signal. A title only one weak seed mentions is halved.
//
// Lists come from the catalogue's three-day store (internal/catalog,
// crowd.go); a build fetches at most crowdFetchBudget pages from TMDB and
// serves stale lists past that, so a cold start spreads over a few builds.

// crowdSource is implemented by the catalogue (internal/catalog.Catalog).
type crowdSource interface {
	CrowdRecommendations(ctx context.Context, key core.RatingKey, pages int, fetch bool) ([]core.Title, bool, error)
}

const (
	// crowdMaxSeeds is the most favourites the graph aggregates.
	crowdMaxSeeds = 20
	// crowdDeepSeeds is how many of the strongest seeds read two pages.
	crowdDeepSeeds = 8
	// crowdFetchBudget caps TMDB recommendation pages fetched per build.
	crowdFetchBudget = 24
	// crowdRankDecay weighs a recommendation by its rank in a seed's list.
	crowdRankDecay = 0.965
	// crowdHouseWeight is the household co-watch share of the crowd sum.
	crowdHouseWeight = 0.3
	// crowdConsensusBonus is each extra independent seed's multiplier.
	crowdConsensusBonus = 0.25
	// crowdScale shapes the saturation of the crowd sum.
	crowdScale = 1.6
	// crowdFallbackSeeds bounds seeds read through Catalog.Similar when the
	// catalogue has no crowd store (its first page is recommendations).
	crowdFallbackSeeds = 10
)

// crowdSeed is one favourite the graph reads.
type crowdSeed struct {
	ev    Evidence
	w     float64 // normalised strength 0..1
	group string  // independence group (franchise or the title itself)
}

// crowdVote is one seed group's strongest contribution to a candidate.
type crowdVote struct {
	w    float64
	seed string
}

// crowdEntry is one candidate's crowd evidence.
type crowdEntry struct {
	t         core.Title
	score     float64 // 0..1
	consensus int     // independent seed groups recommending it
	house     float64 // household co-watch 0..1
	votes     map[string]crowdVote
	seeds     []string // seed titles, strongest contribution first
}

// crowdGraph is a build's aggregated crowd evidence.
type crowdGraph struct {
	items   map[core.RatingKey]*crowdEntry
	order   []core.RatingKey // by score, best first
	seeds   []crowdSeed
	fetched int // TMDB pages fetched this build
	housed  int // similar household viewers that contributed
}

// entry returns a title's crowd evidence (nil when none).
func (g *crowdGraph) entry(k core.RatingKey) *crowdEntry {
	if g == nil {
		return nil
	}
	return g.items[tasteKey(k)]
}

// titles lists the candidates, best first.
func (g *crowdGraph) titles() []core.Title {
	if g == nil {
		return nil
	}
	out := make([]core.Title, 0, len(g.order))
	for _, k := range g.order {
		out = append(out, g.items[k].t)
	}
	return out
}

// crowdSeeds picks the viewer's strongest positive evidence: finished,
// rated 6+ or well under way; strength (finishes, ratings, rewatches,
// decayed by age) times recency, normalised.
func (b *builder) crowdSeeds() []crowdSeed {
	if b.taste == nil {
		return nil
	}
	var out []crowdSeed
	for _, e := range b.taste.Evidence {
		if e.Weight <= 0 {
			continue
		}
		switch e.Kind {
		case EvidenceFinished:
		case EvidenceRated:
			if e.Rating < 6 {
				continue
			}
		case EvidenceWatching:
			if e.Progress < 0.5 && !(e.Title.Type == core.TypeShow && e.Completions >= 2) {
				continue
			}
		default:
			continue
		}
		if e.Title.Type != core.TypeMovie && e.Title.Type != core.TypeShow {
			continue
		}
		rec := 0.75
		if !e.At.IsZero() {
			days := math.Max(0, b.now.Sub(e.At).Hours()/24)
			rec = 0.55 + 0.45*math.Exp(-days/30)
		}
		g := "t" + strconv.FormatInt(int64(e.Key), 10)
		if e.Title.CollectionID != 0 {
			g = "c" + strconv.FormatInt(e.Title.CollectionID, 10)
		}
		out = append(out, crowdSeed{ev: e, w: clamp(e.Weight/1.6, 0.05, 1) * rec, group: g})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].w != out[j].w {
			return out[i].w > out[j].w
		}
		return out[i].ev.Key < out[j].ev.Key
	})
	if len(out) > crowdMaxSeeds {
		out = out[:crowdMaxSeeds]
	}
	if len(out) > 0 {
		top := out[0].w
		for i := range out {
			out[i].w /= top
		}
	}
	return out
}

// crowd builds (once per build) the viewer's crowd graph; nil for viewers
// without personal evidence.
func (b *builder) crowd() *crowdGraph {
	if b.crowdDone {
		return b.crowdG
	}
	b.crowdDone = true
	if !b.personal() {
		return nil
	}
	seeds := b.crowdSeeds()
	if len(seeds) == 0 {
		return nil
	}
	g := &crowdGraph{items: map[core.RatingKey]*crowdEntry{}, seeds: seeds}
	isSeed := map[core.RatingKey]bool{}
	for _, s := range seeds {
		isSeed[tasteKey(s.ev.Key)] = true
	}
	add := func(s crowdSeed, recs []core.Title) {
		for idx, t := range recs {
			k := tasteKey(t.Key)
			if isSeed[k] || b.touched[k] || (t.Type != core.TypeMovie && t.Type != core.TypeShow) {
				continue
			}
			ce := g.items[k]
			if ce == nil {
				ce = &crowdEntry{t: t, votes: map[string]crowdVote{}}
				g.items[k] = ce
			}
			w := s.w * math.Pow(crowdRankDecay, float64(idx))
			if w > ce.votes[s.group].w {
				ce.votes[s.group] = crowdVote{w: w, seed: s.ev.Title.Title}
			}
		}
	}
	cs, ok := b.e.catalog.(crowdSource)
	budget := crowdFetchBudget
	for i, s := range seeds {
		if b.ctx.Err() != nil {
			break
		}
		if !ok {
			if i < crowdFallbackSeeds {
				add(s, b.similar(s.ev.Key, 25))
			}
			continue
		}
		pages := 1
		if i < crowdDeepSeeds {
			pages = 2
		}
		if budget < pages && budget > 0 {
			pages = budget
		}
		ctx, cancel := context.WithTimeout(b.ctx, b.e.opts.SourceTimeout)
		recs, fetched, err := cs.CrowdRecommendations(ctx, s.ev.Key, pages, budget >= pages)
		cancel()
		if fetched {
			budget -= pages
			g.fetched += pages
		}
		if err != nil {
			b.e.log.Debug("personalize: crowd recommendations failed", "seed", s.ev.Key, "err", err)
			continue
		}
		add(s, recs)
	}
	b.addHousehold(g, isSeed)
	finishCrowd(g)
	b.crowdG = g
	b.e.log.Debug("personalize: crowd graph", "user", b.user.ID, "seeds", len(seeds), "candidates", len(g.order),
		"tmdb_pages", g.fetched, "household", g.housed)
	return g
}

// finishCrowd scores every candidate from its seed votes and household
// co-watch, and orders them.
func finishCrowd(g *crowdGraph) {
	g.order = g.order[:0]
	for k, ce := range g.items {
		sum := 0.0
		type sv struct {
			w    float64
			seed string
		}
		var votes []sv
		for _, v := range ce.votes {
			sum += v.w
			votes = append(votes, sv{v.w, v.seed})
		}
		ce.consensus = len(ce.votes)
		sort.Slice(votes, func(i, j int) bool {
			if votes[i].w != votes[j].w {
				return votes[i].w > votes[j].w
			}
			return votes[i].seed < votes[j].seed
		})
		for _, v := range votes {
			ce.seeds = append(ce.seeds, v.seed)
		}
		sum += crowdHouseWeight * ce.house
		n := max(ce.consensus, 1)
		ce.score = 1 - math.Exp(-sum*(1+crowdConsensusBonus*float64(n-1))/crowdScale)
		if ce.consensus == 1 && sum < 0.35 {
			ce.score *= 0.5 // one weak seed's passing mention
		}
		g.order = append(g.order, k)
	}
	sort.Slice(g.order, func(i, j int) bool {
		a, c := g.items[g.order[i]], g.items[g.order[j]]
		if a.score != c.score {
			return a.score > c.score
		}
		return g.order[i] < g.order[j]
	})
}

// householdTitles bounds catalogue lookups for household-only candidates.
const householdTitles = 15

// addHousehold folds in what similar viewers on this server watched: each
// other viewer who finished titles this viewer also finished is weighted
// by cosine overlap, and their other finishes earn that weight. Needs at
// least two viewers with plays.
func (b *builder) addHousehold(g *crowdGraph, isSeed map[core.RatingKey]bool) {
	plays, err := b.e.store.plays(b.ctx, 0, b.now.AddDate(-1, 0, 0))
	if err != nil || len(plays) == 0 {
		return
	}
	liked := map[int64]map[core.RatingKey]bool{}
	for _, p := range plays {
		if p.abandoned || !(p.completed || p.maxFraction >= 0.5) {
			continue
		}
		if liked[p.userID] == nil {
			liked[p.userID] = map[core.RatingKey]bool{}
		}
		liked[p.userID][p.tasteKey] = true
	}
	mine := liked[b.user.ID]
	if mine == nil {
		mine = map[core.RatingKey]bool{}
	}
	for _, e := range b.taste.Evidence {
		if e.Weight > 0 && e.Kind == EvidenceFinished {
			mine[tasteKey(e.Key)] = true
		}
	}
	if len(liked) < 2 || len(mine) == 0 {
		return
	}
	house := map[core.RatingKey]float64{}
	users := make([]int64, 0, len(liked))
	for uid := range liked {
		users = append(users, uid)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for _, uid := range users {
		set := liked[uid]
		if uid == b.user.ID {
			continue
		}
		overlap := 0
		for k := range set {
			if mine[k] {
				overlap++
			}
		}
		if overlap == 0 {
			continue
		}
		g.housed++
		sim := float64(overlap) / math.Sqrt(float64(len(set))*float64(len(mine)))
		for k := range set {
			if !mine[k] && !b.touched[k] && !isSeed[k] {
				house[k] += sim
			}
		}
	}
	top := 0.0
	for _, v := range house {
		top = math.Max(top, v)
	}
	if top <= 0 {
		return
	}
	keys := make([]core.RatingKey, 0, len(house))
	for k := range house {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if house[keys[i]] != house[keys[j]] {
			return house[keys[i]] > house[keys[j]]
		}
		return keys[i] < keys[j]
	})
	looked := 0
	for _, k := range keys {
		h := house[k] / top
		if ce := g.items[k]; ce != nil {
			ce.house = h
			continue
		}
		if looked >= householdTitles {
			continue
		}
		looked++
		t, err := b.e.catalog.Get(b.ctx, k)
		if err != nil || t == nil || (t.Type != core.TypeMovie && t.Type != core.TypeShow) {
			continue
		}
		g.items[k] = &crowdEntry{t: *t, house: h, votes: map[string]crowdVote{}}
	}
}

// consensusStrength maps independent recommending favourites onto 0..1:
// 0 for one, ≈0.39 for two, ≈0.63 for three, ≈0.78 for four.
func consensusStrength(n int) float64 {
	if n <= 1 {
		return 0
	}
	return 1 - math.Exp(-float64(n-1)/2)
}

// crowdReason is an item's micro-reason: "Loved by fans of Severance and
// Dark".
func crowdReason(ce *crowdEntry) string {
	if ce == nil {
		return ""
	}
	switch n := len(ce.seeds); {
	case n == 0:
		if ce.house > 0 {
			return "Watched by viewers here with similar taste"
		}
		return ""
	case n == 1:
		return "Loved by fans of " + ce.seeds[0]
	case n == 2:
		return "Loved by fans of " + ce.seeds[0] + " and " + ce.seeds[1]
	case n == 3:
		return "Loved by fans of " + ce.seeds[0] + ", " + ce.seeds[1] + " and " + ce.seeds[2]
	default:
		return fmt.Sprintf("Loved by fans of %s, %s and %d more of your favourites", ce.seeds[0], ce.seeds[1], n-2)
	}
}

// crowdOK is the bar a crowd candidate clears for a crowd row: trusted
// enough to recommend, in a language the viewer watches, not a genre they
// reject, and not clearly against their taste.
func (b *builder) crowdOK(t *core.Title, fit float64) bool {
	return b.taste.watchesLanguage(t.Language) && !genreVetoed(b.taste, t) && fit > -0.2 && weightedRating(t) >= 6.3
}

// crowdTitles returns per-type row titles.
var crowdTitles = map[string]map[core.MediaType]string{
	KindCrowd: {
		"":             "Fans of What You Watch Also Loved",
		core.TypeMovie: "Films Loved by Fans of What You Watch",
		core.TypeShow:  "Series Loved by Fans of What You Watch",
	},
	KindCrowdTrending: {
		"":             "Trending With Fans of What You Watch",
		core.TypeMovie: "Trending Films With Fans of What You Watch",
		core.TypeShow:  "Trending Series With Fans of What You Watch",
	},
}

func withSectionTitles(r *candRow) *candRow {
	t := crowdTitles[r.kind]
	r.sectionTitles = map[core.MediaType]string{core.TypeMovie: t[core.TypeMovie], core.TypeShow: t[core.TypeShow]}
	return r
}

// genCrowd is "Fans of What You Watch Also Loved": the crowd graph's
// candidates, strongest consensus first, blended with taste and quality.
func (b *builder) genCrowd() []*candRow {
	g := b.crowd()
	if g == nil || len(g.order) == 0 {
		return nil
	}
	items := b.rank(g.titles(), func(t *core.Title, _, _ int, fit float64, _ string) (float64, string) {
		ce := g.entry(t.Key)
		if ce == nil || ce.score < 0.12 || !b.crowdOK(t, fit) {
			return math.NaN(), ""
		}
		return 0.5*ce.score + 0.25*clamp(fit, -1, 1) + 0.2*quality(t) + 0.05*popularity(t), crowdReason(ce)
	})
	n := len(g.seeds)
	r := withSectionTitles(withTyped(newRow(KindCrowd, "crowd", crowdTitles[KindCrowd][""],
		fmt.Sprintf("People who loved the titles you love also loved these: crowd recommendations from %d of your favourites, strongest where several agree.", n),
		capItems(items, 40)), items))
	r.minItems = 5
	return []*candRow{r}
}

// genCrowdTrending is "Trending With Fans of What You Watch": what is
// trending now (TMDB, and Trakt when configured) among titles the crowd
// graph recommends. Trendy but personal.
func (b *builder) genCrowdTrending() []*candRow {
	g := b.crowd()
	if g == nil || len(g.order) == 0 {
		return nil
	}
	bz := b.buzz()
	var src []core.Title
	for _, k := range g.order {
		if bz.score(k) >= 0.05 {
			src = append(src, g.items[k].t)
		}
	}
	items := b.rank(src, func(t *core.Title, _, _ int, fit float64, _ string) (float64, string) {
		ce := g.entry(t.Key)
		if ce == nil || ce.score < 0.1 || !b.crowdOK(t, fit) {
			return math.NaN(), ""
		}
		why := bz.reason(t.Key)
		if cr := crowdReason(ce); cr != "" {
			why += " · " + cr
		}
		return 0.45*bz.score(t.Key) + 0.35*ce.score + 0.2*clamp(fit, -1, 1), why
	})
	r := withSectionTitles(withTyped(newRow(KindCrowdTrending, "crowd.trending", crowdTitles[KindCrowdTrending][""],
		"Trending right now among titles fans of what you watch love.", capItems(items, 40)), items))
	r.minItems = 5
	return []*candRow{r}
}

// crowdFeat is the compact crowd evidence a model keeps for assembly (the
// ranker's features).
type crowdFeat struct {
	score     float32
	consensus uint8
}

// crowdFeatures is the model's per-title crowd evidence.
func (g *crowdGraph) features() map[core.RatingKey]crowdFeat {
	if g == nil {
		return nil
	}
	out := make(map[core.RatingKey]crowdFeat, len(g.items))
	for k, ce := range g.items {
		out[k] = crowdFeat{score: float32(ce.score), consensus: uint8(min(ce.consensus, 255))}
	}
	return out
}
