package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/jellyfin"
)

// jellyfinFront runs the Jellyfin and Emby API on its own port beside the
// Plex one. It follows the settings: switching it on, off or to another
// port takes effect at once, without a restart.
type jellyfinFront struct {
	log *slog.Logger
	srv *jellyfin.Server

	mu      sync.Mutex
	ctx     context.Context
	http    *http.Server
	port    int
	bind    string
	discov  bool
	cancelD context.CancelFunc
	wg      sync.WaitGroup
}

// buildJellyfin assembles the front end from the same components the Plex
// one uses. It does not listen until run.
func (a *App) buildJellyfin(cfg config.Config) {
	var tc core.Transcoder
	if a.transcoder != nil {
		tc = a.transcoder
	}
	deps := jellyfin.Deps{
		Catalog:      a.catalog,
		Collections:  a.discoveryPersonal.ReviewCollections(a.collections),
		State:        a.state,
		Playlists:    a.state.Playlists(),
		Auth:         a.users,
		Playback:     a.playback,
		Transcoder:   tc,
		Markers:      a.markers,
		Subtitles:    a.subtitles,
		AccessPolicy: a.safety,
		Store:        jellyfin.NewStore(a.db, nil),
		ImageURL:     a.tmdb.ImageURLFunc(),
		Config:       a.cfg.Get,
		Logger:       a.log.With("component", "jellyfin"),
		ServerID:     cfg.Server.MachineIdentifier,
		ServerName:   cfg.Server.FriendlyName,
		OnTimeline: func(u *core.User, key core.RatingKey, offset, duration time.Duration, state string) {
			if u != nil && state == "playing" {
				a.personalize.NotePlayback(u, key)
			}
			a.prefetch.OnTimeline(u, key, offset, duration, state)
		},
	}
	if a.availability != nil {
		deps.Catalog = &availabilityCatalog{Catalog: a.catalog, availability: a.availability}
		deps.FilterAvailable = a.availability.FilterDiscovery
	}
	a.jellyfin = &jellyfinFront{log: deps.Logger, srv: jellyfin.NewServer(deps)}
	a.closers = append(a.closers, a.jellyfin.close)
	a.cfg.Subscribe(a.jellyfin.apply)
}

// run serves until ctx ends, keeping the listener in step with the settings
// and ending plays that went quiet.
func (j *jellyfinFront) run(ctx context.Context, cfg config.Config) {
	j.mu.Lock()
	j.ctx = ctx
	j.mu.Unlock()
	j.apply(cfg)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			j.stop()
			return
		case <-t.C:
			j.srv.Sweep()
		}
	}
}

// apply starts, stops or moves the listener to match cfg.
func (j *jellyfinFront) apply(cfg config.Config) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ctx == nil || j.ctx.Err() != nil {
		return
	}
	want := cfg.Jellyfin.Enabled
	port := cfg.Jellyfin.Port
	if port == 0 {
		port = config.DefaultJellyfinPort
	}
	if want && port == cfg.Server.Port {
		j.log.Warn("the Jellyfin and Emby port must differ from the Plex port; not listening", "port", port)
		want = false
	}
	bind := cfg.Server.BindAddress
	if !want || j.http != nil && (j.port != port || j.bind != bind) {
		j.stopLocked()
	}
	if want && j.http == nil {
		j.startLocked(port, bind, cfg)
	}
	switch {
	case want && cfg.Jellyfin.Discovery && j.cancelD == nil:
		j.startDiscoveryLocked(bind, port, cfg)
	case (!want || !cfg.Jellyfin.Discovery) && j.cancelD != nil:
		j.cancelD()
		j.cancelD = nil
	}
}

func (j *jellyfinFront) startLocked(port int, bind string, cfg config.Config) {
	addr := net.JoinHostPort(bind, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		j.log.Warn("Jellyfin and Emby front end could not listen", "addr", addr, "err", err)
		return
	}
	srv := &http.Server{
		Handler:           j.srv,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(j.log.Handler(), slog.LevelDebug),
	}
	j.http, j.port, j.bind = srv, port, bind
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			j.log.Warn("Jellyfin and Emby front end stopped", "err", err)
		}
	}()
	j.log.Info("Jellyfin and Emby apps can connect", "addr", addr)
}

func (j *jellyfinFront) startDiscoveryLocked(bind string, port int, cfg config.Config) {
	ctx, cancel := context.WithCancel(j.ctx)
	j.cancelD = cancel
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		if err := jellyfin.ServeDiscovery(ctx, bind, port, cfg.Server.MachineIdentifier, cfg.Server.FriendlyName); err != nil {
			j.log.Warn("Jellyfin and Emby LAN discovery unavailable", "err", err)
		}
	}()
}

func (j *jellyfinFront) stopLocked() {
	if j.cancelD != nil {
		j.cancelD()
		j.cancelD = nil
	}
	if j.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = j.http.Shutdown(ctx)
		cancel()
		j.http = nil
	}
}

func (j *jellyfinFront) stop() {
	j.mu.Lock()
	j.stopLocked()
	j.mu.Unlock()
	j.wg.Wait()
}

func (j *jellyfinFront) close() {
	j.stop()
	j.srv.Close()
}
