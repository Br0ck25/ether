package stream

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
)

// DurablePrefetch is for managed preloading: unlike best-effort startup warming,
// success requires every segment covering the requested span to remain on disk.
func (e *Engine) DurablePrefetch(ctx context.Context, id string, payload []byte, off, n int64) error {
	hc, ok := e.cache.(hasCache)
	if !ok {
		return errors.New("stream: persistent cache unavailable")
	}
	if err := e.Prefetch(ctx, id, payload, off, n); err != nil {
		return err
	}
	rel, err := e.acquire(payload)
	if err != nil {
		return err
	}
	defer e.release(rel)
	p, err := rel.ensurePlan(ctx, core.PriorityPrefetch, id)
	if err != nil {
		return err
	}
	for _, pc := range p.layout().pieces(off, n) {
		pf := p.vols[pc.vol]
		for _, i := range pf.slotsFor(pc.off, pc.n) {
			if !hc.Has(id, pf.segBase+i) {
				return errors.New("stream: preload segment was not retained on disk")
			}
		}
	}
	return ctx.Err()
}
