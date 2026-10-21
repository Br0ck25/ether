package app

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex"
	"github.com/braedonsaunders/ether/internal/plexguid"
	"github.com/braedonsaunders/ether/internal/plextv"
	"github.com/braedonsaunders/ether/internal/spike"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// buildPlex assembles the PMS-compatible front end. In spike mode the
// library is the configured local files instead of the TMDB catalogue, but
// auth, plex.tv registration and the transcoder are the real ones, so the
// spike exercises the same path official apps will use.
func (a *App) buildPlex(cfg config.Config) (http.Handler, error) {
	var tc core.Transcoder
	if a.transcoder != nil {
		tc = a.transcoder
	}
	identity := plex.Identity{
		MachineIdentifier: cfg.Server.MachineIdentifier,
		FriendlyName:      cfg.Server.FriendlyName,
		Version:           plextv.DefaultServerVersion,
		Platform:          plexPlatform(),
		// Reported honestly: the owner's real subscription state.
		MyPlexSubscription: a.registrar.Status().PlexPass,
	}
	hooks := plex.Hooks{
		OnTimeline: func(u *core.User, key core.RatingKey, offset, duration time.Duration, state string) {
			if u != nil {
				// Plex renews/stops the requesting client's exact session.
				// A title-wide renewal here would preserve abandoned streams
				// on another device watching the same title.
				// Session intent (#32): a detail open that just turned
				// into this playback was a conversion, not browsing.
				if state == "playing" {
					a.personalize.NotePlayback(u, key)
				}
			}
			a.prefetch.OnTimeline(u, key, offset, duration, state)
		},
	}

	var deps plex.Deps
	if cfg.Spike.Enabled {
		lib, err := spike.New(context.Background(), a.cfg.Get, spike.Options{Logger: a.log.With("component", "spike")})
		if err != nil {
			return nil, err
		}
		deps = lib.Deps()
		deps.Auth = a.users
		if tc != nil {
			deps.Transcoder = tc
		}
		hooks.OnTimeline = nil
	} else {
		deps = plex.Deps{
			Catalog:     a.catalog,
			Collections: a.discoveryPersonal.ReviewCollections(a.collections),
			VibeSearch:  a.collections,
			Recommender: a.personalize,
			State:       a.state,
			Playlists:   a.state.Playlists(),
			Auth:        a.users,
			Playback:    a.playback,
			Transcoder:  tc,
			ImageURL:    a.tmdb.ImageURLFunc(),
			DB:          a.db, // plex_ultrablur
		}
	}
	deps.PrioritizeAvailable = func(ctx context.Context, items []core.Title) []core.Title {
		if a.availability == nil {
			return items
		}
		return a.availability.Prioritize(ctx, items)
	}
	deps.Variants = a.resolver // "Play Version" release picker (cache-only)
	deps.PlexGUID = plexguid.New(a.db, plexguid.Options{
		Token:      func() string { return a.cfg.Get().Plex.Token },
		Background: true,
	})
	a.closers = append(a.closers, deps.PlexGUID.Close)
	deps.EpisodeOrder = a.episodeorder
	deps.Discovery = a.plexDiscovery
	deps.LiveTV = a.livetv
	deps.Subtitles = a.subtitles
	deps.TrackPreferences = a.trackprefs
	deps.Markers = a.markers
	if a.transcoder != nil && !cfg.Spike.Enabled {
		if dl, err := a.transcoder.NewDownloadRemuxer(cfg.Downloads); err == nil {
			deps.Downloads = dl
		} else {
			a.log.Warn("offline downloads unavailable", "err", err)
		}
	}
	deps.Extras = a.extras
	if a.transcoder != nil {
		deps.Trickplay = transcode.NewThumbnails(transcode.ThumbOptions{
			FFmpeg: a.transcoder.FFmpegPath(), Logger: a.log,
		})
	}
	deps.AccessPolicy = a.safety
	deps.Config = a.cfg.Get
	deps.Logger = a.log.With("component", "plex")
	deps.Identity = identity
	deps.Hooks = hooks
	if node := a.clusterNode(); node != nil && deps.DB != nil {
		deps.Cluster = node
	}
	srv := plex.NewServer(deps)
	a.plexServer = srv
	a.closers = append(a.closers, func() { srv.Close() })
	return srv, nil
}

func plexPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOSX"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}
