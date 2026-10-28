package newznab

import (
	"context"
	"time"
)

// budgetRetentionDays is how many days of per-indexer hit/grab counters are
// kept. Budgets only ever read today's row; a month of history is plenty for
// the admin UI and debugging.
const budgetRetentionDays = 35

// Prune deletes budget rows older than budgetRetentionDays and any expired
// search-cache rows (normally removed on write, but an idle indexer never
// writes). It is meant to be called periodically.
func (m *Manager) Prune(ctx context.Context) (int64, error) {
	if m.db == nil {
		return 0, nil
	}
	now := time.Now
	if m.opts.Now != nil {
		now = m.opts.Now
	}
	t := now()
	cutoff := t.UTC().AddDate(0, 0, -budgetRetentionDays).Format("2006-01-02")
	res, err := m.db.ExecContext(ctx, `DELETE FROM newznab_budget WHERE day<?`, cutoff)
	if err != nil {
		return 0, err
	}
	total, _ := res.RowsAffected()
	res, err = m.db.ExecContext(ctx, `DELETE FROM newznab_cache WHERE expires_at<?`, t.Unix())
	if err != nil {
		return total, err
	}
	n, _ := res.RowsAffected()
	return total + n, nil
}
