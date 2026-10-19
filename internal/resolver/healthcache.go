package resolver

import (
	"context"
	"time"

	"github.com/ether-media/ether/internal/store"
)

func init() {
	store.Register("resolver", 2, `
CREATE TABLE resolver_verified (
  release_id TEXT PRIMARY KEY,
  checked_at INTEGER NOT NULL
);`)
}

// checkHealth runs the health checker unless the release passed a check
// within healthTTL. Health checks dominate time-to-playable, and a release
// that was fine an hour ago almost always still is; if it isn't, playback
// fails over mid-stream and MarkBad drops it from the cache.
func (s *Service) checkHealth(ctx context.Context, releaseID string, nzb []byte) (bool, error) {
	if s.health == nil {
		return true, nil
	}
	if s.recentlyVerified(ctx, releaseID) {
		return true, nil
	}
	ok, err := s.health.Check(ctx, nzb)
	if ok && err == nil && s.healthTTL > 0 && releaseID != "" {
		now := s.now()
		s.mu.Lock()
		s.verified[releaseID] = now
		s.mu.Unlock()
		if s.db != nil {
			_, _ = s.db.ExecContext(ctx, `INSERT INTO resolver_verified(release_id, checked_at) VALUES (?, ?)
				ON CONFLICT(release_id) DO UPDATE SET checked_at=excluded.checked_at`, releaseID, now.Unix())
		}
	}
	return ok, err
}

func (s *Service) recentlyVerified(ctx context.Context, releaseID string) bool {
	if s.healthTTL <= 0 || releaseID == "" {
		return false
	}
	s.mu.RLock()
	at, ok := s.verified[releaseID]
	s.mu.RUnlock()
	if !ok && s.db != nil {
		var unix int64
		if err := s.db.QueryRowContext(ctx, `SELECT checked_at FROM resolver_verified WHERE release_id=?`, releaseID).Scan(&unix); err == nil {
			at, ok = time.Unix(unix, 0), true
			s.mu.Lock()
			s.verified[releaseID] = at
			s.mu.Unlock()
		}
	}
	return ok && s.now().Sub(at) < s.healthTTL
}

func (s *Service) forgetVerified(ctx context.Context, releaseID string) {
	s.mu.Lock()
	delete(s.verified, releaseID)
	s.mu.Unlock()
	if s.db != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM resolver_verified WHERE release_id=?`, releaseID)
	}
}
