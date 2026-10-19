package playback

import (
	"context"
	"errors"
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

// LocalMedia exposes adopted owner files without coupling playback to their DB.
type LocalMedia interface {
	Preferred(context.Context, core.RatingKey) (core.MediaSource, error)
	MediaInfo(context.Context, core.RatingKey) (*core.MediaInfo, error)
}

func (m *Manager) openLocal(ctx context.Context, s *session, profile core.QualityProfile) (*generation, error) {
	if m.deps.LocalMedia == nil {
		return nil, nil
	}
	src, err := m.deps.LocalMedia.Preferred(ctx, s.ident.key)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("local media: %w", err)
	}
	if src == nil {
		return nil, nil
	}
	if src.Size() <= 0 {
		_ = src.Close()
		return nil, fmt.Errorf("local media: empty file")
	}
	fallback := func(size int64) core.MediaInfo {
		if info, err := m.deps.LocalMedia.MediaInfo(ctx, s.ident.key); err == nil && info != nil {
			info.Size = size
			return *info
		}
		info := EstimateInfo(s.title, s.caps, profile)
		info.Size = size
		return info
	}
	res := &core.Resolution{Title: s.ident.key, Release: core.Release{ID: "local:" + s.ident.key.String(), Name: src.Name(), Size: src.Size()}, ResolvedAt: m.now()}
	return m.probeGeneration(ctx, s, res, src, fallback)
}
