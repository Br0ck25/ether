package plex

import (
	"bufio"
	"compress/gzip"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

const corsAllowHeaders = "Accept, Accept-Language, Content-Type, Range, X-Plex-Token, X-Plex-Client-Identifier, X-Plex-Product, " +
	"X-Plex-Version, X-Plex-Platform, X-Plex-Platform-Version, X-Plex-Device, X-Plex-Device-Name, X-Plex-Device-Vendor, " +
	"X-Plex-Device-Screen-Resolution, X-Plex-Model, X-Plex-Language, X-Plex-Features, X-Plex-Provides, X-Plex-Drm, " +
	"X-Plex-Container-Start, X-Plex-Container-Size, X-Plex-Client-Profile-Extra, X-Plex-Client-Profile-Name, " +
	"X-Plex-Client-Capabilities, X-Plex-Session-Identifier, X-Plex-Pms-Api-Version, X-Plex-Text-Format, X-Plex-Restriction-Profile, " +
	"X-Plex-Marketplace, X-Plex-Sync-Version, X-Plex-Http-Pipeline"

const corsExposeHeaders = "Content-Length, Content-Range, Content-Type, Date, Location, X-Plex-Protocol, " +
	"X-Plex-Container-Start, X-Plex-Container-Total-Size, X-Plex-Content-Original-Length, X-Plex-Content-Compressed-Length, X-Plex-Activity"

// commonHeaders sets X-Plex-Protocol and CORS on every response and answers
// preflights.
func (s *Server) commonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Plex-Protocol", "1.0")
		// Like PMS, CORS headers are only sent to cross-origin (browser)
		// requests.
		if origin := r.Header.Get("Origin"); origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		}
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, HEAD, OPTIONS")
			if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			} else {
				h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			}
			h.Set("Access-Control-Max-Age", "1209600")
			h.Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer turns handler panics into 500s.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("handler panic", "path", r.URL.Path, "panic", v)
				writeHTTPError(w, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.status == 0 {
		sr.status = code
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

// requestLog logs every request at debug level.
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)
		pr := FromContext(r.Context())
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sr.status,
			"dur", time.Since(start).Round(time.Millisecond), "product", pr.Product, "client", pr.ClientID,
			"pageStart", pr.Start, "pageSize", pr.Size, "paged", pr.HasPage, "format", pr.Format, "includeChildren", r.URL.Query().Get("includeChildren"))
	})
}

// parse attaches the parsed request context.
func (s *Server) parse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pr := ParseRequest(r)
		next.ServeHTTP(w, r.WithContext(withRequest(r.Context(), pr)))
	})
}

// publicPaths need no token.
var publicPaths = map[string]bool{
	"/identity": true,
}

// authenticate maps the token to a user. Requests without a valid token get
// PMS's 401 body.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPaths[trimSlash(r.URL.Path)] {
			next.ServeHTTP(w, r)
			return
		}
		pr := FromContext(r.Context())
		if pr.Token == "" && s.authenticateStreamGrant(r, pr) {
			next.ServeHTTP(w, r)
			return
		}
		if pr.Token == "" || s.d.Auth == nil {
			writeHTTPError(w, http.StatusUnauthorized)
			return
		}
		u, err := s.d.Auth.Authenticate(r.Context(), pr.Token)
		switch {
		case err == nil && u != nil:
			if policy, ok := s.d.AccessPolicy.(interface{ UserPolicy(*core.User) *core.User }); ok {
				u = policy.UserPolicy(u)
			}
			if id := transcodeSessionID(r.URL.Path); id != "" {
				e, ok := s.transcodes.get(id)
				if !ok {
					writeHTTPError(w, http.StatusNotFound)
					return
				}
				if e.userID != u.ID {
					writeHTTPError(w, http.StatusForbidden)
					return
				}
			}
			pr.User = u
			next.ServeHTTP(w, r)
		case err == nil, errors.Is(err, core.ErrUnauthorized), errors.Is(err, core.ErrNotFound):
			writeHTTPError(w, http.StatusUnauthorized)
		case errors.Is(err, r.Context().Err()):
			// client went away
		default:
			s.log.Warn("authentication failed", "err", err)
			writeHTTPError(w, http.StatusServiceUnavailable)
		}
	})
}

// ownerOnly restricts a handler to the server owner.
func (s *Server) ownerOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := FromContext(r.Context()).User; u == nil || !u.IsOwner {
			writeHTTPError(w, http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// writeHTTPError writes PMS's minimal HTML error body.
func writeHTTPError(w http.ResponseWriter, code int) {
	text := http.StatusText(code)
	body := "<html><head><title>" + text + "</title></head><body><h1>" + strconv.Itoa(code) + " " + text + "</h1></body></html>"
	h := w.Header()
	h.Set("Content-Type", "text/html")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Del("Content-Encoding")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// write encodes a response model in the client's format, gzip-compressed
// when the client accepts it and the body is worth compressing.
func (s *Server) write(w http.ResponseWriter, r *http.Request, v any) {
	s.writeStatus(w, r, http.StatusOK, v)
}

func (s *Server) writeStatus(w http.ResponseWriter, r *http.Request, status int, v any) {
	pr := FromContext(r.Context())
	// allowSync on library containers follows the viewer's download
	// permission; containers that don't carry the attribute keep not
	// carrying it.
	if mc, ok := v.(*model.MediaContainer); ok && mc.AllowSync != nil {
		*mc.AllowSync = s.downloadsAllowed(pr.User)
	}
	body, err := model.Marshal(v, pr.Format)
	if err != nil {
		s.log.Error("encode response", "path", r.URL.Path, "err", err)
		writeHTTPError(w, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", pr.Format.ContentType())
	h.Set("Cache-Control", "no-cache")
	if mc, ok := v.(*model.MediaContainer); ok && mc.Offset != nil {
		h.Set("X-Plex-Container-Start", strconv.Itoa(*mc.Offset))
		if mc.TotalSize != nil {
			h.Set("X-Plex-Container-Total-Size", strconv.Itoa(*mc.TotalSize))
		}
	}
	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		return
	}
	if len(body) > 1024 && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Set("X-Plex-Content-Original-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(body)
		_ = gz.Close()
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func acceptsGzip(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, p := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(p), ";")
			if strings.EqualFold(strings.TrimSpace(name), "gzip") && !strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0") {
				return true
			}
		}
	}
	return false
}

// container returns a MediaContainer with the attributes PMS puts on
// library responses.
func libraryContainer() *model.MediaContainer {
	return &model.MediaContainer{
		AllowSync:       model.Ptr(false),
		Identifier:      model.LibraryIdentifier,
		MediaTagPrefix:  model.MediaTagPrefix,
		MediaTagVersion: model.MediaTagVersion,
	}
}

// hubContainer is the container PMS uses for hub lists.
func hubContainer() *model.MediaContainer {
	return &model.MediaContainer{AllowSync: model.Ptr(false), Identifier: model.LibraryIdentifier}
}

// writeEmpty writes an empty MediaContainer.
func (s *Server) writeEmpty(w http.ResponseWriter, r *http.Request) {
	s.write(w, r, &model.MediaContainer{})
}

// writeErr maps a component error to an HTTP status.
func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		writeHTTPError(w, http.StatusNotFound)
	case errors.Is(err, core.ErrUnauthorized):
		writeHTTPError(w, http.StatusUnauthorized)
	case errors.Is(err, core.ErrStreamLimit):
		writeHTTPError(w, http.StatusServiceUnavailable)
	case errors.Is(err, core.ErrNotAvailable):
		writeHTTPError(w, http.StatusNotFound)
	case errors.Is(err, r.Context().Err()):
		// client gone; nothing useful to write
	default:
		s.log.Warn("request failed", "path", r.URL.Path, "err", err)
		writeHTTPError(w, http.StatusInternalServerError)
	}
}
