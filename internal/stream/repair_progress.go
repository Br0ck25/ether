package stream

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/par2"
)

type repairObserver struct {
	mu      sync.Mutex
	handler func(core.RepairEvent) core.RepairWindow
}

func (s *source) SetRepairHandler(f func(core.RepairEvent) core.RepairWindow) {
	s.recovery.mu.Lock()
	s.recovery.handler = f
	s.recovery.mu.Unlock()
}

func (s *source) reportRepair(pf *postedFile, slot int, event core.RepairEvent) core.RepairWindow {
	s.recovery.mu.Lock()
	f := s.recovery.handler
	s.recovery.mu.Unlock()
	if f == nil {
		return core.RepairWindow{}
	}
	begin, end, exact := pf.rangeOf(slot)
	event.Key = pf.segBase + slot
	event.Approximate = !exact
	event.Offset = -1
	if s.plan.encrypted == nil {
		for _, x := range s.plan.layout().ext {
			if s.plan.vols[x.vol] == pf && end > x.off && begin < x.off+x.size {
				event.Offset = x.inner + max(int64(0), begin-x.off)
				break
			}
		}
	}
	w := f(event)
	if w.ReadAheadBytes > 0 {
		s.mu.Lock()
		limit := min(s.e.opts.ReadAheadMax, s.e.opts.HotBufferBytes/2)
		for _, c := range s.cursors {
			c.window = max(c.window, min(w.ReadAheadBytes, limit))
		}
		s.mu.Unlock()
		s.kickReadAhead()
	}
	return w
}

// Rate is measured across completed transfers over wall time, rather than
// summing per-socket speeds. Divide by viewers before estimating a repair.
type repairRate struct {
	mu    sync.Mutex
	at    time.Time
	bytes int64
	rate  float64
}

func (r *repairRate) add(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.at.IsZero() {
		r.at = now
	}
	r.bytes += n
	if elapsed := now.Sub(r.at).Seconds(); elapsed >= .25 {
		v := float64(r.bytes) / elapsed
		if r.rate == 0 || v < r.rate {
			r.rate = v
		} else {
			r.rate = .8*r.rate + .2*v
		}
		r.at, r.bytes = now, 0
	}
}
func (e *Engine) repairETA(n int64) time.Duration {
	e.repairRate.mu.Lock()
	rate := e.repairRate.rate
	at := e.repairRate.at
	e.repairRate.mu.Unlock()
	if rate <= 0 {
		return 0
	}
	// A stalled provider must make the forecast worse, not erase it and
	// suppress switching precisely when the viewer's buffer is draining.
	if age := time.Since(at); age > time.Second {
		rate /= min(30., age.Seconds())
	}
	rate /= float64(max(int64(1), e.active.Load()))
	return time.Duration(min(3600., float64(max(int64(0), n))/rate*1.5)*float64(time.Second)) + 250*time.Millisecond
}

// Every attempt announces damage before waiting for the shared repair gate.
// The ticker keeps checking the viewer's moving deadline during blocked NNTP
// reads; a successful solve cannot leave a stale failure notification behind.
func (e *Engine) watchRepair(parent context.Context, pf *postedFile, slot int, prio core.Priority, fo fetchOpts) (context.Context, func(int64, int64), func(int64), func(bool)) {
	if report := fo.repairProgress; report != nil {
		attempt := e.repairNotice.Add(1)
		fo.repairProgress = func(pf *postedFile, slot int, event core.RepairEvent) core.RepairWindow {
			event.Attempt = attempt
			return report(pf, slot, event)
		}
	}
	wait := e.repairOptions().AheadWait.D()
	if wait <= 0 {
		wait = 90 * time.Second
	}
	live := e.repairOptions().LiveWait.D()
	if live <= 0 {
		live = 2 * time.Second
	}
	w := core.RepairWindow{}
	if fo.repairProgress != nil {
		w = fo.repairProgress(pf, slot, core.RepairEvent{Phase: "missing"})
	}
	if prio == core.PriorityLive && w.Deadline.IsZero() {
		wait = live
	}
	deadline := time.Now().Add(wait)
	ctx, cancel := context.WithDeadline(parent, deadline)
	if w.Stop {
		cancel()
	}
	var wire, logical, remaining atomic.Int64
	planned := func(bytes, input int64) {
		wire.Store(bytes)
		logical.Store(max(int64(1), input))
		remaining.Store(input)
	}
	progress := func(n int64) { remaining.Add(-n) }
	done := make(chan struct{})
	stopEngine := context.AfterFunc(e.ctx, cancel)
	if fo.repairProgress == nil {
		close(done)
	} else {
		go func() {
			defer close(done)
			soft := time.NewTimer(time.Until(deadline))
			defer soft.Stop()
			if !w.Deadline.IsZero() {
				soft.Reset(max(0, time.Until(w.Deadline)))
			}
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-soft.C:
					cancel()
					return
				case <-tick.C:
					n := int64(float64(wire.Load()) * float64(max(int64(0), remaining.Load())) / float64(max(int64(1), logical.Load())))
					phase := "planning"
					var estimate time.Duration
					if logical.Load() > 0 {
						phase = "repairing"
						estimate = e.repairETA(n)
					}
					w := fo.repairProgress(pf, slot, core.RepairEvent{Phase: phase, RemainingBytes: n, Estimate: estimate})
					if w.Stop || (!w.Deadline.IsZero() && !time.Now().Before(w.Deadline)) {
						cancel()
						return
					}
					if !w.Deadline.IsZero() {
						soft.Reset(max(0, time.Until(w.Deadline)))
					} else {
						soft.Reset(max(0, time.Until(deadline)))
					}
				}
			}
		}()
	}
	finish := func(ok bool) {
		cancel()
		<-done
		stopEngine()
		if fo.repairProgress != nil {
			phase := "failed"
			if ok {
				phase = "repaired"
			}
			if parent.Err() != nil || e.ctx.Err() != nil || (fo.repairValid != nil && !fo.repairValid()) {
				phase = "obsolete"
			}
			fo.repairProgress(pf, slot, core.RepairEvent{Phase: phase})
		}
	}
	return ctx, planned, progress, finish
}

// Count unique uncached articles, respecting wire granularity. This is a
// conservative plan: unobserved helper holes may require additional equations.
func (e *Engine) estimateRepair(r *release, s *repairSession, holes []par2.Hole, whole bool, rows int, fo fetchOpts) (wire, logical int64) {
	var spans []damageSpan
	targets := map[int]bool{}
	for _, h := range holes {
		targets[h.Block] = true
		spans = append(spans, damageSpan{h.Begin &^ 1, (h.End + 1) &^ 1})
	}
	if whole {
		spans = []damageSpan{{0, s.layout.SliceSize}}
	}
	seen := map[segKey]bool{}
	has, _ := e.cache.(hasCache)
	for i, b := range s.layout.Blocks {
		if s.fixed[i] != nil {
			continue
		}
		intervals := spans
		if targets[i] {
			intervals = []damageSpan{{0, b.Length}}
		}
		pf := s.files[b.File.ID]
		for _, span := range intervals {
			lo, hi := min(span.begin, b.Length), min(span.end, b.Length)
			if hi <= lo {
				continue
			}
			logical += hi - lo
			for _, slot := range pf.slotsFor(b.Offset+lo, hi-lo) {
				key := segKey{r.digest, pf.segBase + slot}
				if seen[key] {
					continue
				}
				seen[key] = true
				if len(seen) > 131072 {
					return 1 << 40, max(int64(1), logical)
				}
				if e.hot.has(key) || (has != nil && fo.cacheID != "" && has.Has(fo.cacheID, key.seg)) {
					continue
				}
				a, z, _ := pf.rangeOf(slot)
				wire += max(pf.learnedPartSize(), z-a)
			}
		}
	}
	wire += int64(rows) * s.layout.SliceSize
	return wire, max(int64(1), logical)
}
