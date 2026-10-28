package stream

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
)

// Keep the inexpensive STAT/header sample for healthy releases. A confirmed
// hole is admitted only after an actual verified recovery, never merely because
// a PAR2 filename claims enough parity. Context bounds the whole health check.
func (e *Engine) repairHealthSample(ctx context.Context, r *release, ref slotRef, fo fetchOpts) bool {
	if !e.repairOptions().Enabled {
		return false
	}
	_, err := e.segment(ctx, r, ref.pf, ref.slot, core.PriorityReadahead, fo)
	return err == nil && ctx.Err() == nil
}
