package markers

import "context"

// Prune deletes expired rows from the remote-lookup cache (markers_cache).
// An expired row is never served, so this only reclaims space; the next
// playback re-fetches as it would anyway.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	if s.o.DB == nil {
		return 0, nil
	}
	now := s.o.Now().Unix()
	var total int64
	for _, q := range []string{
		`DELETE FROM markers_cache WHERE expires_at<=?`,
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
