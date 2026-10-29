package jellyfin

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// hasWeb reports whether a Jellyfin web client is installed in WebDir.
func (s *Server) hasWeb() bool {
	if s.d.WebDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(s.d.WebDir, "index.html"))
	return err == nil
}

// serveWeb serves the Jellyfin web client under /web, and sends a bare
// address there, the way Jellyfin does. The official Jellyfin mobile apps
// are wrappers that load this client from the server. File names in the
// client are case-sensitive, so this runs before ServeHTTP lowercases paths.
func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) bool {
	if !s.hasWeb() || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	p := r.URL.Path
	switch {
	case p == "/" || strings.EqualFold(p, "/index.html"):
		http.Redirect(w, r, "/web/", http.StatusFound)
		return true
	case strings.EqualFold(p, "/web"):
		http.Redirect(w, r, "/web/", http.StatusFound)
		return true
	case len(p) >= 5 && strings.EqualFold(p[:5], "/web/"):
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = p[4:], ""
		// The page and its service worker must not stay cached across
		// Ether updates; the other files are content-hashed.
		if base := filepath.Base(p); base == "index.html" || base == "serviceworker.js" || p == "/web/" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.FileServer(http.Dir(s.d.WebDir)).ServeHTTP(w, r2)
		return true
	}
	return false
}

// statusLogger logs every 404 an app gets, with the app's user agent, so an
// app that cannot connect shows exactly what it asked for.
type statusLogger struct {
	http.ResponseWriter
	r      *http.Request
	log    *slog.Logger
	logged bool
}

func (w *statusLogger) WriteHeader(code int) {
	if code == http.StatusNotFound && !w.logged {
		w.logged = true
		w.log.Info("jellyfin: not found", "method", w.r.Method, "path", w.r.URL.Path, "agent", w.r.UserAgent())
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusLogger) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusLogger) Unwrap() http.ResponseWriter { return w.ResponseWriter }
