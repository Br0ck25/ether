package overlays

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
	"strings"
)

// Router must be owner authenticated. Previews pass through the same generator
// as Plex; the caller supplies real catalog titles, never arbitrary image URLs.
func (s *Service) Router(cat core.Catalog, users func(*http.Request, int64) (*core.User, error)) http.Handler {
	r := chi.NewRouter()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	r.Get("/settings", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Settings(r.Context())
		if e != nil {
			http.Error(w, "Overlay settings unavailable", 500)
			return
		}
		reply(w, v)
	})
	r.Put("/settings", func(w http.ResponseWriter, r *http.Request) {
		var v Settings
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&v) != nil {
			http.Error(w, "Invalid settings", 400)
			return
		}
		v = normalizeSettings(v)
		if e := validateSettings(v); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if s.SaveSettings(r.Context(), v) != nil {
			http.Error(w, "Settings could not be saved", 500)
			return
		}
		reply(w, v)
	})
	r.Get("/sample", func(w http.ResponseWriter, r *http.Request) {
		cfg := Settings{Enabled: true, Status: true, Quality: true, Ratings: true}
		if raw := r.URL.Query().Get("settings"); raw != "" {
			cfg = Settings{}
			if len(raw) > 2048 || json.Unmarshal([]byte(raw), &cfg) != nil {
				http.Error(w, "Invalid preview settings", 400)
				return
			}
		}
		cfg = normalizeSettings(cfg)
		if e := validateSettings(cfg); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		data, ct, e := s.SampleContext(r.Context(), cfg)
		if e != nil {
			http.Error(w, "Sample unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = w.Write(data)
	})
	r.Get("/artwork/{key}", func(w http.ResponseWriter, r *http.Request) {
		k, e := core.ParseRatingKey(chi.URLParam(r, "key"))
		if e != nil {
			http.Error(w, "Invalid title", 400)
			return
		}
		v, e := s.Artwork(r.Context(), k)
		if e != nil {
			http.Error(w, "Artwork unavailable", 500)
			return
		}
		reply(w, v)
	})
	r.Put("/artwork/{key}", func(w http.ResponseWriter, r *http.Request) {
		k, e := core.ParseRatingKey(chi.URLParam(r, "key"))
		if e != nil {
			http.Error(w, "Invalid title", 400)
			return
		}
		var v Artwork
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&v) != nil {
			http.Error(w, "Invalid artwork", 400)
			return
		}
		if e = s.SaveArtwork(r.Context(), k, v); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		reply(w, v)
	})
	r.Get("/variants/{key}", func(w http.ResponseWriter, r *http.Request) {
		if cat == nil || s.o.Variants == nil {
			http.Error(w, "Artwork variants unavailable", 503)
			return
		}
		k, e := core.ParseRatingKey(chi.URLParam(r, "key"))
		if e != nil {
			http.Error(w, "Invalid title", 400)
			return
		}
		t, e := cat.Get(r.Context(), k)
		if e != nil || t == nil || t.Adult {
			http.Error(w, "Title unavailable", 404)
			return
		}
		v, e := s.o.Variants(r.Context(), t, r.URL.Query().Get("lang"))
		if e != nil {
			http.Error(w, "Artwork variants unavailable", 502)
			return
		}
		reply(w, v)
	})
	preview := func(w http.ResponseWriter, r *http.Request) {
		if cat == nil {
			http.Error(w, "Catalog unavailable", 503)
			return
		}
		k, e := core.ParseRatingKey(chi.URLParam(r, "key"))
		if e != nil {
			http.Error(w, "Invalid title", 400)
			return
		}
		t, e := cat.Get(r.Context(), k)
		if e != nil || t == nil || t.Adult {
			http.Error(w, "Title unavailable", 404)
			return
		}
		var u *core.User
		if users != nil {
			id, _ := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
			u, e = users(r, id)
			if e != nil {
				http.Error(w, "Viewer unavailable", 404)
				return
			}
		}
		if !s.visible(u, t) {
			http.Error(w, "Title unavailable for viewer", 404)
			return
		}
		ctx := r.Context()
		if raw := r.URL.Query().Get("settings"); raw != "" {
			var cfg Settings
			if len(raw) > 2048 || json.Unmarshal([]byte(raw), &cfg) != nil {
				http.Error(w, "Invalid preview settings", 400)
				return
			}
			cfg = normalizeSettings(cfg)
			if e := validateSettings(cfg); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			ctx = context.WithValue(ctx, previewSettingsKey{}, cfg)
		}
		if strings.HasPrefix(r.URL.Path, "/explain/") || strings.Contains(r.URL.Path, "/overlays/explain/") {
			report, err := s.Inspect(ctx, u, t)
			if err != nil {
				http.Error(w, "Preview data unavailable", 500)
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			reply(w, report)
			return
		}
		data, ct, e := s.Poster(ctx, u, t, r.URL.Query().Get("lang"))
		if e != nil {
			http.Error(w, "Preview unavailable", 502)
			return
		}
		etag := fmt.Sprintf("\"%x\"", sha256.Sum256(data))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
		w.Header().Set("Vary", "X-Plex-Token")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(data)
	}
	r.Get("/preview/{key}", preview)
	r.Get("/explain/{key}", preview)
	r.Get("/cache", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		reply(w, s.CacheStats())
	})
	return r
}
