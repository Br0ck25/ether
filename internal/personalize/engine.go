// Package personalize is Ether's Netflix-style personalization engine.
//
// Viewers only ever use Plex. The engine makes each viewer's Plex Home and
// library sections feel hand-curated for them, invisibly, by choosing
// which rows they get, in which order, with which titles, for the device
// and time they are watching:
//
//   - a taste model per viewer (taste.go): affinities for genres,
//     keywords, people, decades and languages, with time decay, learned
//     from completions, rewatches, abandons, ratings and impressions that
//     never led to a play, plus habits (session length, peak hours,
//     device, binge and rewatch tendencies) and owner overrides;
//   - candidate rows from generators (generators.go): Continue Watching,
//     Up Next, Top 10, Because you watched, More from <person>, Trending in
//     your taste, Hidden gems, New this week, Comfort rewatches, Something
//     different, Short on time, Finish the franchise, owner collections,
//     LLM themes, Trakt, and popularity rows for new viewers;
//   - assembly per request context (assemble.go): context-aware item
//     scoring, MMR diversity, cross-row dedupe, bandit row ranking with
//     daily-seeded Thompson exploration (bandit.go), the owner's layout
//     rules (layout.go) and daily rotation;
//   - a feedback loop: every served row is recorded as an impression
//     (impressions.go) and every play is attributed to the row it came
//     from (plays.go), which feeds both the taste model and the bandit.
//
// Models are built in the background (on a schedule, after watch-state
// changes and on demand) and cached per viewer; a /hubs request only runs
// assembly over the cache and never waits on TMDB or the LLM.
//
// The Engine implements core.Recommender, core.ContextualRecommender,
// core.ImpressionSink and core.PersonalScorer, plus the owner-facing
// methods the admin API needs (owner.go).
package personalize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/recommend"
	"github.com/braedonsaunders/ether/internal/state"
)

// UserSource lists the server's users (core.Auth satisfies it).
type UserSource interface {
	Users(ctx context.Context) ([]core.User, error)
}

// Options build an Engine. DB and Catalog are required; every other
// dependency is optional and its rows simply drop out when absent.
type Options struct {
	// Owner previews use the same effective viewer and title policy as Plex.
	UserPolicy func(*core.User) *core.User
	Allowed    func(*core.User, *core.Title) bool
	DB         *sql.DB
	Catalog    core.Catalog
	State      core.State
	// Events delivers watch-state changes (internal/state.State).
	Events recommend.EventSource
	// Users lists viewers for background precompute (core.Auth).
	Users UserSource
	// Collections supplies owner-designed collection rows.
	Collections core.CollectionRows
	LLM         recommend.LLMSource
	Trakt       recommend.TraktSource
	Playlists   core.Playlists
	// Base answers detail-page Related rows (internal/recommend).
	Base core.Recommender
	// Sections maps library section ids to their type, for per-user
	// allowed sections (default 1 = movies, 2 = shows).
	Sections map[int]core.MediaType
	// ServerName is the friendly name shown in "Popular on <name>".
	ServerName func() string
	// ImageURL builds browser-loadable artwork URLs for the owner UI
	// (default /ether/api/image?path=…&w=…).
	ImageURL func(path string, width int) string
	Logger   *slog.Logger
	Now      func() time.Time
	Location *time.Location // server local time (default time.Local)
	// IntentEnabled reports whether live session intent (#32) is on
	// (nil = on); consulted per signal so owner edits apply live.
	IntentEnabled func() bool
	// ClockFormat reports the micro-reason finish-time style (#31):
	// "12h", "24h", a locale like "en-GB", or "" for 12h.
	ClockFormat func() string

	ModelTTL        time.Duration // rebuild an active viewer's model after this (default 3h)
	RefreshEvery    time.Duration // background scheduler period (default 15m; <0 disables)
	RebuildDebounce time.Duration // after a watch-state change (default 20s)
	Retention       time.Duration // item impressions (default 60 days)
	Workers         int           // concurrent model builds (default 2)
	ColdWait        time.Duration // how long a request may wait for the first-ever model (default 1.5s)
	OwnerRowsWait   time.Duration // how long a request may wait for owner rows it has never fetched (default 150ms)
	SourceTimeout   time.Duration // Trakt and owner collection calls (default 16s)
	LLMTimeout      time.Duration // LLM themed rows during a build (default 60s)
	BuildTimeout    time.Duration // one model build (default 3m)
	MaxHomeRows     int           // default 20 across the complete composed page
	MaxSectionRows  int           // default 16
	// Exploration scales Thompson sampling (0 = pure exploitation, 1 =
	// full sampling; default 0.5; negative = 0).
	Exploration      float64
	HalfLife         time.Duration // taste decay half-life (default 120 days)
	ImpressionBuffer int           // queued impressions before dropping (default 4096)
	FlushEvery       time.Duration // impression write interval (default 2s)
}

func (o *Options) defaults() {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.ImageURL == nil {
		o.ImageURL = func(path string, width int) string {
			return "/ether/api/image?path=" + url.QueryEscape(path) + "&w=" + strconv.Itoa(width)
		}
	}
	if o.ServerName == nil {
		o.ServerName = func() string { return "This Server" }
	}
	if len(o.Sections) == 0 {
		o.Sections = map[int]core.MediaType{1: core.TypeMovie, 2: core.TypeShow}
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	def(&o.ModelTTL, 3*time.Hour)
	def(&o.RefreshEvery, 15*time.Minute)
	def(&o.RebuildDebounce, 20*time.Second)
	def(&o.Retention, 60*24*time.Hour)
	def(&o.ColdWait, 1500*time.Millisecond)
	def(&o.OwnerRowsWait, 150*time.Millisecond)
	def(&o.SourceTimeout, 10*time.Second)
	def(&o.LLMTimeout, 60*time.Second)
	def(&o.BuildTimeout, 3*time.Minute)
	def(&o.HalfLife, defaultHalfLife)
	def(&o.FlushEvery, 2*time.Second)
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.MaxHomeRows <= 0 {
		o.MaxHomeRows = 20
	}
	if o.MaxSectionRows <= 0 {
		o.MaxSectionRows = 16
	}
	switch {
	case o.Exploration == 0:
		o.Exploration = 0.5
	case o.Exploration < 0:
		o.Exploration = 0
	case o.Exploration > 1:
		o.Exploration = 1
	}
	if o.ImpressionBuffer <= 0 {
		o.ImpressionBuffer = 4096
	}
}

// genericID keys the model served to viewers whose own model is not built
// yet: popularity rows, no personal history.
const genericID int64 = -1

// Engine is the personalization engine.
type Engine struct {
	opts    Options
	log     *slog.Logger
	catalog core.Catalog
	state   core.State
	llm     recommend.LLMSource
	trakt   recommend.TraktSource
	store   *sqlStore
	rec     *recorder
	loc     *time.Location

	mu            sync.Mutex
	closed        bool
	models        map[int64]*userModel
	users         map[int64]core.User
	lastCtx       map[int64]core.HubContext
	flights       map[int64]*flight
	debounce      map[int64]*time.Timer
	version       uint64
	homeLayout    core.HomeLayout
	extraHomeRows func(context.Context, *core.User, core.HubContext, string, int) []core.Hub
	layoutVer     uint64
	owner         map[string]*ownerEntry

	memoMu sync.Mutex
	memo   map[memoKey]*memoEntry

	// Netflix-style hooks state (hooks.go), created on first use.
	hookOnce sync.Once
	hook     *hookState

	hubMu    sync.RWMutex
	hubKinds map[string]string

	playMu sync.Mutex
	active map[playKey]*activePlay

	statsMu sync.Mutex
	stats   *statsSnapshot

	actMu  sync.Mutex
	actAt  time.Time
	actRes *activityResult

	queue  chan int64
	events chan state.Event
	evSync chan chan struct{}
	unsub  func()
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type flight struct {
	done chan struct{}
	// started: a worker is building; again: a request arrived after the
	// build read its inputs, so it must run once more (guarded by e.mu).
	started, again bool
}

var (
	_ core.Recommender           = (*Engine)(nil)
	_ core.ContextualRecommender = (*Engine)(nil)
	_ core.ImpressionSink        = (*Engine)(nil)
	_ core.PersonalScorer        = (*Engine)(nil)
)

// New builds the engine and starts its background work (impression
// writer, event handling, model builders, scheduler). Call Close on
// shutdown.
func New(opts Options) (*Engine, error) {
	if opts.DB == nil {
		return nil, errors.New("personalize: DB is required")
	}
	if opts.Catalog == nil {
		return nil, errors.New("personalize: Catalog is required")
	}
	opts.defaults()
	st := &sqlStore{db: opts.DB, loc: opts.Location}
	e := &Engine{
		opts: opts, log: opts.Logger, catalog: opts.Catalog, state: opts.State, llm: opts.LLM, trakt: opts.Trakt,
		store: st, loc: opts.Location,
		models: map[int64]*userModel{}, users: map[int64]core.User{}, lastCtx: map[int64]core.HubContext{},
		flights: map[int64]*flight{}, debounce: map[int64]*time.Timer{}, owner: map[string]*ownerEntry{},
		hubKinds: map[string]string{}, active: map[playKey]*activePlay{}, memo: map[memoKey]*memoEntry{},
		queue: make(chan int64, 1024), events: make(chan state.Event, 4096), evSync: make(chan chan struct{}),
	}
	e.rec = newRecorder(st, e.log, opts.ImpressionBuffer, opts.FlushEvery)
	var err error
	e.homeLayout, err = st.homeLayout(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.rec.run(e.ctx) }()
	e.wg.Add(1)
	go e.eventLoop(e.ctx)
	for i := 0; i < opts.Workers; i++ {
		e.wg.Add(1)
		go e.worker(e.ctx)
	}
	if opts.Events != nil {
		e.unsub = opts.Events.Subscribe(e.onEvent)
	}
	e.request(genericID)
	if opts.RefreshEvery > 0 {
		e.wg.Add(1)
		go e.scheduler(e.ctx)
	}
	return e, nil
}

// Close stops background work, writing any queued impressions.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	for _, t := range e.debounce {
		t.Stop()
	}
	e.mu.Unlock()
	if e.unsub != nil {
		e.unsub()
	}
	e.cancel()
	e.wg.Wait()
}

func (e *Engine) now() time.Time { return e.opts.Now() }

func (e *Engine) serverName() string {
	if n := e.opts.ServerName(); n != "" {
		return n
	}
	return "This Server"
}

// ---- core.Recommender / core.ContextualRecommender ----------------------------

// HomeHubs implements core.Recommender with a context derived from the
// clock only (device unknown).
func (e *Engine) HomeHubs(ctx context.Context, user *core.User, limit int) ([]core.Hub, error) {
	return e.HomeHubsFor(ctx, user, core.NewHubContext(e.now().In(e.loc), "", "", ""), limit)
}

// SectionHubs implements core.Recommender.
func (e *Engine) SectionHubs(ctx context.Context, user *core.User, t core.MediaType, limit int) ([]core.Hub, error) {
	return e.SectionHubsFor(ctx, user, t, core.NewHubContext(e.now().In(e.loc), "", "", ""), limit)
}

// Related implements core.Recommender by delegating to the base
// recommender (detail-page rows are not personalised per context).
func (e *Engine) Related(ctx context.Context, user *core.User, key core.RatingKey, limit int) ([]core.Hub, error) {
	if e.opts.Base != nil {
		return e.opts.Base.Related(ctx, user, key, limit)
	}
	items, err := e.catalog.Similar(ctx, key, limit)
	if err != nil {
		return nil, err
	}
	items = recommend.FilterTitles(items, user)
	if len(items) == 0 {
		return nil, nil
	}
	return []core.Hub{{ID: "movie.similar", Title: "More Like This", Context: "hub.movie.similar", Style: "shelf", Items: items}}, nil
}

// HomeHubsFor implements core.ContextualRecommender: the viewer's Home rows
// for this context, served from the precomputed model. The Continue
// Watching row is returned as a placeholder ("home.continue", no items)
// marking where the Plex front end should put its own.
func (e *Engine) HomeHubsFor(ctx context.Context, user *core.User, hc core.HubContext, limit int) ([]core.Hub, error) {
	p := e.home(ctx, user, hc, "", limit, false)
	p.Rows = arrangeLayout(p.Rows, e.layoutForContext(ctx), userID(user), hc, sectionName(""), e.opts.MaxHomeRows)
	return toHubs(p.Rows), nil
}

// SectionHubsFor implements core.ContextualRecommender for a library
// section's rows (below the front end's Continue Watching and Recently
// Added).
func (e *Engine) SectionHubsFor(ctx context.Context, user *core.User, t core.MediaType, hc core.HubContext, limit int) ([]core.Hub, error) {
	p := e.home(ctx, user, hc, t, limit, false)
	p.Rows = arrangeLayout(p.Rows, e.layoutForContext(ctx), userID(user), hc, sectionName(t), e.opts.MaxSectionRows)
	return toHubs(p.Rows), nil
}

func toHubs(rows []core.HomeRow) []core.Hub {
	out := make([]core.Hub, 0, len(rows))
	for _, r := range rows {
		if r.Hidden {
			continue
		}
		h := r.Hub
		if r.Kind == KindContinue {
			h.Items = nil
		}
		out = append(out, h)
	}
	return out
}

// home is the one code path behind /hubs, /hubs/sections/{id} and the
// owner's Home preview.
func (e *Engine) home(ctx context.Context, user *core.User, hc core.HubContext, section core.MediaType, limit int, wait bool) core.HomePreview {
	if limit <= 0 {
		limit = 12
	}
	if hc.Now.IsZero() {
		hc.Now = e.now().In(e.loc)
	}
	u := core.User{}
	if user != nil {
		u = *user
	}
	p := core.HomePreview{GeneratedAt: e.now(), Context: hc, Rows: []core.HomeRow{}}
	m := e.modelFor(ctx, &u, wait)
	if m == nil {
		return p
	}
	stats := e.statsFor(ctx)
	var inprog []core.WatchStatus
	if section == "" && !m.generic && e.state != nil {
		var err error
		if inprog, err = e.state.InProgress(ctx, u.ID, 50); err != nil {
			e.log.Debug("personalize: in progress failed", "err", err)
		}
	}
	layoutDoc := e.layoutForContext(ctx)
	owner, ownerVer := e.ownerRows(ctx, &u, hc, section, layoutDoc, wait)
	_, layoutVer := e.layoutState()
	day := dayOf(hc.Now, e.loc)
	intent, intentVer := e.intentFor(u.ID, hc.Now)
	key := memoKey{user: u.ID, section: section, bucket: hc.Bucket(), day: day, hour: hc.Now.Hour(), limit: limit, model: m.version,
		layout: layoutVer, stats: stats.at.UnixNano(), owner: ownerVer, cw: progressSig(inprog),
		limits: u.MaxContentRating + "|" + fmt.Sprint(u.AllowedSections, u.IsOwner), intent: intentVer}
	_, draft := ctx.Value(draftLayoutKey{}).(core.HomeLayout)
	if rows, ok := e.memoGet(key); ok && !draft {
		p.Rows = rows
		return p
	}
	a := &assembly{
		layout: &layoutDoc,
		model:  m, user: u, hc: hc, section: section, limit: limit, rules: nil, owner: owner,
		global: stats.global, arms: stats.users[u.ID], allowed: e.allowedFor(&u), explore: e.opts.Exploration, day: day,
		intent: intent,
	}
	if section == "" {
		a.maxRows = e.opts.MaxHomeRows
		a.cw = e.continueFrom(ctx, inprog)
	} else {
		a.maxRows = e.opts.MaxSectionRows
	}
	a.maxRows = 100
	p.Rows = a.run()
	for _, r := range p.Rows {
		e.registerHub(r.HubID, r.Kind)
	}
	if !draft {
		e.memoPut(key, p.Rows)
	}
	return p
}

// ---- assembled-rows memo ------------------------------------------------------

// memoKey identifies one assembly: everything its result depends on. A
// new model, layout, statistics snapshot, owner rows or Continue Watching
// change produces a new key, so entries never need invalidating.
type memoKey struct {
	user    int64
	section core.MediaType
	bucket  string
	day     int
	hour    int // layout schedules have hour windows
	limit   int
	model   uint64
	layout  uint64
	stats   int64
	owner   int64
	cw      string
	limits  string
	intent  uint64 // live session-intent version (#32); 0 = none
}

type memoEntry struct {
	rows []core.HomeRow
	at   time.Time
}

// memoTTL bounds how long an assembled screen is reused; clients poll
// /hubs often and the rows only change when an input does.
const memoTTL = 2 * time.Minute

func (e *Engine) memoGet(k memoKey) ([]core.HomeRow, bool) {
	e.memoMu.Lock()
	defer e.memoMu.Unlock()
	ent, ok := e.memo[k]
	if !ok || e.now().Sub(ent.at) > memoTTL {
		return nil, false
	}
	// Callers may append to or reorder the slice; the rows themselves are
	// read-only.
	return append([]core.HomeRow(nil), ent.rows...), true
}

func (e *Engine) memoPut(k memoKey, rows []core.HomeRow) {
	e.memoMu.Lock()
	defer e.memoMu.Unlock()
	now := e.now()
	if len(e.memo) >= 4096 {
		for mk, ent := range e.memo {
			if now.Sub(ent.at) > memoTTL {
				delete(e.memo, mk)
			}
		}
		if len(e.memo) >= 4096 {
			e.memo = map[memoKey]*memoEntry{}
		}
	}
	e.memo[k] = &memoEntry{rows: append([]core.HomeRow(nil), rows...), at: now}
}

// progressSig summarises Continue Watching for the memo key.
func progressSig(sts []core.WatchStatus) string {
	var b strings.Builder
	for _, ws := range sts {
		b.WriteString(ws.Key.String())
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(ws.LastViewedAt.Unix(), 36))
		b.WriteByte(',')
	}
	return b.String()
}

// allowedFor returns the media types a viewer's allowed sections cover
// (nil = everything).
func (e *Engine) allowedFor(u *core.User) func(core.MediaType) bool {
	if u == nil || u.IsOwner || len(u.AllowedSections) == 0 {
		return nil
	}
	ok := map[core.MediaType]bool{}
	for _, id := range u.AllowedSections {
		if t, found := e.opts.Sections[id]; found {
			ok[t] = true
		}
	}
	return func(t core.MediaType) bool { return ok[t] }
}

func (e *Engine) layoutRules() []core.LayoutRule {
	rules, _ := e.layoutState()
	return rules
}

func (e *Engine) layoutState() ([]core.LayoutRule, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return legacyLayoutView(e.homeLayout), e.layoutVer
}

// ---- core.PersonalScorer ---------------------------------------------------------

// ScoreTitles implements core.PersonalScorer: taste fit blended with
// quality, roughly -0.7..1 (higher = better fit for this viewer).
func (e *Engine) ScoreTitles(ctx context.Context, user *core.User, titles []core.Title) (map[core.RatingKey]float64, error) {
	var ts *Taste
	if user != nil {
		e.mu.Lock()
		if m := e.models[user.ID]; m != nil {
			ts = m.taste
		}
		e.mu.Unlock()
		if ts == nil {
			var err error
			if ts, _, err = e.buildTaste(ctx, *user); err != nil {
				return nil, err
			}
		}
	}
	out := make(map[core.RatingKey]float64, len(titles))
	for i := range titles {
		fit, _ := ts.Fit(&titles[i])
		out[titles[i].Key] = round3(0.7*fit + 0.3*quality(&titles[i]))
	}
	return out, nil
}

func userID(u *core.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}
