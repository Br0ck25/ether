package playback

import (
	"context"
	"time"
)

// warmPreparationContext bounds speculative work until a Play flight takes
// responsibility for it. Promotion retains the same in-flight operations,
// bounded by that flight's deadline and cancellation. Shutdown always wins.
func warmPreparationContext(parent context.Context, budget time.Duration, foreground <-chan context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		timer := time.NewTimer(budget)
		defer timer.Stop()
		defer cancel()
		select {
		case live := <-foreground:
			select {
			case <-live.Done():
			case <-ctx.Done():
			}
		case <-timer.C:
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
