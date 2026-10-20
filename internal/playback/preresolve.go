package playback

import (
	"context"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const preResolveTTL = 10 * time.Minute

const (
	detailPrefetchBytes  = 2 << 20
	bigWarmPrefetchBytes = 48 << 20
	maxWarmPrefetchBytes = 64 << 20
)

type warmEntry struct {
	profile    string
	profileObj core.QualityProfile
	caps       core.ClientCaps
	done       chan struct{}
	ready      sync.Once
	expires    time.Time
	runtime    time.Duration // title runtime, for size rules without a probe
	res        *core.Resolution
	probe      *probeCache
}

type probeCache struct {
	releaseID string
	info      core.MediaInfo
	points    []IndexPoint
}

// PreResolve schedules detail-page warming; it is the core.PrewarmDetail
// form of Prewarm, kept for existing callers.
func (m *Manager) PreResolve(ctx context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps) bool {
	return m.Prewarm(ctx, user, key, caps, core.PrewarmOptions{Reason: core.PrewarmDetail})
}

// Prewarm implements core.Prewarmer: it resolves, opens, probes (including
// the container keyframe index) and pre-buffers a title ahead of Play, so
// the later Prepare for the same user, client and title needs no resolver
// or probe round trip. Request cancellation does not cancel a warm-up;
// manager shutdown always does. It neither reserves a live stream nor
// writes resume/history. False means skipped.
//
// Budgets: two warm-ups in flight server-wide, one per user, six
// requests/minute/user. A detail warm-up runs 8s, prefetches 2 MiB and is
// skipped while anything is playing. Trailer/post-play warm-ups may run
// during playback (DuringPlayback), prefetch 48 MiB by default (hard cap
// 64 MiB) and get 45s. Prefetch goes through Engine.Prefetch, which runs
// at core.PriorityPrefetch and yields to live streams.
func (m *Manager) Prewarm(_ context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps, opts core.PrewarmOptions) bool {
	if user == nil || caps.ClientID == "" || caps.Product == "" {
		return false
	}
	reason := opts.Reason
	if reason != core.PrewarmTrailer && reason != core.PrewarmPostPlay {
		reason = core.PrewarmDetail
	}
	prefetchBytes := opts.PrefetchBytes
	if prefetchBytes <= 0 {
		if reason == core.PrewarmDetail {
			prefetchBytes = detailPrefetchBytes
		} else {
			prefetchBytes = bigWarmPrefetchBytes
		}
	}
	if prefetchBytes > maxWarmPrefetchBytes {
		prefetchBytes = maxWarmPrefetchBytes
	}
	timeout := 8 * time.Second
	if reason != core.PrewarmDetail {
		timeout = 45 * time.Second
	}
	id := identity{userID: user.ID, clientID: caps.ClientID, key: key}
	now := m.now()
	m.mu.Lock()
	if m.closed || m.byIdent[id] != nil || m.hasFlightLocked(id) {
		m.mu.Unlock()
		return false
	}
	for ident, e := range m.warm {
		if !now.Before(e.expires) {
			delete(m.warm, ident)
		}
	}
	if m.warm[id] != nil {
		m.mu.Unlock()
		return false
	}
	active := 0
	for ident, e := range m.warm {
		select {
		case <-e.done:
			continue
		default:
		}
		active++
		if ident.userID == user.ID {
			m.mu.Unlock()
			return false
		}
	}
	if active >= 2 {
		m.mu.Unlock()
		return false
	}
	if len(m.warm) >= 32 {
		var oldest identity
		var expiry time.Time
		for ident, e := range m.warm {
			select {
			case <-e.done:
				if expiry.IsZero() || e.expires.Before(expiry) {
					oldest, expiry = ident, e.expires
				}
			default:
			}
		}
		if expiry.IsZero() {
			m.mu.Unlock()
			return false
		}
		delete(m.warm, oldest)
	}
	// Active playback takes priority over speculation, unless the caller
	// is warming the next thing while the viewer watches (a trailer or
	// the end of a film).
	if !opts.DuringPlayback && (len(m.sessions) > 0 || m.pendingN > 0) {
		m.mu.Unlock()
		return false
	}
	budgetReason := "speculative"
	if reason == core.PrewarmDetail {
		budgetReason = core.PrewarmDetail
	}
	bk := warmBudgetKey{userID: user.ID, reason: budgetReason}
	requests := m.warmRequests[bk]
	recent := requests[:0]
	for _, at := range requests {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= 6 {
		m.mu.Unlock()
		return false
	}
	m.warmRequests[bk] = append(recent, now)
	profileObj := m.deps.SelectProfile(caps, user)
	e := &warmEntry{profile: profileObj.Name, profileObj: profileObj, caps: caps, done: make(chan struct{}), expires: now.Add(preResolveTTL)}
	m.warm[id] = e
	u := *user
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			if e.res == nil && m.warm[id] == e {
				delete(m.warm, id)
			}
			m.mu.Unlock()
			e.ready.Do(func() { close(e.done) })
		}()
		ctx, cancel := context.WithTimeout(m.ctx, timeout)
		defer cancel()
		title, err := m.deps.Catalog.Get(ctx, key)
		if err != nil || title == nil || title.Type == core.TypeShow || title.Type == core.TypeSeason {
			return
		}
		res, err := m.deps.Resolver.Resolve(ctx, core.ResolveRequest{Title: title, Client: caps, User: &u})
		if err != nil || res == nil {
			return
		}
		src, err := m.deps.Engine.Open(ctx, res.Release.ID, res.NZB)
		if err != nil {
			return
		}
		session := newSession(randomID(16), id, &u, caps, title, e.profile, m.now())
		generation, err := m.probeGeneration(ctx, session, res, src, func(size int64) core.MediaInfo {
			info := EstimateInfo(title, caps, m.deps.SelectProfile(caps, &u))
			info.Size = size
			return info
		})
		if err != nil {
			return
		}
		defer m.dropGeneration(generation)
		facts := &probeCache{releaseID: res.Release.ID, info: generation.infoSnapshot(), points: generation.timeMapSnapshot().Points}
		if facts.info.Estimated {
			facts = nil
		}
		// Publish before prefetch so a play request need not wait for background IO.
		m.mu.Lock()
		e.res, e.probe, e.runtime = res, facts, title.Runtime
		m.mu.Unlock()
		e.ready.Do(func() { close(e.done) })
		m.log.Debug("prewarm ready", "key", key, "reason", reason, "release", res.Release.Name, "prefetch", prefetchBytes)
		_ = m.deps.Engine.Prefetch(ctx, res.Release.ID, res.NZB, 0, prefetchBytes)
		// Seek indexes are optional for starting playback. Keep filling one
		// under the warm-up budget, but never hold the ready signal for it.
		select {
		case <-generation.indexReady:
		case <-ctx.Done():
			return
		}
		complete := &probeCache{releaseID: res.Release.ID, info: generation.infoSnapshot(), points: generation.timeMapSnapshot().Points}
		if !complete.info.Estimated {
			m.mu.Lock()
			if m.warm[id] == e {
				e.probe = complete
			}
			m.mu.Unlock()
		}
	}()
	return true
}

// warmFacts are the release properties a Play-time profile is checked
// against, from the cached probe when there is one, else the parsed
// release name.
type warmFacts struct {
	height   int
	codec    string
	hdr      bool
	dv       bool
	size     int64
	duration time.Duration
	known    bool // at least one property was known
}

func (e *warmEntry) facts() warmFacts {
	var f warmFacts
	if e.probe != nil {
		info := e.probe.info
		f.size, f.duration = info.Size, info.Duration
		if v := info.Video(); v != nil {
			f.height, f.codec = v.Height, v.Codec
			f.hdr = v.ColorTransfer == "smpte2084" || v.ColorTransfer == "arib-std-b67"
			f.dv = v.DolbyVision
		}
		f.known = f.height > 0 || f.codec != "" || f.size > 0
		return f
	}
	if e.res != nil {
		ri := e.res.Release.Info
		f.height, f.codec, f.hdr, f.dv = ri.Height, ri.VideoCodec, ri.HDR10, ri.DolbyVision
		f.size, f.duration = e.res.Release.Size, e.runtime
		f.known = f.height > 0 || f.codec != "" || f.size > 0
	}
	return f
}

// warmAcceptable reports whether the warm release is acceptable for the
// Play-time profile and caps. The detail request that produced the warm
// entry derived caps without decision query params or the client-profile
// extra, so its profile name frequently differs from the Play-time one
// for the same device; equality is sufficient but not required. Unknown
// properties never reject; with no facts at all, the Play-time profile
// must be at least as permissive as the warm one.
func warmAcceptable(e *warmEntry, profile core.QualityProfile, caps core.ClientCaps) bool {
	if e == nil || e.res == nil {
		return false
	}
	f := e.facts()
	if !f.known {
		w := e.profileObj
		heightOK := profile.MaxHeight == 0 || w.MaxHeight == 0 || profile.MaxHeight >= w.MaxHeight
		return heightOK && profile.MinHeight <= w.MinHeight &&
			(profile.AllowHDR || !w.AllowHDR) && (profile.AllowDolbyVision || !w.AllowDolbyVision)
	}
	capHeight := profile.MaxHeight
	if caps.MaxHeight > 0 && (capHeight == 0 || caps.MaxHeight < capHeight) {
		capHeight = caps.MaxHeight
	}
	if capHeight > 0 && f.height > capHeight {
		return false
	}
	if profile.MinHeight > 0 && f.height > 0 && f.height < profile.MinHeight {
		return false
	}
	if f.codec != "" && len(caps.VideoCodecs) > 0 && !caps.SupportsVideo(f.codec) {
		return false
	}
	if f.hdr && !(profile.AllowHDR && caps.HDR10) {
		return false
	}
	if f.dv && !(profile.AllowDolbyVision && caps.DolbyVision) {
		return false
	}
	if f.size > 0 && f.duration > 0 {
		mbPerMin := float64(f.size) / (1024 * 1024) / f.duration.Minutes()
		if profile.MaxSizeMBPerMin > 0 && mbPerMin > profile.MaxSizeMBPerMin {
			return false
		}
		if profile.MinSizeMBPerMin > 0 && mbPerMin < profile.MinSizeMBPerMin {
			return false
		}
	}
	return true
}

// takeWarmResolution consumes the warm entry for id when its release is
// acceptable for the Play-time profile and caps (and, for a variant,
// is the picked release); an unacceptable entry is discarded.
func (m *Manager) takeWarmResolution(ctx context.Context, id identity, profile core.QualityProfile, caps core.ClientCaps, prefer string) *warmEntry {
	m.mu.Lock()
	e := m.warm[id]
	if e == nil {
		m.mu.Unlock()
		return nil
	}
	if !m.now().Before(e.expires) {
		delete(m.warm, id)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	select {
	case <-e.done:
	case <-ctx.Done():
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.warm[id] != e {
		return nil
	}
	if prefer != "" && (e.res == nil || e.res.Release.ID != prefer) {
		return nil // keep it: a plain Prepare may still use it
	}
	if !warmAcceptable(e, profile, caps) {
		delete(m.warm, id)
		return nil
	}
	delete(m.warm, id)
	// Return an immutable snapshot: the background index fill may replace
	// e.probe after the ready signal.
	return &warmEntry{res: e.res, probe: e.probe}
}

// InvalidateTitle drops the warm (pre-resolved) entries for a title, from
// both the detail and speculative budgets, so the next Play resolves
// afresh — after the owner pins, unpins, blocks or unblocks a release, or
// forgets a title's search. A show or season key also drops its
// episodes' entries. A warm-up still in flight finishes into nothing. It
// returns how many entries were dropped.
func (m *Manager) InvalidateTitle(key core.RatingKey) int {
	if key == 0 {
		return 0
	}
	matches := func(k core.RatingKey) bool {
		switch {
		case k == key:
			return true
		case k.Kind() != core.KindEpisode:
			return false
		case key.Kind() == core.KindShow:
			return k.TMDB() == key.TMDB()
		case key.Kind() == core.KindSeason:
			return k.Parent() == key
		}
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id := range m.warm {
		if matches(id.key) {
			delete(m.warm, id)
			n++
		}
	}
	return n
}
