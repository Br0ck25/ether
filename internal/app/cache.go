package app

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/segcache"
)

func (a *App) configureCache(c config.Cache) {
	a.cache.SetPolicy(segcache.Policy{Mode: c.Mode, Retention: time.Duration(c.RetentionDays) * 24 * time.Hour, ReplayBoost: time.Duration(c.ReplayBoostHours) * time.Hour})
	a.cache.SetBudget(c.BudgetBytes)
}

// This extension is mounted behind the owner API's authentication and CSRF guard.
func (a *App) cacheHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		used, budget := a.cache.Usage()
		releases := a.cache.Releases()
		protected := a.protectedReleases()
		var pinnedBytes int64
		for _, item := range releases {
			if len(item.Pins) > 0 || protected[item.ReleaseID] {
				pinnedBytes += item.Bytes
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"used": used, "budget": budget, "directory": a.cache.Directory(), "protectedBytes": pinnedBytes, "releases": releases, "metrics": a.cache.Metrics(), "prefetch": a.prefetch.Stats(), "preload": a.cachefill.Status()})
	})
	mux.HandleFunc("POST /warm", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.cfg.Get().Cache
		if cfg.DisableSpeculation || cfg.Mode == "off" || !cfg.KeepInProgressWarm {
			http.Error(w, "Enable resume warming first", http.StatusConflict)
			return
		}
		a.prefetch.WarmNow()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /pin", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReleaseID string `json:"releaseId"`
			Pinned    bool   `json:"pinned"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || body.ReleaseID == "" {
			http.Error(w, "Invalid release", http.StatusBadRequest)
			return
		}
		found := false
		for _, item := range a.cache.Releases() {
			if item.ReleaseID == body.ReleaseID {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "Release no longer cached", http.StatusNotFound)
			return
		}
		var err error
		if body.Pinned {
			err = a.cache.Pin(body.ReleaseID, "owner")
		} else {
			err = a.cache.Unpin(body.ReleaseID, "owner")
		}
		if err != nil {
			http.Error(w, "Could not save pin", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /preload", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paused bool `json:"paused"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		if err := a.cachefill.Pause(body.Paused); err != nil {
			http.Error(w, "Could not save preload pause", 500)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
