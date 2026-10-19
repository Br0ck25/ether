package markers

import "context"

// Prune deletes expired rows from the remote-lookup cache (markers_cache)
// and the detection ledger (markers_detected). Neither table serves an
// expired row, so this only reclaims space; the next playback re-fetches or
// re-detects as it would anyway. Fingerprints are kept: they are the input
// for matching later episodes of the same season.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	if s.o.DB == nil {
		return 0, nil
	}
	now := s.o.Now().Unix()
	var total int64
	for _, q := range []string{
		`DELETE FROM markers_cache WHERE expires_at<=?`,
		`DELETE FROM markers_detected WHERE expires_at<=?`,
	} {
		res, err := s.o.DB.ExecContext(ctx, q, now)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
