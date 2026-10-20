package resolver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

const (
	defaultMaxTraces      = 50
	maxHealthAttempts     = 32
	defaultSearchCacheTTL = 6 * time.Hour
	defaultUnavailableTTL = 24 * time.Hour
	defaultMaxCandidates  = 3
)

// Options tunes a Service. Zero values get sensible defaults.
type Options struct {
	Logger        *slog.Logger
	Now           func() time.Time
	MaxTraces     int
	RetentionDays int // posts older than this are penalised; 0 = no extra penalty
	// HealthTTL is how long a passed health check is trusted before the
	// release is sampled again (default 6h; negative disables the cache).
	HealthTTL time.Duration
	// HistoryMax bounds the persisted resolution history (default 5000
	// rows); HistoryMaxAge drops older rows (default 60 days).
	HistoryMax    int
	HistoryMaxAge time.Duration
}

// CandidateTrace explains one candidate's fate in a resolution.
type CandidateTrace struct {
	ReleaseID           string        `json:"releaseId"`
	Name                string        `json:"name"`
	Indexer             string        `json:"indexer"`
	Score               float64       `json:"score"`
	CustomFormats       []FormatMatch `json:"customFormats,omitempty"`
	CompatibilityReason string        `json:"compatibilityReason,omitempty"`
	// Route explains a move by source routing (routing.go), e.g. "faster
	// source: cached on debrid".
	Route          string        `json:"route,omitempty"`
	Rejected       bool          `json:"rejected"`
	Reason         string        `json:"reason,omitempty"`
	Size           int64         `json:"size,omitempty"`
	Pinned         bool          `json:"pinned,omitempty"` // the owner's pin for the title
	HealthChecked  bool          `json:"healthChecked"`
	Healthy        bool          `json:"healthy"`
	FetchDuration  time.Duration `json:"fetchDuration"`
	HealthDuration time.Duration `json:"healthDuration"`
}

// Trace is one recorded resolution attempt, for the admin UI.
type Trace struct {
	TitleKey       core.RatingKey   `json:"titleKey"`
	TitleName      string           `json:"titleName"`
	Profile        string           `json:"profile"`
	At             time.Time        `json:"at"`
	Candidates     []CandidateTrace `json:"candidates"`
	Winner         string           `json:"winner,omitempty"` // release id
	Err            string           `json:"err,omitempty"`
	SearchDuration time.Duration    `json:"searchDuration"`
	// Failover marks a resolution that excluded already-tried releases.
	Failover bool `json:"failover,omitempty"`
	// Pinned is the owner-pinned release id considered for the title;
	// PinStatus says what became of it ("used", "unhealthy: …", …).
	Pinned    string `json:"pinned,omitempty"`
	PinStatus string `json:"pinStatus,omitempty"`
	// Omitted counts candidates dropped from a persisted trace.
	Omitted int `json:"omitted,omitempty"`
	// Routing summarises the source-routing decision (routing.go).
	Routing string `json:"routing,omitempty"`
}

type searchCacheEntry struct {
	results []core.Release
	expires time.Time
}

type inflight struct {
	done chan struct{}
	res  *core.Resolution
	err  error
}

// Service implements core.Resolver.
type Service struct {
	indexer core.Indexer
	health  core.HealthChecker
	db      *sql.DB
	log     *slog.Logger
	now     func() time.Time

	cfgMu     sync.RWMutex
	rcfg      config.Resolver
	quality   config.Quality
	scorer    *FormatScorer
	scorerErr error

	mu            sync.RWMutex
	bad           map[string]badMark // release id -> blocklist mark
	reps          map[string]reputation
	avail         map[int64]availEntry
	searchCache   map[string]searchCacheEntry
	retentionDays int
	verified      map[string]time.Time // release id -> last passed health check
	failedHealth  map[string]time.Time // confirmed source failures, briefly skipped on subsequent resolves
	healthTTL     time.Duration

	tracesMu  sync.Mutex
	traces    []Trace
	maxTraces int

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgMu     sync.Mutex
	bgClosed bool
	bgWG     sync.WaitGroup

	flightMu sync.Mutex
	flights  map[string]*inflight

	route routingState // source routing: startup measurements and signals

	con consoleState // pins, release memory, health results, history (console.go)

	invalMu sync.Mutex
	inval   *clusterbus.Invalidator // nil on a standalone server
}

var _ core.Resolver = (*Service)(nil)

// New builds a resolver Service. indexer is the aggregate indexer
// (typically *newznab.Manager); health may be nil (health checks are
// then skipped); db may be nil (state then lives in memory).
func New(indexer core.Indexer, health core.HealthChecker, db *sql.DB, cfg config.Config, opts Options) *Service {
	s := &Service{
		indexer: indexer, health: health, db: db,
		bad: map[string]badMark{}, reps: map[string]reputation{},
		avail: map[int64]availEntry{}, searchCache: map[string]searchCacheEntry{},
		flights:       map[string]*inflight{},
		retentionDays: opts.RetentionDays,
		maxTraces:     opts.MaxTraces,
		verified:      map[string]time.Time{},
		healthTTL:     opts.HealthTTL,
	}
	s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	if s.healthTTL == 0 {
		s.healthTTL = 6 * time.Hour
	}
	s.log = opts.Logger
	if s.log == nil {
		s.log = slog.Default()
	}
	s.now = opts.Now
	if s.now == nil {
		s.now = time.Now
	}
	if s.maxTraces <= 0 {
		s.maxTraces = defaultMaxTraces
	}
	s.UpdateConfig(cfg)
	s.con.init(opts)
	s.loadPersisted(context.Background())
	return s
}

// UpdateConfig swaps the resolver/quality config snapshot.
func (s *Service) UpdateConfig(cfg config.Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.rcfg = cfg.Resolver
	s.quality = cfg.Quality
	s.scorer, s.scorerErr = NewFormatScorer(cfg.Resolver.CustomFormats, cfg.Resolver.CustomFormatScores)
}

func (s *Service) resolverCfg() config.Resolver {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.rcfg
}

func (s *Service) qualityCfg() config.Quality {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.quality
}

func (s *Service) searchTTL() time.Duration {
	if d := s.resolverCfg().SearchCacheTTL.D(); d > 0 {
		return d
	}
	return defaultSearchCacheTTL
}

func (s *Service) cooloff() time.Duration {
	if d := s.resolverCfg().UnavailableCooloff.D(); d > 0 {
		return d
	}
	return defaultUnavailableTTL
}

func (s *Service) maxCandidates() int {
	if n := s.resolverCfg().MaxCandidates; n > 0 {
		return n
	}
	return defaultMaxCandidates
}

// Available implements core.Resolver.
func (s *Service) Available(ctx context.Context, key core.RatingKey) (bool, bool) {
	e, ok := s.getAvailability(ctx, int64(key))
	if !ok {
		return false, false
	}
	return true, e.available
}

// MarkBad implements core.Resolver.
func (s *Service) MarkBad(ctx context.Context, releaseID string, reason string) error {
	if releaseID == "" {
		return fmt.Errorf("resolver: empty release id")
	}
	s.mu.Lock()
	m := badMark{reason: reason, at: s.now()}
	if prev, ok := s.bad[releaseID]; ok && prev.manual {
		// An automatic mark never downgrades an owner block (persistBad
		// keeps the owner's reason too); it only refreshes the clock.
		m.reason, m.manual = prev.reason, true
	}
	s.bad[releaseID] = m
	s.mu.Unlock()
	s.forgetVerified(ctx, releaseID)
	s.notePinsBlocked(ctx, releaseID, reason)
	return s.persistBad(ctx, s.blockEntryFor(releaseID, reason, false))
}

func (s *Service) isBad(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.bad[id]
	return ok && m.live(s.now())
}

// badMark is one in-memory blocklist mark. Automatic marks expire
// badRetention after they were last marked; owner (manual) blocks never do.
type badMark struct {
	reason string
	at     time.Time // when it was (last) marked
	manual bool
}

func (m badMark) live(now time.Time) bool {
	return m.manual || now.Sub(m.at) < badRetention
}

// badReason reports whether a release is on the live blocklist and why.
func (s *Service) badReason(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.bad[id]
	if !ok || !m.live(s.now()) {
		return "", false
	}
	return m.reason, true
}

// Traces returns the most recent resolution traces, newest first.
func (s *Service) Traces() []Trace {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	out := make([]Trace, len(s.traces))
	for i, tr := range s.traces {
		out[len(s.traces)-1-i] = tr
	}
	return out
}

func (s *Service) addTrace(tr Trace) {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	s.traces = append(s.traces, tr)
	if len(s.traces) > s.maxTraces {
		s.traces = append([]Trace(nil), s.traces[len(s.traces)-s.maxTraces:]...)
	}
}

// ProfileFor exposes SelectProfile against the live config.
func (s *Service) ProfileFor(caps core.ClientCaps, user *core.User) core.QualityProfile {
	return SelectProfile(s.qualityCfg(), caps, user)
}

func searchRequestFor(t *core.Title) core.SearchRequest {
	season, episode := t.Season, t.Episode
	if t.Key.Kind() == core.KindEpisode {
		if season == 0 {
			season = t.Key.Season()
		}
		if episode == 0 {
			episode = t.Key.Episode()
		}
	}
	if t.Type == core.TypeEpisode || t.Key.Kind() == core.KindEpisode {
		ids := t.ShowIDs
		if ids.TMDB == 0 && ids.IMDB == "" && ids.TVDB == 0 {
			ids = t.IDs
			// For an episode, IDs.TMDB is the episode id, not the show's.
			ids.TMDB = 0
			if t.Key.Kind() == core.KindEpisode {
				ids.TMDB = t.Key.TMDB()
			}
		}
		title := t.ShowTitle
		if title == "" {
			title = t.Title
		}
		return core.SearchRequest{Type: core.TypeEpisode, IDs: ids, Title: title, Year: t.ShowYear, Season: season, Episode: episode, EpisodeTitle: t.Title}
	}
	return core.SearchRequest{Type: core.TypeMovie, IDs: t.IDs, Title: t.Title, Year: t.Year}
}

func flightKey(req core.ResolveRequest, profile core.QualityProfile) string {
	ex := append([]string(nil), req.Exclude...)
	sort.Strings(ex)
	capsJSON, _ := json.Marshal(req.Client)
	profileJSON, _ := json.Marshal(profile)
	return fmt.Sprintf("%d|%s|%s|%s|%s|%s", req.Title.Key, profile.Name, strings.Join(ex, ","), capsJSON, profileJSON, req.Prefer)
}

// Resolve implements core.Resolver. Concurrent calls for the same
// title+profile share one search via a singleflight.
func (s *Service) Resolve(ctx context.Context, req core.ResolveRequest) (*core.Resolution, error) {
	if req.Title == nil {
		return nil, fmt.Errorf("resolver: nil title")
	}
	if s.indexer == nil {
		return nil, fmt.Errorf("resolver: no indexer configured")
	}
	profile := s.ProfileFor(req.Client, req.User)
	key := flightKey(req, profile)

	s.flightMu.Lock()
	if f, ok := s.flights[key]; ok {
		s.flightMu.Unlock()
		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &inflight{done: make(chan struct{})}
	s.flights[key] = f
	s.flightMu.Unlock()

	res, err := s.resolve(ctx, req, profile)
	f.res, f.err = res, err
	close(f.done)
	s.flightMu.Lock()
	delete(s.flights, key)
	s.flightMu.Unlock()
	return res, err
}

func (s *Service) resolve(ctx context.Context, req core.ResolveRequest, profile core.QualityProfile) (*core.Resolution, error) {
	t := req.Title
	trace := Trace{TitleKey: t.Key, TitleName: consoleTitleName(t), Profile: profile.Name, At: s.now(), Failover: len(req.Exclude) > 0}
	defer func() {
		s.addTrace(trace)
		s.recordHistory(ctx, trace)
	}()

	// The owner's pin for this title, when it applies to this request
	// (pins.go). It is tried first, like a viewer's Play Version pick.
	pin, pinNote := s.pinForResolve(req)
	trace.PinStatus = pinNote
	pinID := ""
	if pin != nil {
		pinID = pin.Release.ID
		trace.Pinned = pinID
	}

	// Fast path: cached unavailable answer, unless this is a failover
	// request that already knows about specific releases, the viewer
	// picked a specific version, or the owner pinned a release.
	if len(req.Exclude) == 0 && req.Prefer == "" && pin == nil {
		if e, ok := s.getAvailability(ctx, int64(t.Key)); ok && !e.available {
			trace.Err = "cached unavailable (cooloff)"
			return nil, fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable)
		}
	}

	sreq := searchRequestFor(t)
	searchStart := time.Now()
	results, searchErr := s.search(ctx, sreq)
	trace.SearchDuration = time.Since(searchStart)
	if searchErr != nil {
		if pin == nil {
			trace.Err = searchErr.Error()
			return nil, searchErr
		}
		// A pin carries its full release, so it can still play while
		// the indexers are failing; the search error stands otherwise.
		results = nil
	}
	s.rememberReleases(t, results)
	if pin != nil {
		results = withRelease(results, pin.Release)
	}

	rcfg := s.resolverCfg()
	cands, clientRejected, err := s.rankCandidates(ctx, req, sreq, profile, results, &trace, pinID)
	if err != nil {
		trace.Err = err.Error()
		return nil, err
	}
	failed := func(fallback error) error {
		if searchErr != nil {
			trace.Err = searchErr.Error()
			return searchErr
		}
		return fallback
	}

	if len(cands) == 0 {
		if pin != nil {
			s.notePin(ctx, t.Key, pinID, PinRejected, candidateReason(&trace, pinID))
			trace.PinStatus = "rejected: " + candidateReason(&trace, pinID)
		}
		// Client/profile restrictions and temporary source skips do not prove
		// that the title is unavailable for everyone.
		if !clientRejected && len(req.Exclude) == 0 && searchErr == nil {
			s.setAvailability(ctx, int64(t.Key), false, s.cooloff())
		}
		trace.Err = "no acceptable candidates"
		return nil, failed(fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable))
	}

	var optimisticErr error
	// The viewer picked a version ("Play Version"), or the owner pinned a
	// release: health-check that release first, ahead of the normal
	// ranking. When it is gone or unhealthy, fall back to the normal
	// choice below.
	preferTried := false
	prefer := req.Prefer
	if prefer == "" {
		prefer = pinID
	}
	if prefer != "" {
		prefIdx := -1
		for i := range cands {
			if cands[i].rel.ID == prefer {
				prefIdx = i
				break
			}
		}
		if prefIdx < 0 {
			s.log.Info("preferred release not among candidates; using normal ranking",
				"title", t.Key, "prefer", prefer)
			if pin != nil {
				reason := candidateReason(&trace, pinID)
				s.notePin(ctx, t.Key, pinID, PinRejected, reason)
				trace.PinStatus = "rejected: " + reason
			}
		} else {
			preferTried = true
			pref := cands[prefIdx]
			idx, nzb, verr := s.validateCandidates(ctx, []candidate{pref}, &trace, rcfg.HealthRace)
			if verr != nil {
				if pin != nil {
					s.notePin(ctx, t.Key, pinID, PinUnavailable, verr.Error())
					trace.PinStatus = "unavailable: " + verr.Error()
				}
				trace.Err = verr.Error()
				return nil, verr
			}
			if idx >= 0 {
				if pin != nil {
					s.notePin(ctx, t.Key, pinID, PinHealthy, "")
					trace.PinStatus = "used"
				}
				s.setAvailability(ctx, int64(t.Key), true, s.searchTTL())
				trace.Winner = pref.rel.ID
				return s.resolution(t.Key, cands, prefIdx, nzb, &trace), nil
			}
			if pin != nil {
				reason := candidateReason(&trace, pinID)
				s.notePin(ctx, t.Key, pinID, PinUnhealthy, reason)
				trace.PinStatus = "unhealthy: " + reason
			}
			s.log.Info("preferred release unhealthy; falling back to normal ranking",
				"title", t.Key, "prefer", prefer)
			cands = append(cands[:prefIdx], cands[prefIdx+1:]...)
			if len(cands) == 0 {
				trace.Err = "preferred release unhealthy and no other candidates"
				return nil, failed(fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable))
			}
		}
	}

	cands = s.prepareSources(ctx, cands, req.Title, &trace)
	if rcfg.OptimisticStart && !preferTried {
		nzb, err := s.indexer.FetchNZB(ctx, cands[0].rel)
		if err == nil {
			winner := cands[0]
			trace.Winner = winner.rel.ID
			trace.Candidates[winner.trace].Reason = "optimistic start: health check pending"
			s.optimisticCheck(winner.rel, nzb)
			return s.resolution(t.Key, cands, 0, nzb, &trace), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		trace.Candidates[cands[0].trace].Rejected = true
		trace.Candidates[cands[0].trace].Reason = "nzb fetch: " + err.Error()
		if !permanentFailure(err) {
			optimisticErr = err
		}
		cands = cands[1:]
	}
	index, nzb, err := s.validateCandidates(ctx, cands, &trace, rcfg.HealthRace)
	if err != nil {
		trace.Err = err.Error()
		return nil, err
	}
	if index >= 0 {
		s.setAvailability(ctx, int64(t.Key), true, s.searchTTL())
		trace.Winner = cands[index].rel.ID
		return s.resolution(t.Key, cands, index, nzb, &trace), nil
	}

	if optimisticErr != nil {
		return nil, optimisticErr
	}
	if searchErr != nil {
		return nil, failed(nil)
	}
	if !clientRejected && len(req.Exclude) == 0 && len(cands) <= maxHealthAttempts {
		s.setAvailability(ctx, int64(t.Key), false, min(s.cooloff(), failedHealthTTL))
	}
	trace.Err = "no healthy candidates"
	return nil, fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable)
}

func searchCacheKey(req core.SearchRequest) string {
	return fmt.Sprintf("%s|%v|%s|%d|%d|%d", req.Type, req.IDs, strings.ToLower(req.Title), req.Year, req.Season, req.Episode)
}

func (s *Service) search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	results, _, err := s.searchCached(ctx, req, false)
	return results, err
}

// searchCached answers from the search cache unless fresh is set; cached
// reports whether the answer came from the cache.
func (s *Service) searchCached(ctx context.Context, req core.SearchRequest, fresh bool) (results []core.Release, cached bool, err error) {
	key := searchCacheKey(req)
	s.mu.RLock()
	e, ok := s.searchCache[key]
	s.mu.RUnlock()
	if ok && !fresh && s.now().Before(e.expires) {
		return append([]core.Release(nil), e.results...), true, nil
	}
	results, err = s.indexer.Search(ctx, req)
	if err != nil {
		return nil, false, err
	}
	// Ensure Info is populated for indexers that don't parse names.
	for i := range results {
		if results[i].Info.Title == "" && results[i].Info.Height == 0 && results[i].Info.Source == core.SourceUnknown {
			results[i].Info = releaseparse.Parse(results[i].Name)
		}
		// Target torrents at the wanted episode here, once, so every
		// consumer (ranking, pins, the console's release memory, variants)
		// sees the same per-episode release ID.
		results[i] = withTarget(results[i], req)
	}
	s.mu.Lock()
	now := s.now()
	boundMap(s.searchCache, maxSearchCache, func(e searchCacheEntry) bool { return !now.Before(e.expires) })
	s.searchCache[key] = searchCacheEntry{results: append([]core.Release(nil), results...), expires: s.now().Add(s.searchTTL())}
	s.mu.Unlock()
	return results, false, nil
}

// scoreCandidates filters and ranks search results for a request and
// profile. trace may be nil (the variants path); candidate.trace is then
// -1 and no trace rows are written.
func (s *Service) scoreCandidates(ctx context.Context, req core.ResolveRequest, sreq core.SearchRequest, profile core.QualityProfile, results []core.Release, trace *Trace) ([]candidate, bool, error) {
	return s.rankCandidates(ctx, req, sreq, profile, results, trace, "")
}

// rankCandidates is scoreCandidates with the owner's pinned release id
// (pinID, "" for none): the pin is judged by pinRejectReason's hard gates
// instead of the profile and identity rules, and is otherwise scored and
// traced like every other result. Interactive search reads the same trace.
func (s *Service) rankCandidates(ctx context.Context, req core.ResolveRequest, sreq core.SearchRequest, profile core.QualityProfile, results []core.Release, trace *Trace, pinID string) ([]candidate, bool, error) {
	t := req.Title
	exclude := map[string]bool{}
	for _, id := range req.Exclude {
		exclude[id] = true
	}
	rcfg := s.resolverCfg()
	var cands []candidate
	clientRejected := false
	for _, rel := range results {
		rel = withTarget(rel, sreq)
		ct := CandidateTrace{ReleaseID: rel.ID, Name: rel.Name, Indexer: rel.Indexer, Size: rel.Size}
		pinned := pinID != "" && rel.ID == pinID
		ct.Pinned = pinned
		preferred := pinned || (req.Prefer != "" && rel.ID == req.Prefer)
		var reason string
		if pinned {
			reason = s.pinRejectReason(rel, sreq, req.Client, exclude)
		} else {
			reason = s.rejectReason(rel, t, sreq, profile, rcfg, req.Client, exclude, preferred)
		}
		if reason != "" {
			ct.Rejected = true
			switch reason {
			case "above profile max height", "dolby vision not allowed for client", "video codec unsupported by client", "quality excluded by imported profile", "source recently failed health check; retrying later":
				clientRejected = true
			}
			ct.Reason = reason
			if trace != nil {
				trace.Candidates = append(trace.Candidates, ct)
			}
			continue
		}
		sc, err := s.scoreRelease(rel, profile, req.Client, t)
		if err != nil {
			return nil, clientRejected, err
		}
		ct.CustomFormats = sc.matches
		if minimum := minFormatScore(rcfg, profile); minimum != nil && sc.format < *minimum && !preferred {
			clientRejected = true
			ct.Rejected = true
			ct.Reason = "below minimum custom format score"
			if trace != nil {
				trace.Candidates = append(trace.Candidates, ct)
			}
			continue
		}
		ct.CompatibilityReason = sc.compat
		rel.Score = sc.total
		ct.Score = rel.Score
		verified := s.recentlyVerified(ctx, rel.ID)
		if verified {
			ct.CompatibilityReason = strings.TrimSpace(sc.compat + "; source recently verified playable")
			ct.CompatibilityReason = strings.TrimPrefix(ct.CompatibilityReason, "; ")
		}
		tr := -1
		if trace != nil {
			trace.Candidates = append(trace.Candidates, ct)
			tr = len(trace.Candidates) - 1
		}
		cands = append(cands, candidate{rel: rel, trace: tr, verified: verified, bandwidth: sourceBandwidthFit(sizedForEpisode(rel), req.Client, t)})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		// A previously working LAN remux must not outrank a source that
		// fits this client's slower connection. Unknown sizes remain usable
		// and an oversized source can still be transcoded as a fallback.
		if cands[i].bandwidth != cands[j].bandwidth {
			return cands[i].bandwidth > cands[j].bandwidth
		}
		// Availability wins over soft quality preferences, after all hard
		// profile/client gates above. Keep ranking within each group.
		if cands[i].verified != cands[j].verified {
			return cands[i].verified
		}
		return cands[i].rel.Score > cands[j].rel.Score
	})
	cands = s.routeSources(cands, req.Title, trace)
	return cands, clientRejected, nil
}

// releaseScore is one release's total ranking score and its parts.
type releaseScore struct {
	total   float64
	format  int
	matches []FormatMatch
	compat  string
}

// scoreRelease computes the ranking score of a release for a profile and
// client: the profile score plus custom formats plus client compatibility.
func (s *Service) scoreRelease(rel core.Release, profile core.QualityProfile, caps core.ClientCaps, t *core.Title) (releaseScore, error) {
	s.cfgMu.RLock()
	scorer, scorerErr := s.scorer, s.scorerErr
	s.cfgMu.RUnlock()
	if scorerErr != nil {
		return releaseScore{}, scorerErr
	}
	formatScore, matches, err := scorer.ScoreWithScores(rel, profile.CustomFormatScores)
	if err != nil {
		return releaseScore{}, err
	}
	compatibility, note := compatibilityScore(sizedForEpisode(rel), caps, t)
	return releaseScore{
		total:   s.score(sizedForEpisode(rel), profile, t) + float64(formatScore) + compatibility,
		format:  formatScore,
		matches: matches,
		compat:  note,
	}, nil
}

// minFormatScore is the profile's minimum custom format score, else the
// resolver-wide one; nil when neither is set.
func minFormatScore(rcfg config.Resolver, profile core.QualityProfile) *int {
	if profile.MinCustomFormatScore != nil {
		return profile.MinCustomFormatScore
	}
	return rcfg.MinCustomFormatScore
}

// rejectReason returns "" to accept, or the reason to drop a release.
// preferred (the viewer's explicit Play Version pick) bypasses the
// profile-policy gates — max height, DV policy, imported quality gates,
// per-profile size range — but never the hard client gates or the
// identity/absolute-size sanity checks.
func (s *Service) rejectReason(rel core.Release, t *core.Title, sreq core.SearchRequest,
	profile core.QualityProfile, rcfg config.Resolver, caps core.ClientCaps, exclude map[string]bool, preferred bool) string {
	if rel.Password {
		return "passworded"
	}
	if exclude[rel.ID] {
		return "excluded (already tried)"
	}
	if s.isBad(rel.ID) {
		return "marked bad"
	}
	if s.recentlyFailedHealth(rel.ID) {
		return "source recently failed health check; retrying later"
	}
	lowerName := strings.ToLower(rel.Name)
	for _, term := range rcfg.BlockedTerms {
		if term != "" && strings.Contains(lowerName, strings.ToLower(term)) {
			return "blocked term: " + term
		}
	}
	for _, g := range rcfg.BlockedGroups {
		if g != "" && strings.EqualFold(rel.Info.Group, g) {
			return "blocked group: " + g
		}
	}
	if rel.Info.Source == core.SourceCam && !(rcfg.AllowCam && profile.AllowCam) {
		return "cam not allowed"
	}
	// Samples ("...Sample.mkv", "-sample" parts).
	for _, tok := range strings.FieldsFunc(lowerName, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == ' '
	}) {
		if tok == "sample" {
			return "sample"
		}
	}
	isEpisode := sreq.Type == core.TypeEpisode
	idMatch, idConflict := releaseparse.CompareTitleIDs(rel.TitleIDs, sreq.IDs)
	if idConflict {
		return "indexer title identifier mismatch"
	}
	// Title / obfuscation.
	wantTitle := sreq.Title
	titleOK := releaseparse.TitleMatches(rel.Info, wantTitle) ||
		(!isEpisode && t.OriginalTitle != "" && releaseparse.TitleMatches(rel.Info, t.OriginalTitle))
	if !titleOK && !idMatch {
		obf := releaseparse.IsObfuscated(rel.Info.Title)
		if obf && !releaseparse.HasUsableInfo(rel.Info) {
			return "obfuscated, no usable info"
		}
		// Querying by ID does not prove that the indexer honored it.
		// Only identifiers actually reported on this result can establish
		// an otherwise unreadable or alternate title's identity.
		return "title mismatch"
	}
	if isEpisode {
		releaseYear := releaseparse.EpisodeSeriesYear(rel.Name, rel.Info.Year)
		if !releaseparse.SeriesYearMatches(releaseYear, sreq.Year) {
			return "series year mismatch"
		}
		if releaseYear == 0 && !idMatch && !releaseparse.EpisodeTitleMatches(rel.Name, sreq.EpisodeTitle) {
			return "episode title mismatch"
		}
		if rel.Info.FullSeason {
			// Usenet season packs are skipped in v1; a torrent pack streams
			// the wanted episode's file through debrid (Release.Target).
			if rel.Protocol != core.ProtocolTorrent {
				return "season pack (skipped in v1)"
			}
			if !packCoversSeason(rel.Info, sreq.Season) {
				return "season/episode mismatch"
			}
		} else if !releaseparse.EpisodeMatches(rel.Info, sreq.Season, sreq.Episode) {
			return "season/episode mismatch"
		}
	} else {
		if len(rel.Info.Episodes) > 0 || rel.Info.FullSeason {
			return "episode release for a movie request"
		}
		if !releaseparse.YearMatches(rel.Info.Year, sreq.Year) {
			return "year mismatch"
		}
	}
	// Client / profile hard gates. The viewer's explicit pick bypasses the
	// profile-policy gates (an explicit 4K pick on a 1080p profile must
	// still resolve), never the client's codec support.
	if !preferred && len(profile.AllowedQualities) > 0 {
		allowed := false
		for _, q := range profile.AllowedQualities {
			if q == releaseQuality(rel.Info) {
				allowed = true
			}
		}
		if !allowed {
			return "quality excluded by imported profile"
		}
	}
	if !preferred && profile.MaxHeight > 0 && rel.Info.Height > profile.MaxHeight {
		return "above profile max height"
	}
	if !preferred && rel.Info.DolbyVision && !profile.AllowDolbyVision && !(rel.Info.HDR10 && profile.AllowHDR) {
		return "dolby vision not allowed for client"
	}
	if rel.Info.VideoCodec != "" && len(caps.VideoCodecs) > 0 && !caps.SupportsVideo(rel.Info.VideoCodec) {
		return "video codec unsupported by client"
	}
	// Size sanity against runtime. The per-profile size range is policy
	// (a 4K remux exceeds a 1080p profile's ceiling), so an explicit pick
	// bypasses it; the absolute implausibility floor below still applies.
	// A season pack's size is the whole season's, so it is not checked.
	size := sizedForEpisode(rel).Size
	if !preferred && size > 0 && t.Runtime > 0 {
		mbPerMin := float64(size) / (1 << 20) / t.Runtime.Minutes()
		if profile.MinSizeMBPerMin > 0 && mbPerMin < profile.MinSizeMBPerMin {
			return "size too small for runtime"
		}
		if profile.MaxSizeMBPerMin > 0 && mbPerMin > profile.MaxSizeMBPerMin {
			return "size too large for runtime"
		}
	} else if size > 0 && size < 5<<20 {
		return "implausibly small"
	}
	return ""
}

// withTarget marks a torrent candidate for an episode request with the
// wanted episode, so debrid streams that file of a multi-file torrent. A
// season pack gets a per-episode release ID: health, bad marks, pins and
// cached bytes then apply to the episode file, not the whole pack.
func withTarget(rel core.Release, sreq core.SearchRequest) core.Release {
	if sreq.Type != core.TypeEpisode || rel.Protocol != core.ProtocolTorrent || sreq.Episode <= 0 {
		return rel
	}
	if rel.Target != nil {
		target := *rel.Target
		target.ShowYear, target.EpisodeTitle = sreq.Year, sreq.EpisodeTitle
		target.ExpectedIDs, target.ReportedIDs = sreq.IDs, rel.TitleIDs
		rel.Target = &target
		return rel // enrich cached identities without deriving their ID again
	}
	target := core.ReleaseTarget{Season: sreq.Season, Episode: sreq.Episode, ShowYear: sreq.Year, EpisodeTitle: sreq.EpisodeTitle, ExpectedIDs: sreq.IDs, ReportedIDs: rel.TitleIDs}
	rel.Target = &target
	if rel.Info.FullSeason {
		rel.ID = core.TargetReleaseID(rel.ID, target)
	}
	return rel
}

// sizedForEpisode hides a season pack's total size from per-runtime size
// checks and bitrate estimates.
func sizedForEpisode(rel core.Release) core.Release {
	if rel.Info.FullSeason && rel.Protocol == core.ProtocolTorrent {
		rel.Size = 0
	}
	return rel
}

func packCoversSeason(info core.ReleaseInfo, season int) bool {
	if len(info.Seasons) == 0 {
		return true // "Complete series": the file chooser verifies the episode
	}
	for _, s := range info.Seasons {
		if s == season {
			return true
		}
	}
	return false
}

func indexScore[T comparable](list []T, v T, per float64) float64 {
	for i, x := range list {
		if x == v {
			return float64(len(list)-i) * per
		}
	}
	return 0
}

// score ranks an accepted release against a profile. Higher is better.
func (s *Service) score(rel core.Release, profile core.QualityProfile, t *core.Title) float64 {
	info := rel.Info
	var sc float64
	// Resolution fit.
	switch {
	case info.Height == 0:
	case profile.MaxHeight > 0 && info.Height == profile.MaxHeight:
		sc += 50
	case info.Height >= profile.MinHeight && (profile.MaxHeight == 0 || info.Height <= profile.MaxHeight):
		maxH := float64(max(profile.MaxHeight, 1))
		sc += 30 + 20*float64(info.Height)/maxH
	default: // below floor
		maxH := float64(max(profile.MaxHeight, 1))
		sc += 10 * float64(info.Height) / maxH
	}
	// Source / codec / audio preference order.
	if v := indexScore(profile.PreferredSources, info.Source, 10); v > 0 {
		sc += v
	} else if info.Source != core.SourceUnknown && info.Source != core.SourceCam {
		sc += 3
	}
	sc += indexScore(profile.PreferredCodecs, info.VideoCodec, 6)
	sc += indexScore(profile.PreferredAudio, info.AudioCodec, 4)
	if info.Atmos {
		sc += 3
	}
	// HDR/DV.
	if info.HDR10 {
		if profile.AllowHDR {
			sc += 8
		} else {
			sc -= 15
		}
	}
	if info.DolbyVision && profile.AllowDolbyVision {
		sc += 10
	}
	// Size sanity: in-range sizes get a small bonus, unknown size none.
	if rel.Size > 0 && t.Runtime > 0 {
		mbPerMin := float64(rel.Size) / (1 << 20) / t.Runtime.Minutes()
		if (profile.MinSizeMBPerMin == 0 || mbPerMin >= profile.MinSizeMBPerMin) &&
			(profile.MaxSizeMBPerMin == 0 || mbPerMin <= profile.MaxSizeMBPerMin) {
			sc += 5
		}
	}
	// Age penalty.
	if !rel.PubDate.IsZero() {
		ageDays := s.now().Sub(rel.PubDate).Hours() / 24
		if ageDays > 0 {
			sc -= math.Min(ageDays/365*2, 15)
			if s.retentionDays > 0 && ageDays > float64(s.retentionDays) {
				sc -= 20
			}
		}
	}
	// Grabs (log scale).
	if rel.Grabs > 0 {
		sc += math.Min(math.Log10(float64(rel.Grabs)+1)*3, 9)
	}
	if info.Proper || info.Repack {
		sc += 6
	}
	// Languages: foreign-only audio is a big penalty unless preferred.
	if len(info.Languages) > 0 {
		ok := false
		for _, l := range info.Languages {
			if l == "english" || l == "multi" {
				ok = true
			}
			for _, pref := range profile.PreferredLanguages {
				if strings.EqualFold(l, pref) {
					ok = true
					sc += 5
				}
			}
		}
		if !ok {
			sc -= 25
		}
	}
	// Indexer reputation.
	s.mu.RLock()
	rep := s.reps[rel.Indexer]
	s.mu.RUnlock()
	sc += rep.score()
	return sc
}
