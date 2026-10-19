package app

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

// dbMaintenanceInterval is how often the SQLite WAL is truncated and the
// planner statistics refreshed. The first pass waits dbMaintenanceDelay so
// it never competes with startup work.
const (
	dbMaintenanceInterval = 24 * time.Hour
	dbMaintenanceDelay    = 15 * time.Minute
)

// maintainDB runs store.Maintain daily until ctx ends.
func (a *App) maintainDB(ctx context.Context) {
	timer := time.NewTimer(dbMaintenanceDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		start := time.Now()
		if err := store.Maintain(ctx, a.db); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("database maintenance", "err", err)
		} else {
			a.log.Debug("database maintenance done", "took", time.Since(start))
		}
		timer.Reset(dbMaintenanceInterval)
	}
}
