package playback

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const playableReserve = 40 * time.Second

type reserveState struct {
	mu         sync.Mutex
	until      time.Duration
	generation *generation
	running    bool
	cancel     context.CancelFunc
}
type standbyState struct {
	mu       sync.Mutex
	started  bool
	ready    *generation
	consumed chan struct{}
	elapsed  time.Duration
}

// Source-cache reserve, independent of the player's own buffering policy. All
// reads use Engine.Prefetch's background priority and are bounded on disk; no
// 40-second video-sized allocation is held in RAM. No startup waits on this.
func (m *Manager) maintainReserve(s *session, position time.Duration, state string) {
	if state != "playing" {
		return
	}
	cfg := m.config().Cache
	if cfg.Mode == "off" {
		return
	}
	g := s.current()
	if g == nil {
		return
	}
	info := g.infoSnapshot()
	if info.Duration <= 0 {
		return
	}
	s.speedMu.Lock()
	rate, known := s.speed.rate(m.now())
	s.speedMu.Unlock()
	if known && rate < mediaBps(info, g.src.Size())*1.25 {
		return
	}
	r := &s.reserve
	r.mu.Lock()
	if r.generation == g && position >= r.until-playableReserve && position+30*time.Second <= r.until {
		r.mu.Unlock()
		return
	}
	if r.running && r.generation == g && position >= r.until-playableReserve && position < r.until {
		r.mu.Unlock()
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	ctx, cancel := context.WithTimeout(g.ctx, 45*time.Second)
	r.generation = g
	r.until = position + playableReserve
	r.running = true
	r.cancel = cancel
	until := r.until
	r.mu.Unlock()
	if !g.acquire() {
		cancel()
		r.mu.Lock()
		if r.generation == g && r.until == until {
			r.running = false
			r.until = position
		}
		r.mu.Unlock()
		return
	}
	m.mu.Lock()
	if m.closed || m.reserveActive >= 2 {
		m.mu.Unlock()
		cancel()
		g.release()
		r.mu.Lock()
		if r.generation == g && r.until == until {
			r.running = false
			r.until = position
		}
		r.mu.Unlock()
		return
	}
	m.reserveActive++
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); m.reserveActive--; m.mu.Unlock() }()
		defer cancel()
		defer g.release()
		tm := g.timeMapSnapshot()
		lo := tm.KeyframeOffsetAt(position)
		hi := tm.OffsetAt(min(info.Duration, until+4*time.Second))
		// Without an index these are conservative byte hints, not claims that
		// every decoded segment is ready. RecoveryBuffer supplies that evidence.
		if len(tm.Points) == 0 {
			lo = tm.OffsetAt(max(0, position-6*time.Second))
		}
		n := min(max(0, hi-lo), int64(256<<20))
		err := m.deps.Engine.Prefetch(ctx, g.releaseID(), g.res.NZB, lo, n)
		r.mu.Lock()
		if r.generation == g && r.until == until {
			r.running = false
			if err != nil {
				r.until = position
			}
		}
		r.mu.Unlock()
	}()
}

// Prepare one replacement while the current encoder and delivered buffer keep
// playing. Retain the opened/probed generation, not just its NZB. Ownership
// transfers once to failover; otherwise the parent/session cancellation closes
// it and revokes its loopback grant. Speculation never changes watch state.
func (m *Manager) prepareSpeedAlternate(g *generation) {
	if m.config().Cache.DisableSpeculation {
		return
	}
	d := &g.standby
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.started = true
	d.consumed = make(chan struct{})
	d.mu.Unlock()
	m.mu.Lock()
	if m.closed || m.standbyActive >= 2 {
		m.mu.Unlock()
		d.mu.Lock()
		d.started = false
		d.mu.Unlock()
		return
	}
	m.standbyActive++
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); m.standbyActive--; m.mu.Unlock() }()
		s := g.sess
		ctx, cancel := context.WithTimeout(g.ctx, 20*time.Second)
		stop := context.AfterFunc(s.stopCtx, cancel)
		defer stop()
		defer cancel()
		s.mu.Lock()
		tried := append(slices.Clone(s.tried), g.releaseID())
		alts := slices.Clone(s.alternates)
		s.mu.Unlock()
		alts = orderLowerBitrateFirst(alts, g.res.Release)
		started := time.Now()
		var prepared *generation
		for attempt := 0; attempt < min(m.opts.MaxAttempts, 3) && ctx.Err() == nil; attempt++ {
			res, err := m.nextResolution(ctx, s, tried, alts)
			if err != nil {
				break
			}
			tried = append(tried, res.Release.ID)
			next, err := m.openGeneration(ctx, s, res, func(size int64) core.MediaInfo { return core.MediaInfo{Size: size, Estimated: true} })
			if err != nil {
				continue
			}
			bps := mediaBps(next.infoSnapshot(), next.src.Size())
			if bps <= 0 || bps >= mediaBps(g.infoSnapshot(), g.src.Size()) || m.preflightSwap(s, next) != nil {
				m.dropGeneration(next)
				continue
			}
			prepared = next
			break
		}
		if prepared == nil {
			return
		}
		// Resolve/open/probe and a bounded upcoming window are ready before use.
		position, _, _ := m.repairHorizon(g, core.RepairEvent{Offset: -1})
		// Warm where an encoder takeover will begin, not stale source-read
		// offsets. Keep the opening header cached independently.
		if hls, ok := m.deps.HLSLookup(s.id); ok {
			if source, ok := hls.(core.RecoveryBufferSource); ok {
				b := source.RecoveryBuffer()
				if b.ReadyUntil > position && b.ReadyUntil < prepared.infoSnapshot().Duration {
					position = m.alignForFailover(ctx, s, g, prepared, b.ReadyUntil)
				}
			}
		}
		tm := prepared.timeMapSnapshot()
		lo := tm.KeyframeOffsetAt(max(0, position-4*time.Second))
		if len(tm.Points) == 0 {
			lo = tm.OffsetAt(max(0, position-8*time.Second))
		}
		_ = m.deps.Engine.Prefetch(ctx, prepared.releaseID(), prepared.res.NZB, 0, 256<<10)
		_ = m.deps.Engine.Prefetch(ctx, prepared.releaseID(), prepared.res.NZB, lo, min(int64(16<<20), max(0, tm.Size-lo)))
		if ctx.Err() != nil || s.current() != g {
			m.dropGeneration(prepared)
			return
		}
		d.mu.Lock()
		d.ready = prepared
		d.elapsed = time.Since(started)
		d.mu.Unlock()
		m.log.Info("playback alternate prepared", "session", s.id, "release", prepared.releaseID(), "took", time.Since(started))
		select {
		case <-d.consumed:
			return
		case <-g.ctx.Done():
		case <-s.stopCtx.Done():
		}
		d.mu.Lock()
		abandoned := d.ready
		d.ready = nil
		d.mu.Unlock()
		if abandoned != nil {
			m.dropGeneration(abandoned)
		}
	}()
}

func (g *generation) takeSpeedAlternate(tried []string) *generation {
	d := &g.standby
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ready == nil || slices.Contains(tried, d.ready.releaseID()) {
		return nil
	}
	next := d.ready
	d.ready = nil
	close(d.consumed)
	return next
}

// Buffer runway under a sustained deficit. Unknown buffer evidence leaves the
// legacy hysteresis intact; zero known runway calls for immediate recovery.
func bufferRunway(buffer time.Duration, ratio float64) time.Duration {
	if ratio >= 1 {
		return time.Hour
	}
	if ratio < 0 {
		ratio = 0
	}
	return time.Duration(float64(max(0, buffer)) / (1 - ratio))
}

// Track only time actually blocked inside live source reads. Encoder pauses,
// HTTP backpressure, probes and speculative reads are not network outages.
type pendingSourceReads struct {
	mu    sync.Mutex
	waits map[*time.Time]struct{}
}

func (p *pendingSourceReads) begin(now time.Time) func() {
	p.mu.Lock()
	if p.waits == nil {
		p.waits = make(map[*time.Time]struct{})
	}
	p.waits[&now] = struct{}{}
	p.mu.Unlock()
	return func() { p.mu.Lock(); delete(p.waits, &now); p.mu.Unlock() }
}
func (p *pendingSourceReads) longest(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	var elapsed time.Duration
	for start := range p.waits {
		elapsed = max(elapsed, now.Sub(*start))
	}
	return elapsed
}
