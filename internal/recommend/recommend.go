// Package recommend implements core.Recommender: the per-user Home and
// library-section rows ("hubs") and detail-page related rows.
//
// Plex hub contexts used (Hub.Context, Hub.ID) — modelled on the contexts
// real PMS uses so official apps render, order and badge rows correctly:
//
//	home.continue           hub.home.continue            Continue Watching (home)
//	home.ondeck             hub.home.ondeck              On Deck next episodes (home)
//	home.trending.week      hub.home.trending            Trending This Week (home)
//	because.<ratingKey>     hub.home.because             Because You Watched X (home)
//	home.toppicks           hub.home.toppicks            local model (home)
//	trakt.recommendations   hub.home.trakt               Trakt (home, optional)
//	trakt.watchlist         hub.home.trakt.watchlist     Trakt (home, optional)
//	(llm.<slug>             hub.home.llm                 LLM rows, from internal/llm)
//	home.newreleases        hub.home.recentlyreleased    New Releases (home)
//	franchise.<tmdbColl>    hub.home.franchise           partially-watched franchises
//	collection.<ratingKey>  hub.home.collection          promoted smart collections
//	movie.inprogress        hub.movie.inprogress         section
//	movie.recentlyreleased  hub.movie.recentlyreleased   section (real PMS context)
//	movie.trending          hub.movie.trending           section
//	movie.toprated          hub.movie.toprated           section
//	movie.popular           hub.movie.popular            section
//	tv.inprogress           hub.tv.inprogress            section
//	tv.ondeck               hub.tv.ondeck                section (real PMS context)
//	tv.trending             hub.tv.trending              section
//	tv.airing               hub.tv.airing                section
//	tv.toprated             hub.tv.toprated              section
//	movie.similar           hub.movie.similar            detail page "More Like This" (tv.similar for shows)
//	movie.collection        hub.movie.collection         detail page franchise row
//	movie.by.actor.or.director.<id>
//	                        hub.movie.by.actor.or.director  detail page cast/director row
//	                                                     (tv.* equivalents for shows)
//
// Real PMS identifiers for reference: Home uses home.continue / home.ondeck
// (hub.home.continue, hub.home.ondeck); sections use movie.inprogress,
// movie.recentlyreleased, movie.recentlyadded, tv.ondeck, tv.inprogress,
// tv.recentlyaired; detail pages use movie.similar / tv.similar and
// *.by.actor.or.director. Official apps key special rendering (progress
// bars, "Up Next" badges) off the context, so those are used verbatim where
// Ether has an equivalent row; Ether-only rows use the same naming scheme.
//
// Individual sources failing (a dead list, Trakt down) degrade to a missing
// row, never a failed Home request. The local model's scoring is documented
// in model.go; per-user MaxContentRating filtering in rating.go.
package recommend

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/state"
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

// Options build a Recommender. Catalog is required; State, Collections,
// Trakt and LLM are optional and their rows simply drop out when absent.
type Options struct {
	Catalog     core.Catalog
	State       core.State
	Collections core.Collections
	Trakt       TraktSource
	LLM         LLMSource
	Events      EventSource
	Logger      *slog.Logger
	Now         func() time.Time
	CacheTTL    time.Duration // model cache; default 10m
	// SourceTimeout bounds each optional network source (Trakt, LLM) per
	// request so a slow third party cannot stall Home (default 3s).
	SourceTimeout time.Duration
}

// Recommender implements core.Recommender.
type Recommender struct {
	catalog     core.Catalog
	state       core.State
	collections core.Collections
	trakt       TraktSource
	llm         LLMSource
	log         *slog.Logger
	now         func() time.Time
	ttl         time.Duration
	srcTimeout  time.Duration

	mu      sync.Mutex
	cache   map[int64]*entry
	gen     map[int64]uint64 // bumped by Invalidate; stale builds are not stored
	flights map[int64]*buildFlight
	unsub   func()
}

type entry struct {
	profile  *Profile
	picks    []ScoredTitle // all scored candidates, best first
	diverse  []ScoredTitle // MMR-diversified top picks
	statuses map[core.RatingKey]core.WatchStatus
	at       time.Time
}

type buildFlight struct {
	done chan struct{}
	e    *entry
	err  error
}

var _ core.Recommender = (*Recommender)(nil)

// New builds a Recommender and subscribes to state events for cache
// invalidation when Options.Events is set.
func New(opts Options) *Recommender {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 10 * time.Minute
	}
	if opts.SourceTimeout <= 0 {
		opts.SourceTimeout = 3 * time.Second
	}
	r := &Recommender{
		catalog: opts.Catalog, state: opts.State, collections: opts.Collections,
		trakt: opts.Trakt, llm: opts.LLM, log: opts.Logger, now: opts.Now,
		ttl: opts.CacheTTL, srcTimeout: opts.SourceTimeout, cache: map[int64]*entry{},
		gen: map[int64]uint64{}, flights: map[int64]*buildFlight{},
	}
	if opts.Events != nil {
		r.unsub = opts.Events.Subscribe(func(ev state.Event) {
			if invalidates(ev) {
				r.Invalidate(ev.UserID)
			}
		})
	}
	return r
}

// invalidates reports whether a state event changes the taste model.
// Timeline ticks while playing (every ~10s per client) do not; a stop,
// completion, (un)scrobble or rating does. Continue Watching is read live
// from State on every request, so it never waits for invalidation.
func invalidates(ev state.Event) bool {
	if ev.Kind == state.EventProgress {
		return ev.Completed || ev.State == "stopped"
	}
	return true
}

// Close unsubscribes from state events.
func (r *Recommender) Close() {
	if r.unsub != nil {
		r.unsub()
	}
}

// Invalidate drops a user's cached profile/picks (called on state events).
func (r *Recommender) Invalidate(userID int64) {
	r.mu.Lock()
	delete(r.cache, userID)
	r.gen[userID]++
	r.mu.Unlock()
}

func userIDOf(u *core.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

// cached returns the user's model entry, building it on a miss. Builds are
// single-flighted per user (other users are never blocked) and run detached
// from the caller's context, so one cancelled request cannot fail the build
// for others waiting on it.
func (r *Recommender) cached(ctx context.Context, userID int64) (*entry, error) {
	r.mu.Lock()
	if e, ok := r.cache[userID]; ok && r.now().Sub(e.at) < r.ttl {
		r.mu.Unlock()
		return e, nil
	}
	f, ok := r.flights[userID]
	if !ok {
		f = &buildFlight{done: make(chan struct{})}
		r.flights[userID] = f
		gen := r.gen[userID]
		go func() {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			e, err := r.build(bctx, userID)
			r.mu.Lock()
			f.e, f.err = e, err
			if err == nil && r.gen[userID] == gen {
				r.cache[userID] = e
			}
			delete(r.flights, userID)
			r.mu.Unlock()
			close(f.done)
		}()
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return f.e, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// bounded derives a context for one optional network source.
func (r *Recommender) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.srcTimeout)
}

// ---- hub helpers ----------------------------------------------------------

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

func (r *Recommender) titlesFor(ctx context.Context, statuses []core.WatchStatus) []core.Title {
	var out []core.Title
	for _, ws := range statuses {
		if ctx.Err() != nil {
			break
		}
		if t, err := r.catalog.Get(ctx, ws.Key); err == nil && t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// continueWatching returns in-progress titles (movies and episodes) in the
// state's recency order, optionally restricted to one section type.
func (r *Recommender) continueWatching(ctx context.Context, user *core.User, only core.MediaType, limit int) []core.Title {
	if r.state == nil {
		return nil
	}
	sts, err := r.state.InProgress(ctx, userIDOf(user), 50)
	if err != nil {
		r.log.Debug("recommend: InProgress failed", "err", err)
		return nil
	}
	items := r.titlesFor(ctx, sts)
	if only != "" {
		kept := items[:0:0]
		for _, t := range items {
			if only == core.TypeMovie && t.Key.Kind() == core.KindMovie {
				kept = append(kept, t)
			}
			if only == core.TypeShow && t.Key.Kind() == core.KindEpisode {
				kept = append(kept, t)
			}
		}
		items = kept
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// onDeck returns the next unwatched episode of each recently finished
// series, most recently finished first.
func (r *Recommender) onDeck(ctx context.Context, user *core.User, limit int) []core.Title {
	if r.state == nil {
		return nil
	}
	uid := userIDOf(user)
	hist, err := r.state.History(ctx, uid, time.Time{}, 200)
	if err != nil {
		return nil
	}
	var nexts []core.Title
	seenShow := map[int64]bool{}
	for _, h := range hist {
		if !h.Completed || h.Key.Kind() != core.KindEpisode {
			continue
		}
		show := h.Key.TMDB()
		if seenShow[show] {
			continue
		}
		seenShow[show] = true
		next, err := r.catalog.NextEpisode(ctx, h.Key)
		if err != nil || next == nil {
			continue
		}
		ws, err := r.state.Get(ctx, uid, next.Key)
		if err == nil && (ws.ViewCount > 0 || ws.ViewOffset > 0) {
			continue // already watched, or it's Continue Watching's job
		}
		nexts = append(nexts, *next)
		if limit > 0 && len(nexts) >= limit {
			break
		}
	}
	return nexts
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

func (r *Recommender) list(ctx context.Context, name string, typ core.MediaType, limit int) []core.Title {
	items, err := r.catalog.List(ctx, name, typ, limit)
	if err != nil {
		r.log.Debug("recommend: list failed", "list", name, "type", typ, "err", err)
		return nil
	}
	return items
}

func (r *Recommender) browse(ctx context.Context, q core.BrowseQuery) []core.Title {
	page, err := r.catalog.Browse(ctx, q)
	if err != nil {
		r.log.Debug("recommend: browse failed", "err", err)
		return nil
	}
	return page.Items
}

// browseFor is browse with the user's content-rating limit pushed down to
// the catalogue (results are filtered again in makeHub regardless).
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

func (r *Recommender) trendingWeek(ctx context.Context, limit int) []core.Title {
	return interleave(
		r.list(ctx, "trending_week", core.TypeMovie, limit),
		r.list(ctx, "trending_week", core.TypeShow, limit),
	)
}

func (r *Recommender) newReleases(ctx context.Context, user *core.User, limit int) []core.Title {
	items := append(
		r.browseFor(ctx, user, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}, Sort: core.SortReleased, Desc: true, Limit: limit}),
		r.browseFor(ctx, user, core.BrowseQuery{Filter: core.Filter{Type: core.TypeShow}, Sort: core.SortReleased, Desc: true, Limit: limit})...,
	)
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Released.Equal(items[j].Released) {
			return items[i].Released.After(items[j].Released)
		}
		return items[i].Key < items[j].Key
	})
	return items
}

// hasTaste reports whether the user's model has any positive evidence.
func (r *Recommender) hasTaste(ctx context.Context, user *core.User) bool {
	e, err := r.cached(ctx, userIDOf(user))
	if err != nil {
		return false
	}
	for _, s := range e.profile.Signals {
		if s.Weight > 0 {
			return true
		}
	}
	return false
}

// recentCompletionSeeds are the liked titles for "Because you watched" and
// LLM rows: positive-weight signals, most recent first.
func (r *Recommender) recentCompletionSeeds(ctx context.Context, user *core.User, n int) []core.Title {
	e, err := r.cached(ctx, userIDOf(user))
	if err != nil {
		return nil
	}
	sigs := append([]Signal(nil), e.profile.Signals...)
	sort.SliceStable(sigs, func(i, j int) bool {
		if !sigs[i].At.Equal(sigs[j].At) {
			return sigs[i].At.After(sigs[j].At)
		}
		return sigs[i].Title.Key < sigs[j].Title.Key
	})
	var out []core.Title
	for _, s := range sigs {
		// Completions weigh >= ~0.5 after decay; a merely-started title
		// (max ~0.46) is not yet evidence the user liked it.
		if s.Weight < 0.5 {
			continue
		}
		out = append(out, s.Title)
		if len(out) >= n {
			break
		}
	}
	return out
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

// ---- core.Recommender -----------------------------------------------------

// HomeHubs returns the ordered Home rows. limit caps items per row
// (default 20).
func (r *Recommender) HomeHubs(ctx context.Context, user *core.User, limit int) ([]core.Hub, error) {
	if r.catalog == nil {
		return nil, fmt.Errorf("recommend: no catalog configured")
	}
	if limit <= 0 {
		limit = 20
	}
	seen := map[core.RatingKey]bool{}
	var hubs []core.Hub
	add := func(id, title, hubContext string, typ core.MediaType, items []core.Title, dedupeAcross bool) bool {
		var s map[core.RatingKey]bool
		if dedupeAcross {
			s = seen
		}
		h, ok := makeHub(id, title, hubContext, typ, items, user, limit, s)
		if ok {
			hubs = append(hubs, h)
		}
		return ok
	}

	add("home.continue", "Continue Watching", "hub.home.continue", "", r.continueWatching(ctx, user, "", limit), true)
	add("home.ondeck", "On Deck", "hub.home.ondeck", core.TypeEpisode, r.onDeck(ctx, user, limit), true)
	add("home.trending.week", "Trending This Week", "hub.home.trending", "", r.trendingWeek(ctx, limit), true)

	seeds := r.recentCompletionSeeds(ctx, user, 12)
	because := 0
	for _, seed := range seeds {
		if because >= 3 {
			break
		}
		similar, err := r.catalog.Similar(ctx, seed.Key, limit)
		if err != nil {
			continue
		}
		if add(fmt.Sprintf("because.%d", int64(seed.Key)), "Because You Watched "+seed.Title,
			"hub.home.because", seed.Type, r.unwatchedOnly(ctx, user, similar), true) {
			because++
		}
	}

	if !r.hasTaste(ctx, user) {
		// No evidence yet: model picks would just be popularity, which the
		// trending/new rows already show better.
	} else if picks, err := r.TopPicks(ctx, user, limit); err == nil && len(picks) > 0 {
		items := make([]core.Title, len(picks))
		for i, p := range picks {
			items[i] = p.Title
		}
		add("home.toppicks", "Top Picks for You", "hub.home.toppicks", "", items, true)
	}

	if r.trakt != nil {
		uid := userIDOf(user)
		tctx, cancel := r.bounded(ctx)
		if items, err := r.trakt.Recommendations(tctx, uid, limit); err == nil {
			add("trakt.recommendations", "Trakt Recommendations", "hub.home.trakt", "", r.unwatchedOnly(ctx, user, items), true)
		} else {
			r.log.Debug("recommend: trakt recommendations failed", "err", err)
		}
		if items, err := r.trakt.Watchlist(tctx, uid, limit); err == nil {
			// The user's own list: shown whole, not deduped against rows above.
			add("trakt.watchlist", "Your Trakt Watchlist", "hub.home.trakt.watchlist", "", r.unwatchedOnly(ctx, user, items), false)
		} else {
			r.log.Debug("recommend: trakt watchlist failed", "err", err)
		}
		cancel()
	}

	if r.llm != nil {
		lctx, cancel := r.bounded(ctx)
		if rows, err := r.llm.ThemedRows(lctx, user, seeds, limit); err == nil {
			for _, row := range rows {
				hubContext := row.Context
				if hubContext == "" {
					hubContext = "hub.home.llm"
				}
				add(row.ID, row.Title, hubContext, row.Type, r.unwatchedOnly(ctx, user, row.Items), true)
			}
		} else {
			r.log.Debug("recommend: llm rows failed", "err", err)
		}
		cancel()
	}

	add("home.newreleases", "New Releases", "hub.home.recentlyreleased", "", r.newReleases(ctx, user, limit), true)

	for _, fh := range r.franchiseHubs(ctx, user, limit, 2) {
		hubs = append(hubs, fh) // franchises may repeat earlier rows by design
	}
	// Curated collection rows may repeat titles from earlier rows, as
	// promoted collections do in Plex.
	hubs = append(hubs, r.collectionHubs(ctx, user, "", limit, 3, nil)...)
	return hubs, nil
}

// franchiseHubs returns rows for franchises the user has partially watched.
func (r *Recommender) franchiseHubs(ctx context.Context, user *core.User, limit, max int) []core.Hub {
	e, err := r.cached(ctx, userIDOf(user))
	if err != nil {
		return nil
	}
	seenColl := map[int64]bool{}
	var hubs []core.Hub
	for _, s := range e.profile.Signals {
		if len(hubs) >= max {
			break
		}
		cid := s.Title.CollectionID
		if cid == 0 || s.Weight <= 0 || seenColl[cid] {
			continue
		}
		seenColl[cid] = true
		name, items, err := r.catalog.Collection(ctx, cid)
		if err != nil || len(items) < 2 {
			continue
		}
		statuses := map[core.RatingKey]core.WatchStatus{}
		if r.state != nil {
			keys := make([]core.RatingKey, len(items))
			for i, t := range items {
				keys[i] = t.Key
			}
			if m, err := r.state.GetMany(ctx, userIDOf(user), keys); err == nil {
				statuses = m
			}
		}
		roll := state.Summarize(keysOf(items), statuses)
		if roll.Watched == 0 || roll.AllWatched {
			continue // not partially watched
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Year != items[j].Year {
				return items[i].Year < items[j].Year
			}
			return items[i].Key < items[j].Key
		})
		if name == "" {
			name = s.Title.CollectionName
		}
		if h, ok := makeHub(fmt.Sprintf("franchise.%d", cid), name, "hub.home.franchise", core.TypeMovie, items, user, limit, nil); ok {
			hubs = append(hubs, h)
		}
	}
	return hubs
}

func keysOf(items []core.Title) []core.RatingKey {
	out := make([]core.RatingKey, len(items))
	for i, t := range items {
		out[i] = t.Key
	}
	return out
}

// collectionHubs promotes up to max smart/trending collections as rows.
func (r *Recommender) collectionHubs(ctx context.Context, user *core.User, typ core.MediaType, limit, max int, seen map[core.RatingKey]bool) []core.Hub {
	if r.collections == nil {
		return nil
	}
	var colls []core.Collection
	types := []core.MediaType{typ}
	if typ == "" {
		types = []core.MediaType{core.TypeMovie, core.TypeShow}
	}
	for _, t := range types {
		if cs, err := r.collections.List(ctx, user, t); err == nil {
			colls = append(colls, cs...)
		}
	}
	var hubs []core.Hub
	for _, c := range colls {
		if len(hubs) >= max {
			break
		}
		if c.Kind != "smart" && c.Kind != "trending" {
			continue
		}
		page, err := r.collections.Items(ctx, user, c.Key, 0, limit)
		if err != nil {
			continue
		}
		if h, ok := makeHub(fmt.Sprintf("collection.%d", int64(c.Key)), c.Title, "hub.home.collection", c.Type, page.Items, user, limit, seen); ok {
			h.Key = c.Key
			h.More = page.Total > len(page.Items)
			hubs = append(hubs, h)
		}
	}
	return hubs
}

// SectionHubs returns the rows at the top of the movie or show section.
func (r *Recommender) SectionHubs(ctx context.Context, user *core.User, t core.MediaType, limit int) ([]core.Hub, error) {
	if r.catalog == nil {
		return nil, fmt.Errorf("recommend: no catalog configured")
	}
	if limit <= 0 {
		limit = 20
	}
	seen := map[core.RatingKey]bool{}
	var hubs []core.Hub
	add := func(id, title, hubContext string, items []core.Title) bool {
		h, ok := makeHub(id, title, hubContext, t, items, user, limit, seen)
		if ok {
			hubs = append(hubs, h)
		}
		return ok
	}
	prefix, ctxPrefix := "movie", "hub.movie"
	if t == core.TypeShow {
		prefix, ctxPrefix = "tv", "hub.tv"
	}

	add(prefix+".inprogress", "Continue Watching", ctxPrefix+".inprogress", r.continueWatching(ctx, user, t, limit))
	if t == core.TypeShow {
		add("tv.ondeck", "On Deck", "hub.tv.ondeck", r.onDeck(ctx, user, limit))
		add("tv.airing", "Airing Today", "hub.tv.airing", r.list(ctx, "airing_today", t, limit))
	} else {
		add("movie.recentlyreleased", "Recently Released", "hub.movie.recentlyreleased",
			r.browseFor(ctx, user, core.BrowseQuery{Filter: core.Filter{Type: t}, Sort: core.SortReleased, Desc: true, Limit: limit}))
	}
	add(prefix+".trending", "Trending This Week", ctxPrefix+".trending", r.list(ctx, "trending_week", t, limit))
	add(prefix+".toprated", "Top Rated", ctxPrefix+".toprated", r.list(ctx, "top_rated", t, limit))
	add(prefix+".popular", "Popular", ctxPrefix+".popular", r.list(ctx, "popular", t, limit))

	// One Because-you-watched row and model picks filtered to this section.
	for _, seed := range r.recentCompletionSeeds(ctx, user, 12) {
		if seed.Type != t {
			continue
		}
		similar, err := r.catalog.Similar(ctx, seed.Key, limit)
		if err != nil {
			continue
		}
		if add(fmt.Sprintf("because.%d", int64(seed.Key)), "Because You Watched "+seed.Title, ctxPrefix+".because", r.unwatchedOnly(ctx, user, similar)) {
			break
		}
	}
	if !r.hasTaste(ctx, user) {
		// See HomeHubs.
	} else if picks, err := r.TopPicks(ctx, user, limit*3); err == nil {
		var items []core.Title
		for _, p := range picks {
			if p.Title.Type == t {
				items = append(items, p.Title)
			}
		}
		add(prefix+".toppicks", "Top Picks for You", ctxPrefix+".toppicks", items)
	}
	hubs = append(hubs, r.collectionHubs(ctx, user, t, limit, 3, nil)...)
	return hubs, nil
}

// Related returns detail-page rows for one title: more like this, its
// franchise, and other work by its cast/director.
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
	add(prefix+".similar", "More Like This", "hub."+prefix+".similar", r.unwatchedOnly(ctx, user, similar), true)

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
		add(fmt.Sprintf("%s.by.actor.or.director.%d", prefix, p.TMDB), fmt.Sprintf("More %s %s", roleWord, p.Name),
			"hub."+prefix+".by.actor.or.director", items, true)
	}
	if len(title.Directors) > 0 {
		personWork(title.Directors[0], "from director")
	}
	if len(title.Cast) > 0 {
		personWork(title.Cast[0], "with")
	}
	if len(title.Cast) > 1 {
		personWork(title.Cast[1], "with")
	}
	return hubs, nil
}
