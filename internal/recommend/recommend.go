// Package recommend supplies detail-page related rows. Home and section
// composition, taste models and caches live in internal/personalize.
package recommend

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"
)

// TraktSource is the optional Trakt dependency (internal/trakt.Client).
// Implementations return (nil, nil) for users who have not connected.
type TraktSource interface {
	Recommendations(ctx context.Context, userID int64, limit int) ([]core.Title, error)
	Watchlist(ctx context.Context, userID int64, limit int) ([]core.Title, error)
}

// TraktHistorySource is optionally implemented by the TraktSource
// (internal/trakt.Client does): plays recorded on Trakt from other apps are
// merged into the local history model as completions.
type TraktHistorySource interface {
	PullHistory(ctx context.Context, userID int64, limit int) ([]core.HistoryEntry, error)
}

// LLMSource is the optional LLM dependency (internal/llm.Service).
// ThemedRows must be non-blocking (serve stale/omit while computing).
type LLMSource interface {
	ThemedRows(ctx context.Context, user *core.User, seeds []core.Title, limit int) ([]core.Hub, error)
	MoreLikeThis(ctx context.Context, title *core.Title, limit int) ([]core.Title, error)
}

// EventSource invalidates the model cache on watch-state changes
// (internal/state.State).
type EventSource interface {
	Subscribe(func(state.Event)) func()
}

// Options supplies only the dependencies used by related recommendations.
type Options struct {
	Catalog       core.Catalog
	State         core.State
	LLM           LLMSource
	Logger        *slog.Logger
	SourceTimeout time.Duration
	Scorer        core.PersonalScorer
}
type Recommender struct {
	catalog    core.Catalog
	state      core.State
	llm        LLMSource
	log        *slog.Logger
	srcTimeout time.Duration
	scorerMu   sync.RWMutex
	scorer     core.PersonalScorer
	home       core.Recommender
}

var _ core.Recommender = (*Recommender)(nil)

func New(o Options) *Recommender {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.SourceTimeout <= 0 {
		o.SourceTimeout = 3 * time.Second
	}
	return &Recommender{catalog: o.Catalog, state: o.State, llm: o.LLM, log: o.Logger, srcTimeout: o.SourceTimeout, scorer: o.Scorer}
}

// SetHomeSource preserves the core.Recommender contract using the one production
// Home engine. It does not maintain another model, cache, or event subscription.
func (r *Recommender) SetHomeSource(home core.Recommender) {
	r.scorerMu.Lock()
	r.home = home
	r.scorerMu.Unlock()
}
func (r *Recommender) HomeHubs(ctx context.Context, u *core.User, limit int) ([]core.Hub, error) {
	r.scorerMu.RLock()
	home := r.home
	r.scorerMu.RUnlock()
	if home == nil {
		return nil, fmt.Errorf("recommend: Home source not configured")
	}
	return home.HomeHubs(ctx, u, limit)
}
func (r *Recommender) SectionHubs(ctx context.Context, u *core.User, typ core.MediaType, limit int) ([]core.Hub, error) {
	r.scorerMu.RLock()
	home := r.home
	r.scorerMu.RUnlock()
	if home == nil {
		return nil, fmt.Errorf("recommend: Home source not configured")
	}
	return home.SectionHubs(ctx, u, typ, limit)
}

// Close is retained for callers which previously managed a model subscription.
func (r *Recommender) Close() {}

func (r *Recommender) SetScorer(s core.PersonalScorer) {
	r.scorerMu.Lock()
	r.scorer = s
	r.scorerMu.Unlock()
}

func (r *Recommender) personalOrder(ctx context.Context, user *core.User, items []core.Title) []core.Title {
	r.scorerMu.RLock()
	sc := r.scorer
	r.scorerMu.RUnlock()
	if sc == nil || user == nil || len(items) < 2 {
		return items
	}
	scores, err := sc.ScoreTitles(ctx, user, items)
	if err != nil || len(scores) == 0 {
		return items
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, t := range items {
		v := scores[t.Key]
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	span := hi - lo
	if span <= 0 {
		return items
	}
	type ranked struct {
		t core.Title
		v float64
		i int
	}
	rs := make([]ranked, len(items))
	n := float64(len(items))
	for i, t := range items {
		rs[i] = ranked{t, 0.6*(1-float64(i)/n) + 0.4*(scores[t.Key]-lo)/span, i}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		if rs[a].v != rs[b].v {
			return rs[a].v > rs[b].v
		}
		return rs[a].i < rs[b].i
	})
	out := make([]core.Title, len(rs))
	for i, x := range rs {
		out[i] = x.t
	}
	return out
}

func userIDOf(u *core.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func (r *Recommender) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.srcTimeout)
}

func dedupeTitles(items []core.Title) []core.Title {
	seen := map[core.RatingKey]bool{}
	out := items[:0:0]
	for _, t := range items {
		if seen[t.Key] {
			continue
		}
		seen[t.Key] = true
		out = append(out, t)
	}
	return out
}

func makeHub(id, title, hubContext string, typ core.MediaType, items []core.Title, user *core.User, limit int, seen map[core.RatingKey]bool) (core.Hub, bool) {
	items = dedupeTitles(FilterTitles(append([]core.Title(nil), items...), user))
	if seen != nil {
		kept := items[:0:0]
		for _, t := range items {
			if !seen[t.Key] {
				kept = append(kept, t)
			}
		}
		items = kept
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	if len(items) == 0 {
		return core.Hub{}, false
	}
	if seen != nil {
		for _, t := range items {
			seen[t.Key] = true
		}
	}
	return core.Hub{ID: id, Title: title, Type: typ, Context: hubContext, Style: "shelf", Promoted: true, Items: items}, true
}

func interleave(a, b []core.Title) []core.Title {
	var out []core.Title
	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			out = append(out, a[i])
		}
		if i < len(b) {
			out = append(out, b[i])
		}
	}
	return out
}

func (r *Recommender) browseFor(ctx context.Context, user *core.User, q core.BrowseQuery) []core.Title {
	if user != nil && user.MaxContentRating != "" {
		q.Filter.MaxContentRating = user.MaxContentRating
	}
	page, err := r.catalog.Browse(ctx, q)
	if err != nil {
		r.log.Debug("recommend: browse failed", "err", err)
		return nil
	}
	return page.Items
}

func (r *Recommender) unwatchedOnly(ctx context.Context, user *core.User, items []core.Title) []core.Title {
	if r.state == nil || len(items) == 0 {
		return items
	}
	keys := make([]core.RatingKey, len(items))
	for i, t := range items {
		keys[i] = t.Key
	}
	statuses, err := r.state.GetMany(ctx, userIDOf(user), keys)
	if err != nil {
		return items
	}
	out := items[:0:0]
	for _, t := range items {
		ws := statuses[t.Key]
		if ws.ViewCount == 0 {
			out = append(out, t)
		}
	}
	return out
}

func (r *Recommender) Related(ctx context.Context, user *core.User, key core.RatingKey, limit int) ([]core.Hub, error) {
	if r.catalog == nil {
		return nil, fmt.Errorf("recommend: no catalog configured")
	}
	if limit <= 0 {
		limit = 20
	}
	title, err := r.catalog.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	seen := map[core.RatingKey]bool{key: true}
	relType := title.Type // item type of the rows
	if relType == core.TypeSeason || relType == core.TypeEpisode {
		relType = core.TypeShow
	}
	var hubs []core.Hub
	add := func(id, hubTitle, hubContext string, items []core.Title, dedupeAcross bool) {
		var s map[core.RatingKey]bool
		if dedupeAcross {
			s = seen
		}
		if h, ok := makeHub(id, hubTitle, hubContext, relType, items, user, limit, s); ok {
			hubs = append(hubs, h)
		}
	}

	prefix := "movie"
	if title.Type == core.TypeShow || title.Type == core.TypeSeason || title.Type == core.TypeEpisode {
		prefix = "tv"
	}
	simKey := key
	if title.ShowKey != 0 && (title.Type == core.TypeSeason || title.Type == core.TypeEpisode) {
		simKey = title.ShowKey // seasons/episodes relate through their show
		seen[simKey] = true
	}
	similar, err := r.catalog.Similar(ctx, simKey, limit)
	if err != nil {
		r.log.Debug("recommend: similar failed", "key", simKey, "err", err)
	}
	if r.llm != nil {
		lctx, cancel := r.bounded(ctx)
		if extra, err := r.llm.MoreLikeThis(lctx, title, limit); err == nil {
			// Interleave so model picks surface without displacing TMDB's.
			similar = interleave(similar, extra)
		}
		cancel()
	}
	add(prefix+".similar", "More Like This", "hub."+prefix+".similar", r.personalOrder(ctx, user, r.unwatchedOnly(ctx, user, similar)), true)

	if title.CollectionID != 0 {
		name, items, err := r.catalog.Collection(ctx, title.CollectionID)
		if err == nil {
			others := items[:0:0]
			for _, it := range items {
				if it.Key != key {
					others = append(others, it)
				}
			}
			sort.SliceStable(others, func(i, j int) bool {
				if others[i].Year != others[j].Year {
					return others[i].Year < others[j].Year
				}
				return others[i].Key < others[j].Key
			})
			if name == "" {
				name = title.CollectionName
			}
			// Exempt from cross-row dedupe but still excludes the title itself.
			if h, ok := makeHub(prefix+".collection", name, "hub."+prefix+".collection", title.Type, others, user, limit, nil); ok {
				h.Key = core.CollectionKey(title.CollectionID)
				hubs = append(hubs, h)
			}
		}
	}

	personWork := func(p core.Person, roleWord string) {
		if p.TMDB == 0 {
			return
		}
		items := r.browseFor(ctx, user, core.BrowseQuery{Filter: core.Filter{Person: p.TMDB}, Sort: core.SortPopularity, Desc: true, Limit: limit})
		items = r.personalOrder(ctx, user, r.unwatchedOnly(ctx, user, items))
		add(fmt.Sprintf("%s.by.actor.or.director.%d", prefix, p.TMDB), fmt.Sprintf("More %s %s", roleWord, p.Name),
			"hub."+prefix+".by.actor.or.director", items, true)
	}
	if len(title.Directors) > 0 {
		personWork(title.Directors[0], "from director")
	} else if len(title.Crew) > 0 {
		for _, cr := range title.Crew {
			if cr.Role == "Creator" || cr.Role == "Showrunner" {
				personWork(cr, "from creator")
				break
			}
		}
	}
	if len(title.Cast) > 0 {
		personWork(title.Cast[0], "with")
	}
	if len(title.Cast) > 1 {
		personWork(title.Cast[1], "with")
	}
	return hubs, nil
}
