package playback

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// errSourceBroken marks a release whose articles went missing while it was
// being opened or probed.
var errSourceBroken = fmt.Errorf("playback: source broken: %w", core.ErrArticleMissing)

// openInitial finds and opens the first release of a session: the stored
// resume release when it was resolved for the same profile, else the
// resolver's answer, moving to the next candidate when a release fails.
func (m *Manager) openInitial(ctx context.Context, s *session, profile core.QualityProfile) (*generation, error) {
	res := m.resumeResolution(ctx, s.ident.userID, s.ident.key, profile.Name)
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
		g, err := m.openGeneration(ctx, s, res, fallback)
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
func (m *Manager) openGeneration(ctx context.Context, s *session, res *core.Resolution, fallback func(size int64) core.MediaInfo) (*generation, error) {
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
	g := &generation{sess: s, n: s.nextGen(), token: randomID(16), src: src, res: res}
	m.mu.Lock()
	m.tokens[s.id+"/"+g.token] = g
	m.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, m.opts.ProbeTimeout)
	info, perr := m.deps.Transcoder.Probe(pctx, m.urlFor(g))
	cancel()
	var mi core.MediaInfo
	switch {
	case g.articleMissing.Load():
		m.dropGeneration(g)
		m.markBad(res.Release.ID, "articles missing while probing")
		return nil, errSourceBroken
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
	// The loopback server may already hold g; publish under its lock.
	g.mu.Lock()
	g.info = mi
	g.tmap = TimeMap{Size: src.Size(), Duration: mi.Duration}
	g.mu.Unlock()
	return g, nil
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
	if _, err := m.failover(ctx, s, s.current(), at, "failover requested"); err != nil {
		return nil, err
	}
	return s.snapshot(m.now()), nil
}

// failover replaces generation observed. Concurrent callers that observed
// the same failing generation share one swap: the first does the work and
// the others return the generation it installed.
func (m *Manager) failover(ctx context.Context, s *session, observed *generation, at time.Duration, reason string) (*generation, error) {
	if err := lock(ctx, s.failSem); err != nil {
		return nil, err
	}
	defer unlock(s.failSem)

	s.mu.Lock()
	cur, closed, failovers := s.cur, s.closed, s.failovers
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
	log.Warn("failing over", "release", cur.res.Release.Name, "reason", reason)
	m.markBad(cur.releaseID(), reason)

	s.mu.Lock()
	s.tried = append(s.tried, cur.releaseID())
	tried := slices.Clone(s.tried)
	alternates := s.alternates
	s.mu.Unlock()

	oldInfo := cur.info
	fallback := func(size int64) core.MediaInfo {
		info := oldInfo
		info.Size = size
		info.Estimated = true
		return info
	}
	var (
		g       *generation
		lastErr error
	)
	for attempt := 0; attempt < m.opts.MaxAttempts; attempt++ {
		res, err := m.nextResolution(ctx, s, tried, alternates)
		if err != nil {
			lastErr = err
			break
		}
		g, err = m.openGeneration(ctx, s, res, fallback)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		log.Warn("failover candidate failed", "release", res.Release.Name, "err", err)
		tried = append(tried, res.Release.ID)
		if len(res.Alternates) > 0 {
			alternates = res.Alternates
		}
	}
	if g == nil {
		log.Error("failover found no working release", "err", lastErr)
		if lastErr == nil {
			lastErr = core.ErrNotAvailable
		}
		return nil, fmt.Errorf("playback: failover: %w", lastErr)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		m.dropGeneration(g)
		return nil, fmt.Errorf("playback: session %s ended: %w", s.id, core.ErrNotFound)
	}
	s.cur = g
	s.failovers++
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

	if hls, ok := m.deps.HLSLookup(s.id); ok && hls != nil {
		target := AlignTime(at, oldInfo.Duration, g.info.Duration)
		if err := hls.SwapInput(ctx, m.urlFor(g), g.info, target); err != nil {
			log.Error("hls input swap failed", "err", err)
			return g, fmt.Errorf("playback: swap hls input: %w", err)
		}
	}
	log.Info("failed over", "release", g.res.Release.Name, "size", g.tmap.Size, "duration", g.info.Duration)
	return g, nil
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
func (m *Manager) resumeResolution(ctx context.Context, userID int64, key core.RatingKey, profile string) *core.Resolution {
	if m.resume == nil {
		return nil
	}
	rec, err := m.resume.get(ctx, userID, key)
	if err != nil {
		m.log.Warn("resume lookup failed", "err", err)
		return nil
	}
	if rec == nil || rec.profile != profile {
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
	if m.resume == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	rec := resumeRecord{
		userID:    s.ident.userID,
		key:       s.ident.key,
		release:   g.res.Release,
		profile:   s.profile,
		size:      g.tmap.Size,
		duration:  g.info.Duration,
		updatedAt: m.now(),
	}
	if err := m.resume.save(ctx, rec, g.res.NZB); err != nil {
		m.log.Warn("save resume record failed", "err", err)
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
	return rec.point(nzb), nil
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
	if rec != nil && rec.profile == profile {
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
	if rp.Profile != m.deps.SelectProfile(caps, user).Name {
		return nil, nil
	}
	return rp, nil
}
