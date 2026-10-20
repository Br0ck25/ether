package store

import (
	"context"
	"database/sql"
	"fmt"
)

// journalSizeLimit bounds the WAL file kept on disk after checkpoints.
const journalSizeLimit = 64 << 20

// Maintain is the periodic (daily) upkeep for a long-running database: it
// checkpoints and truncates the WAL so it cannot creep upward while readers
// keep pinning it, then refreshes query-planner statistics. It deliberately
// never VACUUMs; that rewrites the whole file and is left to the owner.
//
// On PostgreSQL the server manages its own WAL and autovacuum; Maintain only
// refreshes planner statistics with ANALYZE.
func Maintain(ctx context.Context, db *sql.DB) error {
	if DialectOf(db) == Postgres {
		if _, err := db.ExecContext(ctx, `ANALYZE`); err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		return nil
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	return nil
}
