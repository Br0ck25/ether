package subtitles

import (
	"context"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Enrich is playback.Deps.AugmentMedia. The source is exclusively owned by the
// preparing generation, so local hashing cannot race disposal during failover.
// Remote media uses release matching until a cache-only edge reader exists.
func (s *Service) Enrich(ctx context.Context, title *core.Title, user *core.User, res *core.Resolution, src core.MediaSource, info *core.MediaInfo) {
	if !s.Enabled() || info == nil || res == nil {
		return
	}
	release := res.Release.Name
	hash := ""
	if src != nil && strings.HasPrefix(res.Release.ID, "local:") {
		var err error
		hash, err = MovieHash(src, src.Size())
		if err != nil {
			s.log.Debug("local subtitle hash unavailable", "error", err)
		}
	}
	sidecars, err := s.SidecarsWithHash(ctx, title, user, info.Streams, release, hash)
	if err != nil {
		s.log.Debug("subtitle enrichment unavailable", "error", err)
	}
	info.Streams = append(info.Streams, sidecars...)
}
