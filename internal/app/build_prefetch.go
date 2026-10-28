package app

import (
	"context"
	"fmt"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/prefetch"
)

func (a *App) buildPrefetch(ctx context.Context, cfg config.Config) error {
	log := a.log
	var err error
	// Post-play pre-buffering (#30): prewarm through the playback manager
	// once it implements core.Prewarmer; until then prefetch resolves and
	// pulls the opening bytes itself.
	var prewarmer core.Prewarmer
	if pw, ok := any(a.playback).(core.Prewarmer); ok {
		prewarmer = pw
	}
	a.prefetch, err = prefetch.New(prefetch.Deps{
		Catalog: a.catalog, Resolver: a.resolver, Engine: a.media,
		State: a.state, Auth: a.users, Hints: a.playback,
		Logger:   log.With("component", "prefetch"),
		PostPlay: a.personalize, Prewarmer: prewarmer, Markers: a.markers,
		PostPlayEnabled: func() bool {
			c := a.cfg.Get().Personalize
			return c.PostPlay && c.PostPlayPrebuffer
		},
	}, cfg.Cache, prefetch.Options{})
	if err != nil {
		return fmt.Errorf("prefetch: %w", err)
	}
	a.closers = append(a.closers, func() { a.prefetch.Close() })

	return nil
}
