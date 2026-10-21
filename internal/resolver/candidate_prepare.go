package resolver

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func (s *Service) validateOne(ctx context.Context, rel core.Release) validation {
	v := validation{}
	at := time.Now()
	v.nzb, v.err = s.indexer.FetchNZB(ctx, rel)
	v.fetchDuration = time.Since(at)
	if v.err == nil {
		v.healthy = true
		if s.health != nil {
			v.healthChecked = true
			at = time.Now()
			v.healthy, v.healthCached, v.err = s.checkHealthCached(ctx, rel.ID, v.nzb)
			v.healthDuration = time.Since(at)
		}
	}
	return v
}

type candidateCheck struct {
	started time.Time // includes time spent checking during source discovery
	done    chan struct{}
	cancel  context.CancelFunc
	result  validation // immutable after done closes
}

func (c *candidateCheck) wait(ctx context.Context) validation {
	select {
	case <-c.done:
		return c.result
	case <-ctx.Done():
		return validation{err: ctx.Err()}
	}
}

// pendingPrep is one descriptor-preparation call running alongside the
// speculative checks. done is buffered so the goroutine can always finish;
// consumed marks that the result was already taken (or drained by close).
type pendingPrep struct {
	cancel   context.CancelFunc
	done     chan []core.Release
	consumed bool
}

// Owned by one Resolve call. Its deferred close cancels and joins every
// speculative check even when another source wins or the client leaves.
type candidateChecks struct {
	all   []*candidateCheck
	preps []*pendingPrep
}

func (c *candidateChecks) start(ctx context.Context, s *Service, rel core.Release) *candidateCheck {
	ctx, cancel := context.WithTimeout(ctx, candidateBudget)
	f := &candidateCheck{started: time.Now(), done: make(chan struct{}), cancel: cancel}
	c.all = append(c.all, f)
	go func() { defer close(f.done); defer cancel(); f.result = s.validateOne(ctx, rel) }()
	return f
}

func (c *candidateChecks) close() {
	for _, f := range c.all {
		f.cancel()
	}
	for _, p := range c.preps {
		p.cancel()
	}
	for _, f := range c.all {
		<-f.done
	}
	for _, p := range c.preps {
		if !p.consumed {
			<-p.done
			p.consumed = true
		}
	}
}
