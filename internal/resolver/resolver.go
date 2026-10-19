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

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

const (
	defaultMaxTraces      = 50
	maxHealthAttempts     = 8
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
}

// CandidateTrace explains one candidate's fate in a resolution.
type CandidateTrace struct {
	ReleaseID           string        `json:"releaseId"`
	Name                string        `json:"name"`
	Indexer             string        `json:"indexer"`
	Score               float64       `json:"score"`
	CustomFormats       []FormatMatch `json:"customFormats,omitempty"`
	CompatibilityReason string        `json:"compatibilityReason,omitempty"`
	Rejected            bool          `json:"rejected"`
	Reason              string        `json:"reason,omitempty"`
	HealthChecked       bool          `json:"healthChecked"`
	Healthy             bool          `json:"healthy"`
	FetchDuration       time.Duration `json:"fetchDuration"`
	HealthDuration      time.Duration `json:"healthDuration"`
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
	bad           map[string]string
	reps          map[string]reputation
	avail         map[int64]availEntry
	searchCache   map[string]searchCacheEntry
	retentionDays int
	verified      map[string]time.Time // release id -> last passed health check
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
}

var _ core.Resolver = (*Service)(nil)

// New builds a resolver Service. indexer is the aggregate indexer
// (typically *newznab.Manager); health may be nil (health checks are
// then skipped); db may be nil (state then lives in memory).
func New(indexer core.Indexer, health core.HealthChecker, db *sql.DB, cfg config.Config, opts Options) *Service {
	s := &Service{
		indexer: indexer, health: health, db: db,
		bad: map[string]string{}, reps: map[string]reputation{},
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
	s.bad[releaseID] = reason
	s.mu.Unlock()
	s.forgetVerified(ctx, releaseID)
	return s.persistBad(ctx, releaseID, reason)
}

func (s *Service) isBad(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.bad[id]
	return ok
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
		return core.SearchRequest{Type: core.TypeEpisode, IDs: ids, Title: title, Year: t.Year, Season: season, Episode: episode}
	}
	return core.SearchRequest{Type: core.TypeMovie, IDs: t.IDs, Title: t.Title, Year: t.Year}
}

func flightKey(req core.ResolveRequest, profile core.QualityProfile) string {
	ex := append([]string(nil), req.Exclude...)
	sort.Strings(ex)
	capsJSON, _ := json.Marshal(req.Client)
	profileJSON, _ := json.Marshal(profile)
	return fmt.Sprintf("%d|%s|%s|%s|%s", req.Title.Key, profile.Name, strings.Join(ex, ","), capsJSON, profileJSON)
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
	trace := Trace{TitleKey: t.Key, TitleName: displayTitle(t), Profile: profile.Name, At: s.now()}
	defer func() { s.addTrace(trace) }()

	// Fast path: cached unavailable answer, unless this is a failover
	// request that already knows about specific releases.
	if len(req.Exclude) == 0 {
		if e, ok := s.getAvailability(ctx, int64(t.Key)); ok && !e.available {
			trace.Err = "cached unavailable (cooloff)"
			return nil, fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable)
		}
	}

	sreq := searchRequestFor(t)
	searchStart := time.Now()
	results, err := s.search(ctx, sreq)
	trace.SearchDuration = time.Since(searchStart)
	if err != nil {
		trace.Err = err.Error()
		return nil, err
	}

	exclude := map[string]bool{}
	for _, id := range req.Exclude {
		exclude[id] = true
	}
	rcfg := s.resolverCfg()
	var cands []candidate
	clientRejected := false
	for _, rel := range results {
		ct := CandidateTrace{ReleaseID: rel.ID, Name: rel.Name, Indexer: rel.Indexer}
		if reason := s.rejectReason(rel, t, sreq, profile, rcfg, req.Client, exclude); reason != "" {
			ct.Rejected = true
			switch reason {
			case "above profile max height", "dolby vision not allowed for client", "video codec unsupported by client", "quality excluded by imported profile":
				clientRejected = true
			}
			ct.Reason = reason
			trace.Candidates = append(trace.Candidates, ct)
			continue
		}
		s.cfgMu.RLock()
		scorer, scorerErr := s.scorer, s.scorerErr
		s.cfgMu.RUnlock()
		if scorerErr != nil {
			trace.Err = scorerErr.Error()
			return nil, scorerErr
		}
		formatScore, matches, scoreErr := scorer.ScoreWithScores(rel, profile.CustomFormatScores)
		if scoreErr != nil {
			trace.Err = scoreErr.Error()
			return nil, scoreErr
		}
		ct.CustomFormats = matches
		minimum := rcfg.MinCustomFormatScore
		if profile.MinCustomFormatScore != nil {
			minimum = profile.MinCustomFormatScore
		}
		if minimum != nil && formatScore < *minimum {
			clientRejected = true
			ct.Rejected = true
			ct.Reason = "below minimum custom format score"
			trace.Candidates = append(trace.Candidates, ct)
			continue
		}
		compatibility, note := compatibilityScore(rel, req.Client, t)
		ct.CompatibilityReason = note
		rel.Score = s.score(rel, profile, t) + float64(formatScore) + compatibility
		ct.Score = rel.Score
		trace.Candidates = append(trace.Candidates, ct)
		cands = append(cands, candidate{rel: rel, trace: len(trace.Candidates) - 1})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rel.Score > cands[j].rel.Score })

	if len(cands) == 0 {
		// Client/profile rejection is not proof that the title is unavailable for everyone.
		if !clientRejected && len(req.Exclude) == 0 {
			s.setAvailability(ctx, int64(t.Key), false, s.cooloff())
		}
		trace.Err = "no acceptable candidates"
		return nil, fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable)
	}

	var optimisticErr error
	if rcfg.OptimisticStart {
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
	if !clientRejected && len(req.Exclude) == 0 && len(cands) <= maxHealthAttempts {
		s.setAvailability(ctx, int64(t.Key), false, s.cooloff())
	}
	trace.Err = "no healthy candidates"
	return nil, fmt.Errorf("%q: %w", t.Title, core.ErrNotAvailable)
}

func displayTitle(t *core.Title) string {
	if t.ShowTitle != "" && (t.Type == core.TypeEpisode || t.Type == core.TypeSeason) {
		return t.ShowTitle
	}
	return t.Title
}

func (s *Service) search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	key := fmt.Sprintf("%s|%v|%s|%d|%d|%d", req.Type, req.IDs, strings.ToLower(req.Title), req.Year, req.Season, req.Episode)
	s.mu.RLock()
	e, ok := s.searchCache[key]
	s.mu.RUnlock()
	if ok && s.now().Before(e.expires) {
		return append([]core.Release(nil), e.results...), nil
	}
	results, err := s.indexer.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	// Ensure Info is populated for indexers that don't parse names.
	for i := range results {
		if results[i].Info.Title == "" && results[i].Info.Height == 0 && results[i].Info.Source == core.SourceUnknown {
			results[i].Info = releaseparse.Parse(results[i].Name)
		}
	}
	s.mu.Lock()
	s.searchCache[key] = searchCacheEntry{results: append([]core.Release(nil), results...), expires: s.now().Add(s.searchTTL())}
	s.mu.Unlock()
	return results, nil
}

// rejectReason returns "" to accept, or the reason to drop a release.
func (s *Service) rejectReason(rel core.Release, t *core.Title, sreq core.SearchRequest,
	profile core.QualityProfile, rcfg config.Resolver, caps core.ClientCaps, exclude map[string]bool) string {
	if rel.Password {
		return "passworded"
	}
	if exclude[rel.ID] {
		return "excluded (already tried)"
	}
	if s.isBad(rel.ID) {
		return "marked bad"
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
	// Title / obfuscation.
	wantTitle := sreq.Title
	titleOK := releaseparse.TitleMatches(rel.Info, wantTitle) ||
		(t.OriginalTitle != "" && releaseparse.TitleMatches(rel.Info, t.OriginalTitle))
	if !titleOK {
		strongID := sreq.IDs.IMDB != "" || sreq.IDs.TMDB > 0 || sreq.IDs.TVDB > 0
		obf := releaseparse.IsObfuscated(rel.Info.Title)
		if obf && !releaseparse.HasUsableInfo(rel.Info) {
			return "obfuscated, no usable info"
		}
		if !(strongID && obf && releaseparse.HasUsableInfo(rel.Info)) {
			return "title mismatch"
		}
	}
	if isEpisode {
		if rel.Info.FullSeason {
			return "season pack (skipped in v1)"
		}
		if !releaseparse.EpisodeMatches(rel.Info, sreq.Season, sreq.Episode) {
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
	// Client / profile hard gates.
	if len(profile.AllowedQualities) > 0 {
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
	if profile.MaxHeight > 0 && rel.Info.Height > profile.MaxHeight {
		return "above profile max height"
	}
	if rel.Info.DolbyVision && !profile.AllowDolbyVision && !(rel.Info.HDR10 && profile.AllowHDR) {
		return "dolby vision not allowed for client"
	}
	if rel.Info.VideoCodec != "" && len(caps.VideoCodecs) > 0 && !caps.SupportsVideo(rel.Info.VideoCodec) {
		return "video codec unsupported by client"
	}
	// Size sanity against runtime.
	if rel.Size > 0 && t.Runtime > 0 {
		mbPerMin := float64(rel.Size) / (1 << 20) / t.Runtime.Minutes()
		if profile.MinSizeMBPerMin > 0 && mbPerMin < profile.MinSizeMBPerMin {
			return "size too small for runtime"
		}
		if profile.MaxSizeMBPerMin > 0 && mbPerMin > profile.MaxSizeMBPerMin {
			return "size too large for runtime"
		}
	} else if rel.Size > 0 && rel.Size < 5<<20 {
		return "implausibly small"
	}
	return ""
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
