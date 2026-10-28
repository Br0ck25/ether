package plex

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type inputFailover interface {
	FailoverInput(context.Context, string, string, time.Duration) error
}

// RecoverTranscode maps the client's encoder ID to its playback session.
// Playback rechecks the input generation before attempting recovery.
func (s *Server) RecoverTranscode(ctx context.Context, id, inputURL string, at time.Duration) error {
	entry, ok := s.transcodes.get(id)
	if !ok || entry.playID == "" {
		return nil // stopped, or not a library playback session
	}
	p, ok := s.d.Playback.(inputFailover)
	if !ok {
		return core.ErrNotAvailable
	}
	return p.FailoverInput(ctx, entry.playID, inputURL, at)
}
