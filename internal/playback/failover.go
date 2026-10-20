package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// errSourceBroken marks a release whose articles went missing while it was
// being opened or probed.
var errSourceBroken = fmt.Errorf("playback: source broken: %w", core.ErrArticleMissing)

// errRequireRemux reports a completed failover that swapped the session to
// a healthy release whose bytes cannot continue the in-flight direct-play
// response: the response must end and the next decision/start is a remux.
var errRequireRemux = errors.New("playback: failover requires remux")

// openInitial finds and opens the first release of a session: the stored
// resume release when it was resolved for the same profile, else the
// resolver's answer, moving to the next candidate when a release fails.
func (m *Manager) openInitial(ctx context.Context, s *session, profile core.QualityProfile) (*generation, error) {
	var res *core.Resolution
	var cached *probeCache
	if warm := m.takeWarmResolution(ctx, s.ident, profile, s.caps, s.prefer); warm != nil {
		res, cached = warm.res, warm.probe
	}
	if res == nil {
		res = m.resumeResolution(ctx, s.ident.userID, s.ident.key, profile.Name, s.caps)
		if res != nil && s.prefer != "" && res.Release.ID != s.prefer {
			res = nil // a picked version wins over the remembered release
		}
	}
	var (
		tried      []string
		alternates []core.Release
		lastErr    error
	)
	fallback := func(size int64) core.MediaInfo {
		info := EstimateInfo(s.title, s.caps, profile)
		info.Size = size
		return info
	}
	for attempt := 0; attempt < m.opts.MaxAttempts; attempt++ {
		if res == nil {
			var err error
			res, err = m.nextResolution(ctx, s, tried, alternates)
			if err != nil {
				if lastErr != nil && errors.Is(err, core.ErrNotAvailable) {
					return nil, fmt.Errorf("%w (last failure: %v)", err, lastErr)
				}
				return nil, err
			}
		}
		g, err := m.openGeneration(ctx, s, res, fallback, cached)
		if err == nil {
			s.mu.Lock()
			s.tried = append(s.tried, tried...)
			s.mu.Unlock()
			return g, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		m.log.Warn("release failed to open", "release", res.Release.Name, "err", err)
		tried = append(tried, res.Release.ID)
		if len(res.Alternates) > 0 {
			alternates = res.Alternates
		}
		res = nil
	}
	return nil, fmt.Errorf("%w: %d releases failed (last: %v)", core.ErrNotAvailable, len(tried), lastErr)
}

// nextResolution returns the next release to try: an untried alternate when
// NZBs can be fetched directly, else a fresh Resolve excluding tried ones.
func (m *Manager) nextResolution(ctx context.Context, s *session, tried []string, alternates []core.Release) (*core.Resolution, error) {
	if m.deps.FetchNZB != nil {
		for i, alt := range alternates {
			if alt.ID == "" || slices.Contains(tried, alt.ID) {
				continue
			}
			nzb, err := m.deps.FetchNZB(ctx, alt)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				m.log.Warn("alternate nzb fetch failed", "release", alt.Name, "err", err)
				continue
			}
			return &core.Resolution{
				Title:      s.ident.key,
				Release:    alt,
				NZB:        nzb,
				Alternates: slices.Clone(alternates[i+1:]),
				ResolvedAt: m.now(),
			}, nil
		}
	}
	u := s.user
	res, err := m.deps.Resolver.Resolve(ctx, core.ResolveRequest{
		Title:   s.title,
		Client:  s.caps,
		User:    &u,
		Exclude: slices.Clone(tried),
		Prefer:  s.prefer,
	})
	if err != nil {
		return nil, err
	}
	if res == nil || res.Release.ID == "" || len(res.NZB) == 0 {
		return nil, fmt.Errorf("%w: resolver returned no usable release", core.ErrNotAvailable)
	}
	if slices.Contains(tried, res.Release.ID) {
		return nil, fmt.Errorf("%w: resolver returned an excluded release", core.ErrNotAvailable)
	}
	return res, nil
}

// openGeneration opens a release through the engine, exposes it on the
// loopback server and probes it. A release whose articles are missing is
// marked bad. When the probe fails for another reason, fallback supplies the
// media info.
func (m *Manager) openGeneration(ctx context.Context, s *session, res *core.Resolution, fallback func(size int64) core.MediaInfo, cached ...*probeCache) (*generation, error) {
	src, err := m.deps.Engine.Open(ctx, res.Release.ID, res.NZB)
	if err != nil {
		if errors.Is(err, core.ErrArticleMissing) {
			m.markBad(res.Release.ID, "articles missing on open")
		}
		return nil, fmt.Errorf("open %s: %w", res.Release.Name, err)
	}
	if src.Size() <= 0 {
		src.Close()
		return nil, fmt.Errorf("open %s: empty media file", res.Release.Name)
	}
	return m.probeGeneration(ctx, s, res, src, fallback, cached...)
}

func (m *Manager) probeGeneration(ctx context.Context, s *session, res *core.Resolution, src core.MediaSource, fallback func(int64) core.MediaInfo, cached ...*probeCache) (*generation, error) {
	g := &generation{sess: s, n: s.nextGen(), token: randomID(16), src: src, res: res, indexReady: make(chan struct{})}
	g.ctx, g.cancel = context.WithCancel(m.ctx)
	m.mu.Lock()
	m.tokens[s.id+"/"+g.token] = g
	m.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, m.opts.ProbeTimeout)
	var info *core.MediaInfo
	var perr error
	var facts *probeCache
	if len(cached) > 0 && cached[0] != nil && cached[0].releaseID == res.Release.ID && cached[0].info.Size == src.Size() {
		facts = cached[0]
		copy := facts.info
		info = &copy
	} else {
		info, perr = m.deps.Transcoder.Probe(pctx, m.urlFor(g))
	}
	cancel()
	var mi core.MediaInfo
	switch {
	case g.sourceFailure() != nil || g.articleMissing.Load():
		failure := g.sourceFailure()
		if failure == nil {
			failure = errSourceBroken
		}
		m.dropGeneration(g)
		if permanentSourceFailure(failure) {
			m.markBad(res.Release.ID, "invalid source while probing")
		}
		return nil, failure
	case perr != nil && ctx.Err() != nil:
		m.dropGeneration(g)
		return nil, ctx.Err()
	case perr != nil || info == nil:
		m.log.Warn("probe failed; using estimated media info", "release", res.Release.Name, "err", perr)
		mi = fallback(src.Size())
		mi.Estimated = true
	default:
		mi = *info
	}
	if mi.Size <= 0 {
		mi.Size = src.Size()
	}
	if mi.Container == "" {
		mi.Container = containerFromName(src.Name())
	}
	// A cached warm probe was already augmented when it was made; running
	// AugmentMedia again would put its cost back on the Play path.
	if facts == nil && m.deps.AugmentMedia != nil {
		m.deps.AugmentMedia(ctx, s.title, &s.user, res, src, &mi)
	}
	var points []IndexPoint
	if facts != nil {
		points = facts.points
		if len(points) == 0 {
			points = indexPointsOf(facts.info.Keyframes)
		}
	} else if len(mi.Keyframes) > 0 {
		points = indexPointsOf(mi.Keyframes)
	}
	mi.Keyframes = keyframesOf(points)
	needIndex := len(points) == 0 && !mi.Estimated && mi.Duration > 0
	// The loopback server may already hold g; publish under its lock.
	g.mu.Lock()
	g.info = mi
	g.tmap = TimeMap{Size: src.Size(), Duration: mi.Duration, Points: points}
	g.mu.Unlock()
	if needIndex {
		// Play never waits for the container index: publish now and fill
		// TimeMap.Points / MediaInfo.Keyframes in the background.
		m.startIndexFill(g)
	} else {
		g.markIndexReady()
	}
	return g, nil
}

// startIndexFill reads the container keyframe index off the critical path
// and installs it on the generation (and the session's virtual map when
// this generation defines it) once read.
func (m *Manager) startIndexFill(g *generation) {
	if !g.acquire() {
		g.markIndexReady()
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		g.release()
		g.markIndexReady()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer g.release()
		defer g.markIndexReady()
		ctx, cancel := context.WithTimeout(g.ctx, m.opts.IndexTimeout)
		defer cancel()
		points := containerIndex(ctx, g.src, g.src.Size())
		if len(points) == 0 {
			return
		}
		g.setIndex(points)
		s := g.sess
		if m.resume != nil {
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			if err := m.resume.saveIndex(ctx, s.ident.userID, s.ident.key, g.releaseID(), keyframesOf(points)); err != nil {
				m.log.Warn("save resume keyframe index failed", "key", s.ident.key, "err", err)
			}
			cancel()
		}
		s.mu.Lock()
		if s.virtualGen == g {
			s.virtual.Points = points
		}
		s.mu.Unlock()
	}()
}

// dropGeneration discards a generation that never became current.
func (m *Manager) dropGeneration(g *generation) {
	m.mu.Lock()
	delete(m.tokens, g.sess.id+"/"+g.token)
	m.mu.Unlock()
	g.retire()
}

func (m *Manager) markBad(releaseID, reason string) {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	if err := m.deps.Resolver.MarkBad(ctx, releaseID, reason); err != nil {
		m.log.Warn("mark release bad failed", "release", releaseID, "err", err)
	}
}

// Failover swaps the session to the next candidate release, keeping the
// timestamp at. The current release is marked bad. When an HLS session
// reads from this playback session it is restarted on the new input at the
// same position. Returns the updated session.
func (m *Manager) Failover(ctx context.Context, sessionID string, at time.Duration) (*core.PlaySession, error) {
	s := m.lookup(sessionID)
	if s == nil {
		return nil, fmt.Errorf("playback: session %s: %w", sessionID, core.ErrNotFound)
	}
	if _, err := m.failover(ctx, s, s.current(), at, "failover requested"); err != nil && !errors.Is(err, errRequireRemux) {
		return nil, err
	}
	return s.snapshot(m.now()), nil
}

// failoverOpts distinguishes a broken-source failover (markBad) from a
// speed-driven down-failover (down: pick a lower-bitrate candidate, never
// mark the slow release bad).
type failoverOpts struct {
	reason  string
	markBad bool
	down    bool
}

func (m *Manager) failover(ctx context.Context, s *session, observed *generation, at time.Duration, reason string) (*generation, error) {
	return m.failoverWith(ctx, s, observed, at, failoverOpts{reason: reason, markBad: true})
}

// failoverWith replaces generation observed. Concurrent callers that
// observed the same failing generation share one swap: the first does the
// work and the others return the generation it installed.
func (m *Manager) failoverWith(ctx context.Context, s *session, observed *generation, at time.Duration, fo failoverOpts) (*generation, error) {
	if err := lock(ctx, s.failSem); err != nil {
		return nil, err
	}
	defer unlock(s.failSem)

	s.mu.Lock()
	cur, closed, failovers := s.cur, s.closed, s.failovers
	direct := s.decision == core.DecisionDirectPlay && !spliceable(s.container, s.name)
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("playback: session %s ended: %w", s.id, core.ErrNotFound)
	}
	if cur != observed {
		return cur, nil // someone else already swapped
	}
	if failovers >= m.opts.MaxFailovers {
		return nil, fmt.Errorf("%w: session %s gave up after %d failovers", core.ErrNotAvailable, s.id, failovers)
	}
	log := m.log.With("session", s.id, "key", s.ident.key, "at", at)
	log.Warn("failing over", "release", cur.res.Release.Name, "reason", fo.reason, "down", fo.down)
	// A provider outage is not evidence that the release itself is invalid.
	if failure := cur.sourceFailure(); permanentSourceFailure(failure) {
		m.markBad(cur.releaseID(), fo.reason)
	}

	s.mu.Lock()
	s.tried = append(s.tried, cur.releaseID())
	tried := slices.Clone(s.tried)
	alternates := s.alternates
	s.mu.Unlock()

	oldInfo := cur.infoSnapshot()
	if direct {
		// Identical-size reposts can continue the client's byte stream;
		// try them before opening candidates in rank order.
		alternates = orderExactSizeFirst(alternates, cur.res.Release)
	} else if fo.down {
		alternates = orderLowerBitrateFirst(alternates, cur.res.Release)
	}
	fallback := func(size int64) core.MediaInfo {
		info := oldInfo
		info.Size = size
		info.Estimated = true
		info.Keyframes = nil
		return info
	}
	curBps := mediaBps(oldInfo, cur.src.Size())
	var (
		g           *generation
		fallbackGen *generation // healthy but not spliceable into direct play
		remuxSwitch bool
		lastErr     error
	)
	for attempt := 0; attempt < m.opts.MaxAttempts; attempt++ {
		res, err := m.nextResolution(ctx, s, tried, alternates)
		if err != nil {
			lastErr = err
			break
		}
		g, err = m.openGeneration(ctx, s, res, fallback)
		// Byte-range clients retain the original container headers and
		// offsets. Only an exact-size repost may replace a non-spliceable
		// direct source in place; a healthy mismatched candidate is kept
		// as a remux-switch fallback instead of being spliced in.
		if err == nil && direct && g.src.Size() != cur.src.Size() {
			if fallbackGen == nil {
				fallbackGen = g
			} else {
				m.dropGeneration(g)
			}
			g = nil
			err = fmt.Errorf("incompatible direct-play replacement size: %w", core.ErrNotAvailable)
		}
		if err == nil && fo.down {
			if bps := mediaBps(g.infoSnapshot(), g.src.Size()); curBps > 0 && bps > 0 && bps >= curBps {
				m.dropGeneration(g)
				g = nil
				err = fmt.Errorf("down-failover candidate is not lower bitrate: %w", core.ErrNotAvailable)
			}
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			if fallbackGen != nil {
				m.dropGeneration(fallbackGen)
			}
			return nil, ctx.Err()
		}
		lastErr = err
		log.Warn("failover candidate failed", "release", res.Release.Name, "err", err)
		tried = append(tried, res.Release.ID)
		if len(res.Alternates) > 0 {
			alternates = res.Alternates
		}
		if direct {
			alternates = orderExactSizeFirst(alternates, cur.res.Release)
		} else if fo.down {
			alternates = orderLowerBitrateFirst(alternates, cur.res.Release)
		}
	}
	if g == nil && fallbackGen != nil && fo.down {
		// A down-failover only helps with a lower-bitrate fallback.
		if bps := mediaBps(fallbackGen.infoSnapshot(), fallbackGen.src.Size()); curBps > 0 && bps > 0 && bps >= curBps {
			m.dropGeneration(fallbackGen)
			fallbackGen = nil
		}
	}
	if g == nil && fallbackGen != nil {
		// No exact-size repost: do not splice. Swap anyway and restart
		// the client as a zero-encode remux at the mapped time.
		g, fallbackGen, remuxSwitch = fallbackGen, nil, true
		log.Warn("no exact-size direct-play replacement; switching to remux", "release", g.res.Release.Name)
	}
	if fallbackGen != nil {
		m.dropGeneration(fallbackGen)
	}
	if g == nil {
		log.Error("failover found no working release", "err", lastErr)
		if lastErr == nil {
			lastErr = core.ErrNotAvailable
		}
		m.emitEvent("error", s, lastErr)
		return nil, fmt.Errorf("playback: failover: %w", lastErr)
	}

	// An HLS session restarts ffmpeg on fresh headers; anything else that
	// swaps to a different-size file must be remuxed. Mark that in the same
	// critical section as the swap, so no request sees the new release
	// without the remux requirement.
	hls, hasHLS := m.deps.HLSLookup(s.id)
	hasHLS = hasHLS && hls != nil
	if hasHLS {
		remuxSwitch = false
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		m.dropGeneration(g)
		return nil, fmt.Errorf("playback: session %s ended: %w", s.id, core.ErrNotFound)
	}
	s.cur = g
	if remuxSwitch {
		s.requiresRemux = true
	}
	s.failovers++
	if fo.down {
		s.downFailovers++
	}
	s.tried = tried
	if len(g.res.Alternates) > 0 {
		s.alternates = g.res.Alternates
	} else {
		s.alternates = withoutTried(alternates, append(tried, g.releaseID()))
	}
	s.mu.Unlock()

	m.mu.Lock()
	delete(m.tokens, s.id+"/"+cur.token)
	m.mu.Unlock()
	cur.retire()
	m.pinLive(g.releaseID())
	m.unpinLive(cur.releaseID())
	m.saveResume(ctx, s, g)
	s.resetSpeed(m.now(), m.opts.SpeedGrace)

	newInfo := g.infoSnapshot()
	if hasHLS {
		target := m.alignForFailover(ctx, s, cur, g, at)
		if err := hls.SwapInput(ctx, m.urlFor(g), newInfo, target); err != nil {
			log.Error("hls input swap failed", "err", err)
			return g, fmt.Errorf("playback: swap hls input: %w", err)
		}
	}
	if fo.down {
		m.emitEvent("downfailover", s, nil)
	} else {
		m.emitEvent("failover", s, nil)
	}
	log.Info("failed over", "release", g.res.Release.Name, "size", g.src.Size(), "duration", newInfo.Duration, "remuxSwitch", remuxSwitch)
	if remuxSwitch {
		return g, errRequireRemux
	}
	return g, nil
}

// alignForFailover maps a failover timestamp between releases: through
// the optional dialogue TimelineAligner (300ms budget) when wired, else
// duration-based AlignTime (PAL rule).
func (m *Manager) alignForFailover(ctx context.Context, s *session, from, to *generation, at time.Duration) time.Duration {
	fromDur := from.infoSnapshot().Duration
	toDur := to.infoSnapshot().Duration
	if m.deps.Aligner != nil && from.res != nil && to.res != nil {
		req := core.AlignRequest{
			Key:          s.ident.key,
			From:         from.res.Release,
			To:           to.res.Release,
			FromDuration: fromDur,
			ToDuration:   toDur,
		}
		type result struct {
			mapped time.Duration
			ok     bool
		}
		ch := make(chan result, 1)
		actx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		go func() {
			mapped, ok := m.deps.Aligner.AlignTime(actx, req, at)
			ch <- result{mapped, ok}
		}()
		select {
		case r := <-ch:
			cancel()
			if r.ok {
				if r.mapped < 0 {
					return 0
				}
				if toDur > 0 && r.mapped > toDur {
					return toDur
				}
				return r.mapped
			}
		case <-actx.Done():
			cancel()
			m.log.Debug("timeline aligner timed out; using duration alignment", "session", s.id)
		}
	}
	return AlignTime(at, fromDur, toDur)
}

// orderExactSizeFirst stably moves candidates whose known size matches
// the failing release (identical reposts — same size, then same name,
// then same group) ahead of rank order for direct-play failover.
func orderExactSizeFirst(alts []core.Release, cur core.Release) []core.Release {
	if cur.Size <= 0 || len(alts) < 2 {
		return alts
	}
	out := slices.Clone(alts)
	rank := func(r core.Release) int {
		switch {
		case r.Size == cur.Size && r.Name == cur.Name:
			return 0
		case r.Size == cur.Size && r.Info.Group != "" && r.Info.Group == cur.Info.Group:
			return 1
		case r.Size == cur.Size:
			return 2
		}
		return 3
	}
	sortStableByRank(out, rank)
	return out
}

// orderLowerBitrateFirst stably moves candidates of known smaller size
// (lower bitrate at equal duration) ahead for down-failover, keeping the
// resolver's ranking within each group.
func orderLowerBitrateFirst(alts []core.Release, cur core.Release) []core.Release {
	if cur.Size <= 0 || len(alts) < 2 {
		return alts
	}
	out := slices.Clone(alts)
	rank := func(r core.Release) int {
		if r.Size > 0 && r.Size < cur.Size {
			return 0
		}
		return 1
	}
	sortStableByRank(out, rank)
	return out
}

func sortStableByRank(rs []core.Release, rank func(core.Release) int) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rank(rs[j]) < rank(rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func withoutTried(rs []core.Release, tried []string) []core.Release {
	var out []core.Release
	for _, r := range rs {
		if !slices.Contains(tried, r.ID) {
			out = append(out, r)
		}
	}
	return out
}

func containerFromName(name string) string {
	switch ct := contentTypeFor("", name); ct {
	case "video/x-matroska":
		return "mkv"
	case "video/mp4":
		return "mp4"
	case "video/mp2t":
		return "mpegts"
	case "video/x-msvideo":
		return "avi"
	case "video/webm":
		return "webm"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Resume records

// resumeResolution rebuilds a Resolution from the stored resume record when
// it was resolved for the same quality profile.
func (m *Manager) resumeResolution(ctx context.Context, userID int64, key core.RatingKey, profile string, caps core.ClientCaps) *core.Resolution {
	if m.resume == nil {
		return nil
	}
	rec, err := m.resume.get(ctx, userID, key)
	if err != nil {
		m.log.Warn("resume lookup failed", "err", err)
		return nil
	}
	if rec == nil || rec.profile != profile || !m.resumeSourceAllowed(rec.release) || !sourceFitsBitrate(rec.size, rec.duration, 0, caps.MaxBitrateKbps) {
		return nil
	}
	nzb, err := m.resume.readNZB(rec.nzbHash)
	if err != nil {
		m.log.Warn("stored nzb unreadable; resolving afresh", "release", rec.releaseID, "err", err)
		return nil
	}
	m.log.Debug("reusing stored release", "key", key, "release", rec.release.Name)
	return &core.Resolution{Title: key, Release: rec.release, NZB: nzb, ResolvedAt: rec.updatedAt}
}

// saveResume remembers the release now serving a session.
func (m *Manager) saveResume(ctx context.Context, s *session, g *generation) {
	if m.resume == nil || g.res == nil || len(g.res.NZB) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	rec := resumeRecord{
		userID:    s.ident.userID,
		key:       s.ident.key,
		release:   g.res.Release,
		profile:   s.profile,
		size:      g.src.Size(),
		duration:  g.infoSnapshot().Duration,
		updatedAt: m.now(),
	}
	if err := m.resume.save(ctx, rec, g.res.NZB); err != nil {
		m.log.Warn("save resume record failed", "err", err)
		return
	}
	if points := g.infoSnapshot().Keyframes; len(points) > 0 {
		if err := m.resume.saveIndex(ctx, s.ident.userID, s.ident.key, g.releaseID(), points); err != nil {
			m.log.Warn("save resume keyframe index failed", "key", s.ident.key, "err", err)
		}
	}
}

// ResumePoint returns the release that last served (or was prefetched for)
// a user's title, with its NZB; nil when there is none.
func (m *Manager) ResumePoint(ctx context.Context, userID int64, key core.RatingKey) (*ResumePoint, error) {
	if m.resume == nil {
		return nil, nil
	}
	rec, err := m.resume.get(ctx, userID, key)
	if err != nil || rec == nil {
		return nil, err
	}
	nzb, err := m.resume.readNZB(rec.nzbHash)
	if err != nil {
		return nil, err
	}
	point := rec.point(nzb)
	point.Keyframes, err = m.resume.loadIndex(ctx, userID, key, rec.releaseID)
	if err != nil {
		return nil, err
	}
	return point, nil
}

// Remember stores a resolution for a title the user has not played yet
// (a prefetched next episode) so the eventual Prepare opens the same,
// already warm release. An existing record for the same profile is kept.
func (m *Manager) Remember(ctx context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps, res *core.Resolution) error {
	if m.resume == nil || user == nil || res == nil || len(res.NZB) == 0 {
		return nil
	}
	profile := m.deps.SelectProfile(caps, user).Name
	rec, err := m.resume.get(ctx, user.ID, key)
	if err != nil {
		return err
	}
	if rec != nil && rec.profile == profile && m.resumeSourceAllowed(rec.release) && sourceFitsBitrate(rec.size, rec.duration, 0, caps.MaxBitrateKbps) {
		return nil
	}
	return m.resume.save(ctx, resumeRecord{
		userID:    user.ID,
		key:       key,
		release:   res.Release,
		profile:   profile,
		updatedAt: m.now(),
	}, res.NZB)
}

// ResumeFor is ResumePoint restricted to a record resolved for the quality
// profile this client and user would get, i.e. the release a Prepare from
// that client would open.
func (m *Manager) ResumeFor(ctx context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps) (*ResumePoint, error) {
	if user == nil {
		return nil, nil
	}
	rp, err := m.ResumePoint(ctx, user.ID, key)
	if err != nil || rp == nil {
		return nil, err
	}
	if rp.Profile != m.deps.SelectProfile(caps, user).Name || !m.resumeSourceAllowed(rp.Release) || !sourceFitsBitrate(rp.Size, rp.Duration, 0, caps.MaxBitrateKbps) {
		return nil, nil
	}
	return rp, nil
}

func permanentSourceFailure(err error) bool {
	return errors.Is(err, core.ErrArticleMissing) || errors.Is(err, core.ErrArticleCorrupt) || errors.Is(err, core.ErrReleaseRejected)
}
func recoverableSourceFailure(err error) bool {
	if permanentSourceFailure(err) || errors.Is(err, core.ErrSourceUnavailable) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrNoProgress) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded)
}
