// Package app wires Ether's components into one running server: the Plex
// front end and the owner UI on one port, and the background services
// (catalogue sync, plex.tv registration, prefetch, LAN discovery) behind it.
package app

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ether-media/ether/internal/admin"
	"github.com/ether-media/ether/internal/auth"
	"github.com/ether-media/ether/internal/catalog"
	"github.com/ether-media/ether/internal/collections"
	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/llm"
	"github.com/ether-media/ether/internal/newznab"
	"github.com/ether-media/ether/internal/playback"
	"github.com/ether-media/ether/internal/plex"
	"github.com/ether-media/ether/internal/plextv"
	"github.com/ether-media/ether/internal/prefetch"
	"github.com/ether-media/ether/internal/recommend"
	"github.com/ether-media/ether/internal/remote"
	"github.com/ether-media/ether/internal/resolver"
	"github.com/ether-media/ether/internal/segcache"
	"github.com/ether-media/ether/internal/state"
	"github.com/ether-media/ether/internal/store"
	"github.com/ether-media/ether/internal/stream"
	"github.com/ether-media/ether/internal/subtitles"
	"github.com/ether-media/ether/internal/tmdb"
	"github.com/ether-media/ether/internal/trakt"
	"github.com/ether-media/ether/internal/transcode"
	"github.com/ether-media/ether/internal/updater"
	"github.com/ether-media/ether/internal/usenet"
)

// Options configure a run.
type Options struct {
	DataDir string
	Version string
	// Logger receives everything; the admin log ring is teed onto it.
	Logger *slog.Logger
	// Spike forces spike mode (local files, no Usenet) on top of config.
	Spike bool
	// SpikeFiles are extra local files for spike mode.
	SpikeFiles []string
}

// App is a running Ether server.
type App struct {
	opts Options
	log  *slog.Logger
	logs *admin.LogBuffer
	cfg  *config.Store
	db   *sql.DB

	tmdb        *tmdb.Client
	catalog     *catalog.Catalog
	collections *collections.Service
	state       *state.State
	trakt       *trakt.Client
	llm         *llm.Service
	recommend   *recommend.Recommender
	subtitles   *subtitles.Service
	fetcher     *usenet.Fetcher
	cache       *segcache.Cache
	engine      *stream.Engine
	indexers    *newznab.Manager
	resolver    *resolver.Service
	transcoder  *transcode.Manager
	playback    *playback.Manager
	prefetch    *prefetch.Prefetcher
	plextv      *plextv.Client
	users       *auth.Service
	certs       *plextv.CertManager
	registrar   *plextv.Registrar
	gdm         *remote.GDMResponder

	plexServer *plex.Server
	handler    http.Handler
	closers    []func()
}

// New builds every component. Nothing listens until Run.
func New(ctx context.Context, opts Options) (*App, error) {
	if opts.DataDir == "" {
		opts.DataDir = config.DefaultDataDir()
	}
	if opts.Version == "" {
		opts.Version = updater.Version
	}
	a := &App{opts: opts, logs: admin.NewLogBuffer(4000)}
	base := opts.Logger
	if base == nil {
		base = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	a.log = slog.New(admin.TeeHandler(base.Handler(), a.logs))
	slog.SetDefault(a.log)

	var err error
	if a.cfg, err = config.Open(opts.DataDir); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg := a.cfg.Get()
	if opts.Spike {
		_ = a.cfg.Update(func(c *config.Config) error {
			c.Spike.Enabled = true
			c.Spike.LocalFiles = appendUnique(c.Spike.LocalFiles, opts.SpikeFiles...)
			return nil
		})
		cfg = a.cfg.Get()
	}
	for _, dir := range []string{cfg.Cache.Dir, cfg.Transcode.TempDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
	}
	if a.db, err = store.Open(ctx, opts.DataDir); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a.closers = append(a.closers, func() { a.db.Close() })
	if err := a.build(ctx, cfg); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) build(ctx context.Context, cfg config.Config) error {
	log := a.log
	dataDir := a.opts.DataDir
	var err error

	// Catalogue.
	a.tmdb = tmdb.NewFromConfig(cfg.Catalog, log.With("component", "tmdb"))
	a.closers = append(a.closers, a.tmdb.Close)
	if a.catalog, err = catalog.New(a.db, a.tmdb, cfg.Catalog, log.With("component", "catalog")); err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	if a.collections, err = collections.New(a.db, a.catalog, log.With("component", "collections")); err != nil {
		return fmt.Errorf("collections: %w", err)
	}

	// Watch state and recommendations.
	a.state = state.New(a.db, state.Options{Logger: log.With("component", "state")})
	a.trakt = trakt.New(trakt.Options{Config: cfg.Trakt, DB: a.db, Catalog: a.catalog, Logger: log.With("component", "trakt")})
	a.trakt.SubscribeTo(a.state)
	a.closers = append(a.closers, a.trakt.Close)
	a.llm = llm.New(llm.ServiceOptions{
		Client:  llm.NewClient(llm.ClientOptions{Config: cfg.LLM, Logger: log.With("component", "llm")}),
		Catalog: a.catalog,
		Logger:  log.With("component", "llm"),
	})
	a.recommend = recommend.New(recommend.Options{
		Catalog: a.catalog, State: a.state, Collections: a.collections,
		Trakt: a.trakt, LLM: a.llm, Events: a.state,
		Logger: log.With("component", "recommend"),
	})
	a.closers = append(a.closers, a.recommend.Close)
	a.subtitles = subtitles.New(subtitles.Options{
		Config:    cfg.OpenSubtitles,
		CacheDir:  filepath.Join(dataDir, "subtitles"),
		Logger:    log.With("component", "subtitles"),
		UserAgent: "Ether v" + a.opts.Version,
	})

	// Usenet, cache, stream engine.
	a.fetcher = usenet.New(cfg.Usenet.Providers, usenet.Options{Logger: log.With("component", "usenet")})
	a.closers = append(a.closers, func() { a.fetcher.Close() })
	if a.cache, err = segcache.Open(cfg.Cache.Dir, cfg.Cache.BudgetBytes, log); err != nil {
		return fmt.Errorf("segment cache: %w", err)
	}
	a.closers = append(a.closers, func() { a.cache.Close() })
	a.engine = stream.New(a.fetcher, a.cache, stream.OptionsFromConfig(cfg), log.With("component", "stream"))
	a.closers = append(a.closers, func() { a.engine.Close() })

	// Indexers and resolver.
	a.indexers = newznab.NewManager(a.db, cfg.Indexers, newznab.Options{
		Logger:         log.With("component", "newznab"),
		SearchCacheTTL: cfg.Resolver.SearchCacheTTL.D(),
		NZBDir:         filepath.Join(dataDir, "nzb"),
	})
	a.resolver = resolver.New(a.indexers, a.engine, a.db, cfg, resolver.Options{Logger: log.With("component", "resolver")})

	// Transcoder. A missing ffmpeg is not fatal: direct play still works.
	a.transcoder, err = transcode.New(cfg.Transcode, log)
	if err != nil {
		log.Warn("transcoder unavailable; only direct play will work", "err", err)
		a.transcoder = nil
	} else {
		a.closers = append(a.closers, func() { a.transcoder.Close() })
	}
	var tc core.Transcoder
	if a.transcoder != nil {
		tc = a.transcoder
	}

	// Playback and prefetch.
	a.playback, err = playback.New(playback.Deps{
		Resolver:   a.resolver,
		Engine:     a.engine,
		Transcoder: tc,
		Catalog:    a.catalog,
		Cache:      a.cache,
		State:      a.state,
		SelectProfile: func(caps core.ClientCaps, u *core.User) core.QualityProfile {
			return resolver.SelectProfile(a.cfg.Get().Quality, caps, u)
		},
		FetchNZB: a.indexers.FetchNZB,
		// Transcode sessions are keyed by the client's session id; the Plex
		// front end (built below) maps playback sessions to them.
		HLSLookup: func(id string) (core.HLSSession, bool) {
			if a.plexServer != nil {
				if hs, ok := a.plexServer.TranscodeFor(id); ok {
					return hs, true
				}
			}
			if a.transcoder != nil {
				return a.transcoder.Session(id)
			}
			return nil, false
		},
		DB:     a.db,
		Logger: log,
	}, cfg, playback.Options{DataDir: dataDir})
	if err != nil {
		return fmt.Errorf("playback: %w", err)
	}
	a.closers = append(a.closers, func() { a.playback.Close() })
	a.cache.Protect(a.protectedReleases)

	// plex.tv, users, certificates, LAN discovery.
	if a.plextv, err = plextv.New(plextv.Options{Identity: plextv.ServerIdentity(cfg, "")}); err != nil {
		return fmt.Errorf("plex.tv client: %w", err)
	}
	if a.users, err = auth.New(context.Background(), a.db, auth.Options{
		Validator: auth.PlexTVValidator{Client: a.plextv, MachineID: cfg.Server.MachineIdentifier},
		Logger:    log.With("component", "auth"),
	}); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if cfg.Plex.Token != "" {
		if err := a.users.BootstrapOwner(context.Background(), cfg.Plex); err != nil {
			log.Warn("owner bootstrap", "err", err)
		}
	}
	if a.certs, err = plextv.NewCertManager(plextv.CertManagerOptions{
		Dir:            filepath.Join(dataDir, "certs"),
		Client:         a.plextv,
		CustomCertFile: cfg.Plex.CertFile,
		CustomKeyFile:  cfg.Plex.KeyFile,
		Logger:         log.With("component", "certs"),
	}); err != nil {
		return fmt.Errorf("certificates: %w", err)
	}
	if a.registrar, err = plextv.NewRegistrar(plextv.RegistrarOptions{
		Config: a.cfg,
		Client: a.plextv,
		Users:  a.users,
		Certs:  a.certs,
		Logger: log.With("component", "registrar"),
	}); err != nil {
		return fmt.Errorf("registrar: %w", err)
	}
	if cfg.Plex.Token != "" {
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := a.registrar.RefreshOwner(rctx); err != nil {
			log.Warn("plex.tv owner refresh", "err", err)
		}
		cancel()
	}
	a.gdm = remote.NewGDMResponder(remote.GDMOptions{
		Info: func() remote.GDMInfo {
			c := a.cfg.Get()
			return remote.GDMInfo{Name: c.Server.FriendlyName, MachineIdentifier: c.Server.MachineIdentifier,
				Port: c.Server.Port, Version: plextv.DefaultServerVersion, UpdatedAt: time.Now()}
		},
		Logger: log.With("component", "gdm"),
	})

	a.prefetch, err = prefetch.New(prefetch.Deps{
		Catalog: a.catalog, Resolver: a.resolver, Engine: a.engine,
		State: a.state, Auth: a.users, Hints: a.playback,
		Logger: log.With("component", "prefetch"),
	}, cfg.Cache, prefetch.Options{})
	if err != nil {
		return fmt.Errorf("prefetch: %w", err)
	}
	a.closers = append(a.closers, func() { a.prefetch.Close() })

	a.cfg.Subscribe(a.applyConfig)

	plexHandler, err := a.buildPlex(cfg)
	if err != nil {
		return fmt.Errorf("plex front end: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/ether/", a.buildAdmin())
	mux.HandleFunc("/ether", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ether/", http.StatusFound)
	})
	mux.Handle("/", plexHandler)
	a.handler = mux
	return nil
}

// applyConfig pushes an edited config into the running components.
func (a *App) applyConfig(c config.Config) {
	a.fetcher.Update(c.Usenet.Providers)
	a.indexers.ApplyConfig(c)
	a.resolver.UpdateConfig(c)
	a.catalog.UpdateConfig(c.Catalog)
	a.cache.SetBudget(c.Cache.BudgetBytes)
	a.playback.SetConfig(c)
	a.prefetch.SetConfig(c.Cache)
	if a.transcoder != nil {
		a.transcoder.UpdateConfig(c.Transcode)
	}
	a.trakt.UpdateConfig(c.Trakt)
	a.subtitles.UpdateConfig(c.OpenSubtitles)
	a.registrar.Trigger()
}

// protectedReleases is every release that must survive cache eviction:
// live sessions (playback pins those itself) plus pinned in-progress titles.
func (a *App) protectedReleases() map[string]bool {
	out := map[string]bool{}
	for _, s := range a.playback.Sessions() {
		if s.Resolution != nil {
			out[s.Resolution.Release.ID] = true
		}
	}
	return out
}

// Handler is the combined Plex + owner UI handler (tests).
func (a *App) Handler() http.Handler { return a.handler }

// Config is the settings store.
func (a *App) Config() *config.Store { return a.cfg }

// Run serves until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg := a.cfg.Get()

	go a.catalog.Run(ctx)
	go a.trakt.Start(ctx)
	if err := a.registrar.Start(ctx); err != nil {
		a.log.Warn("plex.tv registrar", "err", err)
	}
	if err := a.gdm.Start(ctx); err != nil {
		a.log.Warn("GDM discovery responder", "err", err)
	}
	go a.housekeeping(ctx)

	addr := net.JoinHostPort(cfg.Server.BindAddress, strconv.Itoa(cfg.Server.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	dl := newDualListener(ln, func() *tls.Config { return a.certs.TLSConfig() })
	srv := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelDebug),
	}
	a.log.Info("Ether listening", "addr", addr, "ui", fmt.Sprintf("http://localhost:%d/ether/", cfg.Server.Port),
		"version", a.opts.Version, "spike", cfg.Spike.Enabled)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(dl) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown)
	a.registrar.Stop()
	a.gdm.Stop()
	return nil
}

func (a *App) housekeeping(ctx context.Context) {
	pins := time.NewTicker(5 * time.Minute)
	defer pins.Stop()
	_ = a.playback.ReconcilePins(ctx)
	for {
		select {
		case <-ctx.Done():
			_ = a.users.FlushSeen(context.Background())
			return
		case <-pins.C:
			_ = a.playback.ReconcilePins(ctx)
			_ = a.users.FlushSeen(ctx)
		}
	}
}

// Close releases every component in reverse order of construction.
func (a *App) Close() {
	var once sync.Once
	once.Do(func() {
		for i := len(a.closers) - 1; i >= 0; i-- {
			a.closers[i]()
		}
	})
}

func appendUnique(xs []string, more ...string) []string {
	seen := map[string]bool{}
	for _, x := range xs {
		seen[x] = true
	}
	for _, m := range more {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			xs = append(xs, m)
			seen[m] = true
		}
	}
	return xs
}
