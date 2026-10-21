package playback

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// damageState belongs to a generation, never to a release globally. Minor
// artifacts keep playing while one alternate is prepared. Burst hysteresis uses
// byte progress rather than wall time so parallel range requests and pauses
// cannot reset the budget. A lifetime budget also bounds intermittent damage.
type damageState struct {
	once                                        sync.Once
	mu                                          sync.Mutex
	preparing                                   bool
	prepared                                    *core.Resolution
	burstStart, lastEnd, burstBytes, totalBytes int64
	burstCount, totalCount                      int
}

func (m *Manager) installDamageHandler(g *generation) {
	if !g.sess.isRegistered() {
		return
	}
	g.damage.once.Do(func() {
		if src, ok := g.src.(interface{ SetDamageHandler(func(int64, int64) bool) }); ok {
			src.SetDamageHandler(func(offset, length int64) bool { return m.noteDamage(g, offset, length) })
		}
	})
}

func (m *Manager) noteDamage(g *generation, offset, length int64) bool {
	s := g.sess
	if s.isClosed() || s.current() != g {
		return false
	}
	m.prepareDamageAlternate(g)
	// A zero length is an early notification, before payload safety is proved.
	if length <= 0 {
		return false
	}
	d := &g.damage
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.burstCount == 0 || offset >= d.lastEnd+(32<<20) {
		d.burstStart = offset
		d.burstBytes = 0
		d.burstCount = 0
	}
	d.burstCount++
	d.totalCount++
	d.burstBytes += length
	d.totalBytes += length
	d.lastEnd = max(d.lastEnd, offset+length)
	// At most two isolated gaps per burst, 1 MiB each, <=12.5% of the
	// damaged region (with an 8 MiB startup allowance), 8 MiB per source.
	// Crossing any threshold returns the original source error to the existing
	// direct/remux-aware failover path; this callback never swaps a source.
	window := max(int64(8<<20), d.lastEnd-d.burstStart)
	return length <= 1<<20 && d.burstCount <= 2 && d.burstBytes <= 2<<20 &&
		d.burstBytes <= window/8 && d.totalBytes <= 8<<20 && d.totalCount <= 16
}

func (m *Manager) prepareDamageAlternate(g *generation) {
	d := &g.damage
	d.mu.Lock()
	if d.preparing {
		d.mu.Unlock()
		return
	}
	d.preparing = true // one bounded preparation per generation, including failures
	d.mu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(g.ctx, min(m.opts.FailoverTimeout, 20*time.Second))
		defer cancel()
		s := g.sess
		if s.stopCtx != nil {
			stop := context.AfterFunc(s.stopCtx, cancel)
			defer stop()
		}
		s.mu.Lock()
		tried := append(slices.Clone(s.tried), g.releaseID())
		alternates := slices.Clone(s.alternates)
		direct := s.decision == core.DecisionDirectPlay && !spliceable(s.container, s.name)
		s.mu.Unlock()
		if direct {
			alternates = orderExactSizeFirst(alternates, g.res.Release)
		}
		res, err := m.nextResolution(ctx, s, tried, alternates)
		if err != nil || ctx.Err() != nil {
			return
		}
		// Keep the original source, history, pins, and session count untouched.
		// Failover can consume the NZB while its initial bytes are still warming.
		d.mu.Lock()
		d.prepared = res
		d.mu.Unlock()
		_ = m.deps.Engine.Prefetch(ctx, res.Release.ID, res.NZB, 0, 256<<10)
	}()
}

func (g *generation) takeDamageResolution(tried []string) *core.Resolution {
	g.damage.mu.Lock()
	defer g.damage.mu.Unlock()
	res := g.damage.prepared
	g.damage.prepared = nil
	if res == nil || slices.Contains(tried, res.Release.ID) {
		return nil
	}
	return res
}
