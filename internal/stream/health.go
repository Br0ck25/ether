package stream

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
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
func (e *Engine) Check(ctx context.Context, nzbData []byte) (healthy bool, err error) {
	defer func() {
		if err != nil && IsRejection(err) {
			err = fmt.Errorf("%w: %w", core.ErrReleaseRejected, err)
		}
	}()
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

	type planned struct {
		p       *plan
		err     error
		elapsed time.Duration
	}
	planCh := make(chan planned, 1)
	go func() {
		started := time.Now()
		p, err := rel.ensurePlan(sctx, core.PriorityLive, "")
		planCh <- planned{p, err, time.Since(started)}
	}()
	var ready planned
	var res statResult
	gotStats, gotPlan := false, false
	defer func() {
		cancelStats()
		if !gotStats {
			<-statCh
		}
		if !gotPlan {
			<-planCh
		}
	}()
	for !gotStats || !gotPlan {
		select {
		case <-ctx.Done():
			cancelStats()
			return false, ctx.Err()
		case res = <-statCh:
			gotStats = true
			trace.STAT, trace.Samples = res.duration, res.samples
			if res.err != nil || res.missing > 0 {
				cancelStats()
				if res.err != nil {
					return false, res.err
				}
				return false, fmt.Errorf("stream: sampled BODY or STAT unavailable: %w", core.ErrArticleMissing)
			}
		case ready = <-planCh:
			gotPlan = true
			trace.Plan = ready.elapsed
			if ready.err != nil {
				return false, ready.err
			}
		}
	}
	p := ready.p
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
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	type result struct {
		missing bool
		err     error
	}
	results := make(chan result, len(refs))
	type job struct {
		ref  slotRef
		body bool
	}
	jobs := make(chan job, len(refs))
	for _, wantBody := range []bool{true, false} {
		for i, ref := range refs {
			body := len(refs) >= 4 && (i == len(refs)/3 || i == len(refs)*2/3)
			if body == wantBody {
				jobs <- job{ref, body}
			}
		}
	}
	close(jobs)
	check := func(j job) result {
		ref := j.ref
		id := ref.pf.msgID(ref.slot)
		if id == "" {
			return result{missing: true}
		}
		if j.body {
			var err error
			if checker, ok := e.fetcher.(interface {
				CheckBody(context.Context, string, []string, core.Priority) error
			}); ok {
				err = checker.CheckBody(ctx, id, ref.pf.groups, core.PriorityLive)
			} else {
				_, err = e.fetcher.Body(ctx, id, ref.pf.groups, core.PriorityLive)
			}
			return result{missing: isMissing(err), err: err}
		}
		ok, err := e.fetcher.Stat(ctx, id)
		return result{missing: (!ok && err == nil) || isMissing(err), err: err}
	}
	for i := 0; i < min(e.opts.HealthParallel, len(refs)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j, ok := <-jobs:
					if !ok {
						return
					}
					results <- check(j)
				}
			}
		}()
	}
	for range refs {
		select {
		case <-ctx.Done():
			return 0, len(refs), ctx.Err()
		case r := <-results:
			if r.missing {
				return 1, len(refs), nil
			}
			if r.err != nil {
				return 0, len(refs), r.err
			}
		}
	}
	return 0, len(refs), nil
}
