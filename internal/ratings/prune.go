package ratings

import (
	"context"
	"time"
)

// maxCacheAge is the longest cache interval SaveSettings accepts (2160 h).
// A row older than this can never be served whatever the setting, so it is
// safe to delete; rows inside it are kept in case the owner raises the
// interval. Rows of a provider the owner switched away from age out too.
const maxCacheAge = 2160 * time.Hour

// Prune deletes rating rows older than the maximum cache interval. It is
// meant to be called periodically.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	age := maxCacheAge
	if cfg, err := s.settings(ctx); err == nil {
		if d := time.Duration(cfg.CacheHours) * time.Hour; d > age {
			age = d
		}
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM ratings_cache WHERE updated<?`, s.o.Now().Add(-age).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
