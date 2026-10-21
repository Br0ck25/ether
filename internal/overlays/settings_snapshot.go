package overlays

import "context"

type settingsSnapshotKey struct{ service *Service }
type settingsSnapshot struct {
	value Settings
	err   error
}

// WithSettingsSnapshot reads settings once for a bounded render. It avoids a
// database round trip per tile without keeping settings stale across requests.
func (s *Service) WithSettingsSnapshot(ctx context.Context) context.Context {
	if _, ok := ctx.Value(settingsSnapshotKey{s}).(settingsSnapshot); ok {
		return ctx
	}
	value, err := s.Settings(ctx)
	return context.WithValue(ctx, settingsSnapshotKey{s}, settingsSnapshot{value, err})
}
