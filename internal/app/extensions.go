package app

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// extensionHandlers keeps optional owner features behind the same LAN, token
// and CSRF boundaries as the existing admin API.
func (a *App) extensionHandlers() map[string]http.Handler {
	m := map[string]http.Handler{"safety/policies": a.safety.Handler()}
	clean := a.cleanHome
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", clean.read)
	mux.HandleFunc("POST /", clean.apply)
	m["safety/clean-home"] = mux
	m["stats"] = a.stats.Handler()
	m["notifications"] = a.notifications.Handler()
	m["livetv"] = a.livetv.Handler()
	m["safety/diagnostics"] = a.diagnosticsHandler()
	m["playback"] = a.playbackPolicyHandler()
	m["stream-preferences"] = a.trackprefs.Handler()
	m["markers"] = a.markers.Handler()
	m["subtitle-sync"] = a.subtitles.SyncHandler()
	m["metadata"] = a.episodeorder.Router()
	m["updates"] = a.updatesHandler()
	a.discoveryHandlers(m)
	adoptionHandlers(a, m)
	return m
}

type cleanHomeStatus struct {
	app     *App
	mu      sync.Mutex
	last    time.Time
	err     string
	sources any
}

func (c *cleanHomeStatus) read(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"enabled": !c.app.cfg.Get().Plex.DisableCleanHome, "lastApplied": c.last, "error": c.err, "sources": c.sources, "scope": "Owner and managed Plex Home accounts; friends require their own account authorization"})
}
func (c *cleanHomeStatus) apply(w http.ResponseWriter, r *http.Request) {
	c.run(r.Context())
	c.read(w, r)
}
func (c *cleanHomeStatus) due() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.last) > 6*time.Hour
}
func (c *cleanHomeStatus) run(ctx context.Context) {
	cfg := c.app.cfg.Get()
	if cfg.Plex.DisableCleanHome || cfg.Plex.Token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sources, err := c.app.plextv.CleanHome(ctx, cfg.Plex.Token)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sources = sources
	c.last = time.Now()
	c.err = ""
	if err != nil {
		c.err = err.Error()
	}
}
