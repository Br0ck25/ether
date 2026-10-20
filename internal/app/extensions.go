package app

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/cardigann"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/resolver"
)

// extensionHandlers keeps optional owner features behind the same LAN, token
// and CSRF boundaries as the existing admin API.
func (a *App) extensionHandlers() map[string]http.Handler {
	m := map[string]http.Handler{"safety/policies": a.safety.Handler()}
	m["stats"] = a.stats.Handler()
	m["notifications"] = a.notifications.Handler()
	m["livetv"] = a.livetv.Handler()
	m["safety/diagnostics"] = a.diagnosticsHandler()
	m["playback"] = a.playbackPolicyHandler()
	m["indexer-sites"] = a.sites.Handler(cardigann.HandlerDeps{
		Indexers: func() []config.Indexer { return a.cfg.Get().Indexers },
		Unredact: func(ix config.Indexer) config.Indexer { return admin.UnredactIndexer(ix, a.cfg.Get()) },
	})
	m["stream-preferences"] = a.trackprefs.Handler()
	m["markers"] = a.markers.Handler()
	m["subtitle-sync"] = a.subtitles.SyncHandler()
	m["metadata"] = a.episodeorder.Router()
	m["releases"] = a.resolver.ConsoleHandler(resolver.ConsoleDeps{
		Catalog: a.catalog,
		Invalidate: func(key core.RatingKey) {
			if a.playback != nil {
				a.playback.InvalidateTitle(key)
			}
		},
		Logger: a.log.With("component", "release-console"),
	})
	m["updates"] = a.updatesHandler()
	a.discoveryHandlers(m)
	adoptionHandlers(a, m)
	return m
}
