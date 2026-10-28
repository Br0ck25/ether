package stats

import "context"

// dailyRetentionDays bounds the per-day indexer and provider counters. The
// admin UI reads the current month; two years keeps year-over-year
// comparisons possible without letting the tables grow forever. Plays
// (stats_plays) are user history and are never pruned here.
const dailyRetentionDays = 730

// Prune deletes per-day indexer/provider counters older than
// dailyRetentionDays. It is meant to be called periodically.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	cutoff := s.opts.Now().UTC().AddDate(0, 0, -dailyRetentionDays).Format("2006-01-02")
	var total int64
	for _, q := range []string{
		`DELETE FROM stats_indexer_days WHERE day<?`,
		`DELETE FROM stats_provider_days WHERE day<?`,
	} {
		res, err := s.db.ExecContext(ctx, q, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
