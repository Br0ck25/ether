package recommend

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/state"
)

// Signal is one piece of taste evidence: a title the user interacted with
// and the signed weight that interaction carries (completions and high
// ratings positive, abandonments and low ratings negative), already
// time-decayed. Episodes are aggregated onto their show.
type Signal struct {
	Title  core.Title
	Weight float64
	At     time.Time
}

// Profile is a user's content-based taste model: signed affinities for
// genres, keywords, people, decades, languages, studios and franchises.
//
// How it is built (deterministic for a given history and clock):
//
//  1. Evidence per title (episodes fold onto their show): a completion is
//     +1.0 (+0.3 per rewatch, max +0.6); an in-progress title is +0.1..+0.5
//     by progress; an abandonment (stalled under 30% for 14+ days) is -0.6;
//     a rating adds (rating-5)/5*1.2, so 10 = +1.2 and 1 = -0.96. Trakt
//     plays not in the local log count as completions.
//  2. Each weight decays with a 120-day half-life and is clamped to ±2.
//  3. The up-to-100 strongest titles spread their weight onto features:
//     genres 1.0, keywords 0.5, directors 1.0, top-5 cast 0.7, decade 1.0,
//     language 1.0, studios 0.3, franchise 0.8.
//
// Score (Profile.Score) sums the candidate's feature affinities with
// per-feature scaling and caps, plus a weak TMDB rating/popularity prior.
type Profile struct {
	Genre      map[string]float64
	Keyword    map[string]float64
	Person     map[int64]float64
	PersonName map[int64]string
	Decade     map[int]float64
	Language   map[string]float64
	Studio     map[string]float64
	Collection map[int64]float64
	Signals    []Signal // by weight, liked first
	BuiltAt    time.Time
}

// ScoredTitle is a model-ranked candidate with its explanation.
type ScoredTitle struct {
	Title core.Title
	Score float64
	Why   string
}

// tasteKey maps an episode/season key onto its show, leaving others alone.
func tasteKey(k core.RatingKey) core.RatingKey {
	switch k.Kind() {
	case core.KindEpisode, core.KindSeason:
		return core.ShowKey(k.TMDB())
	}
	return k
}

// signalWeight turns a status + last history entry into a signed weight.
func signalWeight(ws core.WatchStatus, completed bool, at, now time.Time) float64 {
	w := 0.0
	frac := state.Fraction(ws)
	switch {
	case completed || ws.ViewCount > 0:
		w = 1.0
		if ws.ViewCount > 1 {
			w += math.Min(0.3*float64(ws.ViewCount-1), 0.6)
		}
		if ws.ViewOffset > 0 { // rewatching something already finished
			w += 0.2
		}
	case ws.ViewOffset > 0:
		abandoned := !at.IsZero() && now.Sub(at) > 14*24*time.Hour && frac < 0.3
		if abandoned {
			w = -0.6
		} else {
			w = 0.1 + 0.4*frac
		}
	}
	if ws.UserRating > 0 {
		w += (ws.UserRating - 5) / 5 * 1.2
	}
	ageDays := now.Sub(at).Hours() / 24
	if ageDays > 0 {
		w *= math.Pow(0.5, ageDays/120) // 120-day half-life
	}
	return w
}

// gatherSignals collects weights per taste key from history + statuses.
func (r *Recommender) gatherSignals(ctx context.Context, userID int64, now time.Time) (map[core.RatingKey]*Signal, map[core.RatingKey]core.WatchStatus, error) {
	weight := map[core.RatingKey]float64{}
	at := map[core.RatingKey]time.Time{}
	statusKeys := map[core.RatingKey]bool{}

	type histInfo struct {
		completed bool
		at        time.Time
	}
	latest := map[core.RatingKey]histInfo{}
	if r.state != nil {
		hist, err := r.state.History(ctx, userID, time.Time{}, 500)
		if err != nil {
			return nil, nil, err
		}
		for _, h := range hist { // newest first: first seen per key is latest
			tk := tasteKey(h.Key)
			cur, seen := latest[tk]
			if !seen {
				latest[tk] = histInfo{completed: h.Completed, at: h.ViewedAt}
			} else if h.Completed {
				cur.completed = true
				latest[tk] = cur
			}
			statusKeys[tk] = true
			statusKeys[h.Key] = true
		}
		ip, err := r.state.InProgress(ctx, userID, 100)
		if err != nil {
			return nil, nil, err
		}
		for _, ws := range ip {
			statusKeys[tasteKey(ws.Key)] = true
			statusKeys[ws.Key] = true
			if _, seen := latest[tasteKey(ws.Key)]; !seen {
				latest[tasteKey(ws.Key)] = histInfo{at: ws.LastViewedAt}
			}
		}
	}
	// Plays recorded on Trakt by other apps: completions for titles the
	// local log has never seen (local evidence always wins).
	if th, ok := r.trakt.(TraktHistorySource); ok {
		tctx, cancel := r.bounded(ctx)
		hist, err := th.PullHistory(tctx, userID, 300)
		cancel()
		if err != nil {
			r.log.Debug("recommend: trakt history failed", "user", userID, "err", err)
		}
		local := make(map[core.RatingKey]bool, len(latest))
		for k := range latest {
			local[k] = true
		}
		for _, h := range hist {
			tk := tasteKey(h.Key)
			if local[tk] {
				continue
			}
			cur, seen := latest[tk]
			if !seen || h.ViewedAt.After(cur.at) {
				latest[tk] = histInfo{completed: true, at: h.ViewedAt}
			}
		}
	}
	keys := make([]core.RatingKey, 0, len(statusKeys))
	for k := range statusKeys {
		keys = append(keys, k)
	}
	statuses := map[core.RatingKey]core.WatchStatus{}
	if r.state != nil && len(keys) > 0 {
		var err error
		if statuses, err = r.state.GetMany(ctx, userID, keys); err != nil {
			return nil, nil, err
		}
	}
	// For shows, aggregate episode statuses: a show with completed episodes
	// may have no status row of its own.
	for tk, info := range latest {
		ws := statuses[tk]
		weight[tk] += signalWeight(ws, info.completed, info.at, now)
		if info.at.After(at[tk]) {
			at[tk] = info.at
		}
	}
	for k, ws := range statuses {
		if k.Kind() != core.KindEpisode {
			continue
		}
		tk := tasteKey(k)
		if _, done := latest[tk]; done {
			continue // already weighted via history
		}
		if ws.ViewCount > 0 || ws.ViewOffset > 0 || ws.UserRating > 0 {
			weight[tk] += signalWeight(ws, ws.ViewCount > 0, ws.LastViewedAt, now) * 0.5
			if ws.LastViewedAt.After(at[tk]) {
				at[tk] = ws.LastViewedAt
			}
		}
	}
	out := map[core.RatingKey]*Signal{}
	for tk, w := range weight {
		// Episode aggregation can stack many episodes of one show; cap it so
		// one long binge does not swamp everything else.
		if w > 2 {
			w = 2
		}
		if w < -2 {
			w = -2
		}
		if w != 0 {
			out[tk] = &Signal{Weight: w, At: at[tk]}
		}
	}
	return out, statuses, nil
}

// BuildProfile builds (or returns the cached) taste profile for a user.
// Exported for explainability/admin; hits the same cache TopPicks uses.
func (r *Recommender) BuildProfile(ctx context.Context, user *core.User) (*Profile, error) {
	e, err := r.cached(ctx, userIDOf(user))
	if err != nil {
		return nil, err
	}
	return e.profile, nil
}

func (r *Recommender) build(ctx context.Context, userID int64) (*entry, error) {
	now := r.now()
	raw, statuses, err := r.gatherSignals(ctx, userID, now)
	if err != nil {
		return nil, err
	}
	p := &Profile{
		Genre: map[string]float64{}, Keyword: map[string]float64{}, Person: map[int64]float64{}, PersonName: map[int64]string{},
		Decade: map[int]float64{}, Language: map[string]float64{}, Studio: map[string]float64{},
		Collection: map[int64]float64{}, BuiltAt: now,
	}
	// Rank signals by |weight| and resolve at most 100 to titles.
	keys := make([]core.RatingKey, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		wi, wj := math.Abs(raw[keys[i]].Weight), math.Abs(raw[keys[j]].Weight)
		if wi != wj {
			return wi > wj
		}
		return keys[i] < keys[j]
	})
	if len(keys) > 100 {
		keys = keys[:100]
	}
	for _, k := range keys {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		t, err := r.catalog.Get(ctx, k)
		if err != nil || t == nil {
			continue
		}
		sig := *raw[k]
		sig.Title = *t
		p.Signals = append(p.Signals, sig)
		w := sig.Weight
		for _, g := range t.Genres {
			p.Genre[g] += w
		}
		for _, kw := range t.Keywords {
			p.Keyword[strings.ToLower(kw)] += w * 0.5
		}
		for _, d := range t.Directors {
			if d.TMDB != 0 {
				p.Person[d.TMDB] += w
				p.PersonName[d.TMDB] = d.Name
			}
		}
		for i, c := range t.Cast {
			if i >= 5 {
				break
			}
			if c.TMDB != 0 {
				p.Person[c.TMDB] += w * 0.7
				p.PersonName[c.TMDB] = c.Name
			}
		}
		if t.Year > 0 {
			p.Decade[t.Year/10*10] += w
		}
		if t.Language != "" {
			p.Language[t.Language] += w
		}
		for _, st := range t.Studios {
			p.Studio[st] += w * 0.3
		}
		if t.CollectionID != 0 {
			p.Collection[t.CollectionID] += w * 0.8
		}
	}
	sort.SliceStable(p.Signals, func(i, j int) bool {
		if p.Signals[i].Weight != p.Signals[j].Weight {
			return p.Signals[i].Weight > p.Signals[j].Weight
		}
		return p.Signals[i].Title.Key < p.Signals[j].Title.Key
	})

	e := &entry{profile: p, statuses: statuses, at: now}
	e.picks, err = r.scorePool(ctx, userID, p, statuses)
	if err != nil {
		// Pool failures (e.g. one dead list endpoint) shouldn't lose the
		// profile; scorePool already degrades per-source.
		r.log.Debug("recommend: scoring pool failed", "user", userID, "err", err)
		e.picks = nil
	}
	e.diverse = Diversify(e.picks, 100, 0.75)
	return e, nil
}

type contribution struct {
	label string
	value float64
}

// Score rates one candidate against the profile and explains itself.
func (p *Profile) Score(t core.Title) (float64, string) {
	var contribs []contribution
	score := 0.0
	if len(t.Genres) > 0 {
		sum := 0.0
		for _, g := range t.Genres {
			sum += p.Genre[g]
		}
		v := sum / float64(len(t.Genres))
		score += v
		// Explain with the single strongest genre.
		best, bv := "", 0.0
		for _, g := range t.Genres {
			if p.Genre[g] > bv {
				best, bv = g, p.Genre[g]
			}
		}
		if bv > 0 {
			contribs = append(contribs, contribution{fmt.Sprintf("you watch a lot of %s", best), bv})
		}
	}
	if len(t.Keywords) > 0 && len(p.Keyword) > 0 {
		sum, best, bv := 0.0, "", 0.0
		for _, kw := range t.Keywords {
			kw = strings.ToLower(kw)
			v := p.Keyword[kw]
			sum += v
			if v > bv || (v == bv && v > 0 && kw < best) {
				best, bv = kw, v
			}
		}
		// Normalise by sqrt(n) so keyword-rich titles are not favoured
		// linearly, and cap so keywords refine rather than dominate.
		v := math.Max(-1, math.Min(1, sum/math.Sqrt(float64(len(t.Keywords)))))
		score += v
		if bv > 0.25 {
			contribs = append(contribs, contribution{"you like stories about " + best, bv})
		}
	}
	person := 0.0
	var bestPerson string
	var bestPersonV float64
	addPerson := func(id int64, mult float64) {
		if id == 0 {
			return
		}
		v := p.Person[id] * mult
		person += v
		if v > bestPersonV {
			bestPersonV, bestPerson = v, p.PersonName[id]
		}
	}
	for _, d := range t.Directors {
		addPerson(d.TMDB, 1)
	}
	for i, c := range t.Cast {
		if i >= 5 {
			break
		}
		addPerson(c.TMDB, 0.7)
	}
	if person > 1.5 {
		person = 1.5
	}
	if person < -1.5 {
		person = -1.5
	}
	score += person
	if bestPerson != "" && bestPersonV > 0 {
		contribs = append(contribs, contribution{"features " + bestPerson, bestPersonV})
	}
	if t.Year > 0 {
		v := p.Decade[t.Year/10*10] * 0.3
		score += v
		if v > 0.2 {
			contribs = append(contribs, contribution{fmt.Sprintf("from the %ds you favour", t.Year/10*10), v})
		}
	}
	if t.Language != "" {
		score += p.Language[t.Language] * 0.15
	}
	if t.CollectionID != 0 {
		score += p.Collection[t.CollectionID] * 0.5
		if p.Collection[t.CollectionID] > 0 && t.CollectionName != "" {
			contribs = append(contribs, contribution{"part of " + t.CollectionName, p.Collection[t.CollectionID]})
		}
	}
	score += (t.Rating - 6) / 10 * 0.3 // weak quality prior
	score += math.Log10(t.Popularity+1) * 0.02

	sort.SliceStable(contribs, func(i, j int) bool { return contribs[i].value > contribs[j].value })
	var reasons []string
	for _, c := range contribs {
		if c.value > 0 {
			reasons = append(reasons, c.label)
		}
		if len(reasons) == 2 {
			break
		}
	}
	why := ""
	if len(reasons) > 0 {
		why = "Because " + strings.Join(reasons, " and ")
	}
	return score, why
}

// candidatePool gathers unwatched candidates: similar-to-liked, trending,
// and catalogue-popular titles.
func (r *Recommender) candidatePool(ctx context.Context, p *Profile) []core.Title {
	seen := map[core.RatingKey]bool{}
	var pool []core.Title
	add := func(items []core.Title) {
		for _, t := range items {
			if t.Type != core.TypeMovie && t.Type != core.TypeShow {
				continue
			}
			if seen[t.Key] {
				continue
			}
			seen[t.Key] = true
			pool = append(pool, t)
		}
	}
	seeds := 0
	for _, s := range p.Signals {
		if s.Weight <= 0 || seeds >= 8 {
			continue
		}
		seeds++
		if items, err := r.catalog.Similar(ctx, s.Title.Key, 20); err == nil {
			add(items)
		}
	}
	for _, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		if items, err := r.catalog.List(ctx, "trending_week", typ, 20); err == nil {
			add(items)
		}
		if page, err := r.catalog.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: typ}, Sort: core.SortPopularity, Desc: true, Limit: 50}); err == nil {
			add(page.Items)
		}
	}
	return pool
}

func (r *Recommender) scorePool(ctx context.Context, userID int64, p *Profile, known map[core.RatingKey]core.WatchStatus) ([]ScoredTitle, error) {
	pool := r.candidatePool(ctx, p)
	if len(pool) == 0 {
		return nil, nil
	}
	statuses := known
	if r.state != nil {
		keys := make([]core.RatingKey, len(pool))
		for i, t := range pool {
			keys[i] = t.Key
		}
		fetched, err := r.state.GetMany(ctx, userID, keys)
		if err != nil {
			return nil, err
		}
		statuses = fetched
	}
	seeded := map[core.RatingKey]bool{}
	for _, s := range p.Signals {
		seeded[s.Title.Key] = true
	}
	var scored []ScoredTitle
	for _, t := range pool {
		ws := statuses[t.Key]
		if ws.ViewCount > 0 || ws.ViewOffset > 0 { // watched or in progress
			continue
		}
		if seeded[t.Key] { // has any signal at all (e.g. abandoned)
			continue
		}
		sc, why := p.Score(t)
		scored = append(scored, ScoredTitle{Title: t, Score: sc, Why: why})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Title.Key < scored[j].Title.Key
	})
	return scored, nil
}

// similarity is the MMR redundancy between two candidates: genre overlap
// plus a same-franchise penalty.
func similarity(a, b core.Title) float64 {
	set := map[string]bool{}
	for _, g := range a.Genres {
		set[g] = true
	}
	inter := 0
	for _, g := range b.Genres {
		if set[g] {
			inter++
		}
	}
	union := len(set) + len(b.Genres) - inter
	sim := 0.0
	if union > 0 {
		sim = float64(inter) / float64(union) * 0.7
	}
	if a.CollectionID != 0 && a.CollectionID == b.CollectionID {
		sim += 0.5
	}
	if sim > 1 {
		sim = 1
	}
	return sim
}

// Diversify applies Maximal Marginal Relevance (lambda trades score against
// redundancy with already-picked items) so a row is not one franchise or one
// genre wall-to-wall. Deterministic: ties break by score then key.
func Diversify(scored []ScoredTitle, limit int, lambda float64) []ScoredTitle {
	if limit <= 0 || len(scored) == 0 {
		return nil
	}
	lo, hi := scored[0].Score, scored[0].Score
	for _, s := range scored {
		lo, hi = math.Min(lo, s.Score), math.Max(hi, s.Score)
	}
	span := hi - lo
	if span == 0 {
		span = 1
	}
	norm := func(s ScoredTitle) float64 { return (s.Score - lo) / span }

	remaining := append([]ScoredTitle(nil), scored...)
	var out []ScoredTitle
	for len(out) < limit && len(remaining) > 0 {
		best := 0
		bestVal := math.Inf(-1)
		for i, cand := range remaining {
			maxSim := 0.0
			for _, picked := range out {
				if s := similarity(cand.Title, picked.Title); s > maxSim {
					maxSim = s
				}
			}
			v := lambda*norm(cand) - (1-lambda)*maxSim
			if v > bestVal || (v == bestVal && (cand.Score > remaining[best].Score ||
				(cand.Score == remaining[best].Score && cand.Title.Key < remaining[best].Title.Key))) {
				best, bestVal = i, v
			}
		}
		out = append(out, remaining[best])
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return out
}

// TopPicks returns the user's model-ranked, diversity-filtered picks with
// explanations. Results are cached per user and invalidated by watch-state
// events (see Options.Events).
func (r *Recommender) TopPicks(ctx context.Context, user *core.User, limit int) ([]ScoredTitle, error) {
	e, err := r.cached(ctx, userIDOf(user))
	if err != nil {
		return nil, err
	}
	picks := filterScored(e.diverse, user)
	if limit > 0 && len(picks) > limit {
		picks = picks[:limit]
	}
	return picks, nil
}

func filterScored(in []ScoredTitle, user *core.User) []ScoredTitle {
	if user == nil || user.MaxContentRating == "" {
		return in
	}
	out := in[:0:0]
	for _, s := range in {
		if ContentRatingAllowed(s.Title.ContentRating, user.MaxContentRating) {
			out = append(out, s)
		}
	}
	return out
}
