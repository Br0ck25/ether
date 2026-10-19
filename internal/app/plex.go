package app

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/plex"
	"github.com/ether-media/ether/internal/plextv"
	"github.com/ether-media/ether/internal/spike"
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
	}
	hooks := plex.Hooks{
		OnTimeline: func(u *core.User, key core.RatingKey, offset, duration time.Duration, state string) {
			if u != nil {
				a.playback.Timeline(u.ID, key, state)
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
			Collections: a.collections,
			Recommender: a.recommend,
			State:       a.state,
			Playlists:   a.state.Playlists(),
			Auth:        a.users,
			Playback:    a.playback,
			Transcoder:  tc,
			ImageURL:    a.tmdb.ImageURLFunc(),
			DB:          a.db, // plex_ultrablur
		}
	}
	deps.Config = a.cfg.Get
	deps.Logger = a.log.With("component", "plex")
	deps.Identity = identity
	deps.Hooks = hooks
	srv := plex.NewServer(deps)
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
