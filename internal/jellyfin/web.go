package jellyfin

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
		if p == "/web/" || strings.EqualFold(p, "/web/index.html") {
			s.serveIndex(w, r)
			return true
		}
		http.FileServer(http.Dir(s.d.WebDir)).ServeHTTP(w, r2)
		return true
	}
	return false
}

// playbackPath reports requests that start, feed or end a playback.
var playbackPath = regexp.MustCompile(`(?i)/(playbackinfo|videos/|livetv/channels/|sessions/playing|livestreams/)`)

// statusLogger logs every 404 an app gets, with the app's user agent, so an
// app that cannot connect shows exactly what it asked for. Playback requests
// are logged with their outcome whatever it is, since an app that fails to
// play often gives no other sign.
type statusLogger struct {
	http.ResponseWriter
	r      *http.Request
	log    *slog.Logger
	start  time.Time
	status int
	bytes  int64
	logged bool
}

func (w *statusLogger) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	if code == http.StatusNotFound && !w.logged {
		w.logged = true
		w.log.Info("jellyfin: not found", "method", w.r.Method, "path", w.r.URL.Path, "agent", w.r.UserAgent())
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusLogger) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// finish logs a playback request once it has been served.
func (w *statusLogger) finish() {
	if w.status == http.StatusNotFound || !playbackPath.MatchString(w.r.URL.Path) {
		return
	}
	// Segments arrive by the hundred; log the playlists and everything else.
	if p := strings.ToLower(w.r.URL.Path); strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".m4s") || strings.HasSuffix(p, ".mp4") && strings.Contains(p, "/hls/") {
		if w.status < 400 {
			return
		}
	}
	w.log.Info("jellyfin: playback request", "method", w.r.Method, "path", w.r.URL.Path, "status", w.status,
		"bytes", w.bytes, "took", time.Since(w.start).Round(time.Millisecond), "range", w.r.Header.Get("Range"), "agent", w.r.UserAgent())
}

func (w *statusLogger) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusLogger) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// serveIndex serves the web client's page with Ether's Home rows script added.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	page, err := os.ReadFile(filepath.Join(s.d.WebDir, "index.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	html := string(page)
	const tag = `<script src="/ether/home.js" defer></script>`
	if i := strings.LastIndex(html, "</body>"); i >= 0 {
		html = html[:i] + tag + html[i:]
	} else {
		html += tag
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(html))
	}
}
