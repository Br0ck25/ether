package stream

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ether-media/ether/internal/core"
)

// Check implements core.HealthChecker. It parses the NZB, inspects the
// archive headers of the main video (rejecting compressed, solid and
// encrypted archives and releases without a playable file) and STATs a
// spread sample of the main video's articles in parallel: the first, the
// last and evenly spaced ones across every volume.
//
// healthy=false comes with an error saying why: one wrapping
// core.ErrArticleMissing (sampled articles are gone or the NZB is
// incomplete), one of the package's rejection errors (see IsRejection), or a
// transient error (network, context).
//
// The index built here is kept, so an Open of the same NZB right after a
// successful Check does not repeat the header fetches.
func (e *Engine) Check(ctx context.Context, nzbData []byte) (bool, error) {
	trace := HealthTrace{}
	defer func() { e.healthMu.Lock(); e.healthTrace = trace; e.healthMu.Unlock() }()
	if err := e.ctx.Err(); err != nil {
		return false, ErrClosed
	}
	parseStart := time.Now()
	rel, err := e.acquire(nzbData)
	trace.Parse = time.Since(parseStart)
	if err != nil {
		return false, err
	}
	defer e.release(rel)
	if warmer, ok := e.fetcher.(interface {
		Warm(context.Context, int) error
	}); ok {
		start := time.Now()
		err := warmer.Warm(ctx, 4)
		trace.Warm = time.Since(start)
		if err != nil {
			e.log.Debug("stream health warm failed; continuing with demand dials", "err", err)
		}
	}

	b := &builder{e: e, rel: rel, prio: core.PriorityLive}
	cands := b.candidates(rel.files)

	// STAT the likely main candidate while its headers are inspected.
	type statResult struct {
		files    []*postedFile
		missing  int
		total    int
		err      error
		duration time.Duration
		samples  int
	}
	statCh := make(chan statResult, 1)
	sctx, cancelStats := context.WithCancel(ctx)
	defer cancelStats()
	var guessed []*postedFile
	if len(cands) > 0 && !b.shouldProbe(cands) {
		guessed = cands[0].files
		go func() {
			started := time.Now()
			m, t, err := e.statSample(sctx, guessed)
			statCh <- statResult{files: guessed, missing: m, total: t, err: err, duration: time.Since(started), samples: t}
		}()
	} else {
		statCh <- statResult{}
	}

	planStart := time.Now()
	p, err := rel.ensurePlan(ctx, core.PriorityLive, "")
	trace.Plan = time.Since(planStart)
	if err != nil {
		return false, err
	}
	res := <-statCh
	if !sameFiles(res.files, p.vols) {
		started := time.Now()
		m, t, err := e.statSample(ctx, p.vols)
		res = statResult{files: p.vols, missing: m, total: t, err: err, duration: time.Since(started), samples: t}
	}
	trace.STAT = res.duration
	trace.Samples = res.samples
	if res.err != nil {
		return false, res.err
	}
	if res.missing > 0 {
		return false, fmt.Errorf("stream: %d of %d sampled articles of %s missing: %w", res.missing, res.total, p.name, core.ErrArticleMissing)
	}
	return true, nil
}

func sameFiles(a, b []*postedFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// healthSample picks n slots spread over the concatenated parts of files:
// always the first and the last, the rest evenly spaced.
func healthSample(files []*postedFile, n int) []slotRef {
	var total int
	for _, pf := range files {
		total += pf.numSlots()
	}
	if total == 0 {
		return nil
	}
	if n < 2 {
		n = 2
	}
	at := func(g int) slotRef {
		for _, pf := range files {
			if g < pf.numSlots() {
				return slotRef{pf, g}
			}
			g -= pf.numSlots()
		}
		last := files[len(files)-1]
		return slotRef{last, last.numSlots() - 1}
	}
	if total <= n {
		out := make([]slotRef, 0, total)
		for g := 0; g < total; g++ {
			out = append(out, at(g))
		}
		return out
	}
	out := make([]slotRef, 0, n)
	prev := -1
	for j := 0; j < n; j++ {
		g := int((int64(j)*int64(total-1) + int64(n-1)/2) / int64(n-1))
		if g == prev {
			continue
		}
		prev = g
		out = append(out, at(g))
	}
	return out
}

type slotRef struct {
	pf   *postedFile
	slot int
}

// statSample STATs the health sample of files in parallel.
func (e *Engine) statSample(ctx context.Context, files []*postedFile) (missing, total int, err error) {
	refs := healthSample(files, e.opts.HealthSamples)
	if many, ok := e.fetcher.(interface {
		StatMany(context.Context, []string) (map[string]bool, error)
	}); ok {
		ids := make([]string, 0, len(refs))
		for _, r := range refs {
			id := r.pf.msgID(r.slot)
			if id == "" {
				missing++
			} else {
				ids = append(ids, id)
			}
		}
		got, err := many.StatMany(ctx, ids)
		if err != nil {
			return missing, len(refs), err
		}
		for _, id := range ids {
			if !got[id] {
				missing++
			}
		}
		return missing, len(refs), nil
	}
	var miss atomic.Int64
	var mu sync.Mutex
	var first error
	sem := make(chan struct{}, e.opts.HealthParallel)
	var wg sync.WaitGroup
	for _, r := range refs {
		id := r.pf.msgID(r.slot)
		if id == "" {
			miss.Add(1)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return 0, len(refs), ctx.Err()
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			ok, err := e.fetcher.Stat(ctx, id)
			switch {
			case err != nil && isMissing(err):
				miss.Add(1)
			case err != nil:
				mu.Lock()
				if first == nil {
					first = fmt.Errorf("stream: stat <%s>: %w", id, err)
				}
				mu.Unlock()
			case !ok:
				miss.Add(1)
			}
		}(id)
	}
	wg.Wait()
	if first != nil {
		return int(miss.Load()), len(refs), first
	}
	return int(miss.Load()), len(refs), nil
}
