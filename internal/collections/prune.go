package collections

import (
	"context"
	"time"
)

// Retention for the collections lookup caches. Both are derived from TMDB
// and catalogue lookups, so a deleted row only costs one repeat lookup.
const (
	// nameRetention: name<->id rows are re-validated after 30 days on use
	// and kept stale as a fallback when TMDB is down; rows untouched for
	// six months belong to names no spec references any more.
	nameRetention = 180 * 24 * time.Hour
	// externalRetention: IMDb/Letterboxd/title -> rating key mappings are
	// written once and never refreshed on read, so an item still on a list
	// is simply re-resolved once a year.
	externalRetention = 365 * 24 * time.Hour
)

// Prune deletes stale rows from collections_names and collections_external.
// Specs, members and owner data are never touched. It is meant to be called
// periodically.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	now := s.now()
	var total int64
	for _, q := range []struct {
		sql string
		age time.Duration
	}{
		{`DELETE FROM collections_names WHERE at<?`, nameRetention},
		{`DELETE FROM collections_external WHERE at<?`, externalRetention},
	} {
		res, err := s.db.ExecContext(ctx, q.sql, now.Add(-q.age).Unix())
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
