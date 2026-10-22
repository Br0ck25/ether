package playback

import (
	"context"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// swapChecker is implemented by HLS sessions whose delivered format is
// fixed for their lifetime (a segmented-Matroska header the player never
// refetches). It is optional: other sessions accept any replacement.
type swapChecker interface {
	CheckSwap(info core.MediaInfo) error
}

// preflightSwap reports whether the session's running HLS stream can take
// g as its replacement input. It runs before a failover publishes g, so an
// incompatible candidate is released and the next one tried. The mismatch
// is specific to this session: the release is not marked bad.
func (m *Manager) preflightSwap(ctx context.Context, s *session, g *generation) error {
	if m.deps.HLSLookup == nil {
		return nil
	}
	hls, ok := m.deps.HLSLookup(s.id)
	if !ok || hls == nil {
		return nil
	}
	c, ok := hls.(swapChecker)
	if !ok {
		return nil
	}
	if err := c.CheckSwap(g.infoSnapshot()); err != nil {
		return fmt.Errorf("%w: %v", core.ErrNotAvailable, err)
	}
	if preparer, ok := hls.(interface {
		PrepareSwap(context.Context, string, core.MediaInfo, time.Duration) error
	}); ok {
		at := time.Duration(0)
		if position, ok := hls.(core.PlaybackPosition); ok {
			at = position.RequestedPosition()
		}
		if source, ok := hls.(core.RecoveryBufferSource); ok {
			b := source.RecoveryBuffer()
			if b.ReadyUntil > at && b.ReadyUntil < g.infoSnapshot().Duration {
				at = b.ReadyUntil
			}
		}
		if err := preparer.PrepareSwap(ctx, m.urlFor(g), g.infoSnapshot(), at); err != nil {
			return fmt.Errorf("%w: replacement picture preparation failed", core.ErrNotAvailable)
		}
	}
	return nil
}
