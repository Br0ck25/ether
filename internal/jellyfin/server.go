package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/markers"
)

// Versions the server reports. Apps gate features on them, so they must
// look like current releases of the server each app expects.
const (
	jellyfinVersion = "10.10.7"
	embyVersion     = "4.8.10.0"
)

// AccessPolicy adds owner-defined restrictions to the mandatory title guard.
type AccessPolicy interface {
	Allowed(*core.User, *core.Title) bool
}

// MarkerProvider supplies intro and credits markers for Skip buttons.
type MarkerProvider interface {
	Markers(context.Context, core.RatingKey, core.MediaInfo) ([]markers.Marker, error)
}

// SubtitleSource opens sidecar subtitles ("ether-subtitle:" references).
type SubtitleSource interface {
	OpenFormat(string, string) (io.ReadCloser, string, error)
}

// Deps are the server's dependencies. Catalog, State, Auth, Playback and
// Store are required; the rest degrade gracefully when nil.
type Deps struct {
	Catalog      core.Catalog
	Collections  core.Collections
	State        core.State
	Playlists    core.Playlists
	Auth         core.Auth
	Playback     core.Playback
	Transcoder   core.Transcoder
	Markers      MarkerProvider
	Subtitles    SubtitleSource
	AccessPolicy AccessPolicy
	// LiveTV lists the owner's virtual channels as Live TV. Optional.
	LiveTV LiveTV
	// FilterAvailable drops titles the viewer cannot play yet.
	FilterAvailable func(context.Context, []core.Title) []core.Title
	// OnTimeline is told of every playback progress report, as the Plex
	// front end does, so prefetch follows the viewer.
	OnTimeline func(user *core.User, key core.RatingKey, offset, duration time.Duration, state string)

	Store    *Store
	ImageURL core.ImageURLFunc
	Config   func() config.Config
	Logger   *slog.Logger
	HTTP     *http.Client
	// ServerID and ServerName identify this server to apps.
	ServerID   string
	ServerName string
	Now        func() time.Time
	// WebDir holds the Jellyfin web client (index.html and its assets). The
	// official Jellyfin mobile apps load it from the server, so when it is
	// present it is served under /web and the root redirects there.
	WebDir string
}

// Server is the Jellyfin/Emby API handler.
type Server struct {
	d        Deps
	log      *slog.Logger
	now      func() time.Time
	serverID string
	name     string
	router   chi.Router
	throttle *throttle

	live liveState

	mu        sync.Mutex
	plays     map[string]*play // by play session id
	capsByDev map[string]core.ClientCaps
}

// NewServer builds the handler.
func NewServer(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	s := &Server{d: d, log: d.Logger, now: d.Now, serverID: d.ServerID, name: d.ServerName,
		throttle:  newThrottle(d.Now),
		plays:     map[string]*play{},
		capsByDev: map[string]core.ClientCaps{}}
	if s.serverID == "" {
		s.serverID = "ether"
	}
	s.serverID = strings.ReplaceAll(strings.ToLower(s.serverID), "-", "")
	if s.name == "" {
		s.name = "Ether"
	}
	s.router = s.routes()
	return s
}

type dialectKey struct{}

func isEmby(ctx context.Context) bool { v, _ := ctx.Value(dialectKey{}).(bool); return v }

// ServeHTTP normalizes the request the way Jellyfin does: an Emby app's
// /emby prefix is dropped (and remembered, for the links in responses),
// and paths match case-insensitively. Item ids, which are the only
// case-sensitive part, are lower-case hex or decimal.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = &statusLogger{ResponseWriter: w, r: r, log: s.log}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Headers", "*, Authorization, Content-Type, X-Emby-Authorization, X-Emby-Token, X-MediaBrowser-Token")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS, HEAD")
	h.Set("Access-Control-Expose-Headers", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.serveWeb(w, r) {
		return
	}
	path := strings.ToLower(r.URL.Path)
	emby := false
	if rest, ok := strings.CutPrefix(path, "/emby/"); ok {
		path, emby = "/"+rest, true
	} else if path == "/emby" {
		path, emby = "/", true
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
	}
	r2 := r.Clone(context.WithValue(r.Context(), dialectKey{}, emby))
	r2.URL.Path, r2.URL.RawPath = path, ""
	s.router.ServeHTTP(w, r2)
}

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	open := func(pattern string, fn http.HandlerFunc, methods ...string) {
		for _, m := range methods {
			r.MethodFunc(m, pattern, fn)
		}
	}
	both := []string{http.MethodGet, http.MethodHead}
	// Unauthenticated: server discovery, sign-in, artwork.
	open("/system/info/public", s.handlePublicInfo, both...)
	open("/", s.handleLanding, both...)
	open("/web", s.handleLanding, both...)
	open("/health", s.handleHealth, both...)
	open("/system/ping", s.handlePing, http.MethodGet, http.MethodPost, http.MethodHead)
	open("/users/public", s.handleUsersPublic, both...)
	open("/users/authenticatebyname", s.handleAuthByName, http.MethodPost)
	open("/quickconnect/enabled", s.handleQuickConnectEnabled, both...)
	open("/branding/configuration", s.handleBranding, both...)
	open("/branding/css", s.handleEmptyText, both...)
	open("/branding/css.css", s.handleEmptyText, both...)
	open("/items/{id}/images/{type}", s.handleImage, both...)
	open("/items/{id}/images/{type}/{index}", s.handleImage, both...)
	open("/users/{uid}/images/{type}", s.handleNoImage, both...)
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)
		s.protected(r)
	})
	r.NotFound(http.NotFound)
	return r
}

func (s *Server) protected(r chi.Router) {
	// System and users.
	r.Get("/system/info", s.handleSystemInfo)
	r.Get("/system/endpoint", s.handleEndpoint)
	r.Get("/users", s.handleUsers)
	r.Get("/users/me", s.handleMe)
	r.Get("/users/{uid}", s.handleUser)
	r.Post("/sessions/logout", s.handleLogout)
	r.Post("/sessions/capabilities", s.handleCapabilities)
	r.Post("/sessions/capabilities/full", s.handleCapabilities)
	r.Get("/sessions", s.handleSessionsList)
	r.Get("/displaypreferences/{id}", s.handleDisplayPrefs)
	r.Post("/displaypreferences/{id}", s.handleOK)
	r.Get("/system/configuration", s.handleServerConfig)
	r.Get("/playback/bitratetest", s.handleBitrateTest)

	// Libraries.
	r.Get("/users/{uid}/views", s.handleViews)
	r.Get("/userviews", s.handleViews)
	r.Get("/library/mediafolders", s.handleViews)
	r.Get("/users/{uid}/items", s.handleItems)
	r.Get("/items", s.handleItems)
	r.Get("/users/{uid}/items/latest", s.handleLatest)
	r.Get("/items/latest", s.handleLatest)
	r.Get("/useritems/latest", s.handleLatest)
	r.Get("/users/{uid}/items/resume", s.handleResume)
	r.Get("/useritems/resume", s.handleResume)
	r.Get("/items/resume", s.handleResume)
	r.Get("/users/{uid}/items/{id}", s.handleItem)
	r.Get("/items/{id}", s.handleItem)
	r.Get("/useritems/{id}/userdata", s.handleGetUserData)
	r.Get("/users/{uid}/items/{id}/userdata", s.handleGetUserData)
	r.Get("/items/{id}/similar", s.handleSimilar)
	r.Get("/movies/{id}/similar", s.handleSimilar)
	r.Get("/shows/{id}/similar", s.handleSimilar)
	r.Get("/items/{id}/ancestors", s.handleAncestors)
	r.Get("/items/{id}/specialfeatures", s.handleEmptyArray)
	r.Get("/items/{id}/localtrailers", s.handleEmptyArray)
	r.Get("/items/{id}/intros", s.handleEmptyItems)
	r.Get("/users/{uid}/items/{id}/specialfeatures", s.handleEmptyArray)
	r.Get("/users/{uid}/items/{id}/localtrailers", s.handleEmptyArray)
	r.Get("/users/{uid}/items/{id}/intros", s.handleEmptyItems)
	r.Get("/items/{id}/thememedia", s.handleThemeMedia)
	r.Get("/items/{id}/themesongs", s.handleThemeSongs)
	r.Get("/items/{id}/themevideos", s.handleThemeSongs)
	r.Get("/items/filters", s.handleFilters)
	r.Get("/items/filters2", s.handleFilters2)
	r.Get("/shows/{id}/seasons", s.handleSeasons)
	r.Get("/shows/{id}/episodes", s.handleEpisodes)
	r.Get("/shows/nextup", s.handleNextUp)
	r.Get("/genres", s.handleGenres)
	r.Get("/movies/recommendations", s.handleEmptyArray)
	r.Get("/search/hints", s.handleSearchHints)
	r.Get("/persons", s.handleEmptyItems)
	r.Get("/studios", s.handleEmptyItems)
	r.Get("/years", s.handleEmptyItems)
	r.Get("/musicgenres", s.handleEmptyItems)
	r.Get("/artists", s.handleEmptyItems)
	r.Get("/channels", s.handleEmptyItems)
	r.Get("/livetv/programs/recommended", s.handleRecommendedPrograms)
	r.Get("/livetv/info", s.handleLiveTVInfo)
	r.Get("/livetv/guideinfo", s.handleGuideInfo)
	r.Get("/livetv/channels", s.handleLiveChannels)
	r.Get("/livetv/channels/{id}", s.handleLiveChannel)
	r.Get("/livetv/channels/{id}/master.m3u8", s.handleLiveMaster)
	r.Get("/livetv/channels/{id}/main.m3u8", s.handleLiveMaster)
	r.Get("/livetv/channels/{id}/program/{start}/{seg}", s.handleLiveSegment)
	r.Get("/livetv/programs", s.handlePrograms)
	r.Post("/livetv/programs", s.handlePrograms)
	r.Get("/livetv/programs/{id}", s.handleProgram)
	for _, p := range []string{"/livetv/recordings", "/livetv/recordings/folders", "/livetv/recordings/groups", "/livetv/timers", "/livetv/seriestimers"} {
		r.Get(p, s.handleEmptyLive)
	}
	r.Get("/collections", s.handleEmptyItems)
	r.Get("/playlists", s.handleEmptyItems)
	r.Get("/sync/items/ready", s.handleEmptyArray)
	r.Get("/mediasegments/{id}", s.handleMediaSegments)

	// Watch state.
	r.Post("/users/{uid}/playeditems/{id}", s.handleMarkPlayed)
	r.Post("/useritems/{id}/played", s.handleMarkPlayed)
	r.Post("/playeditems/{id}", s.handleMarkPlayed)
	r.Delete("/users/{uid}/playeditems/{id}", s.handleMarkUnplayed)
	r.Delete("/playeditems/{id}", s.handleMarkUnplayed)
	r.Post("/users/{uid}/favoriteitems/{id}", s.handleFavorite)
	r.Post("/useritems/{id}/favorite", s.handleFavorite)
	r.Delete("/users/{uid}/favoriteitems/{id}", s.handleUnfavorite)
	r.Delete("/useritems/{id}/favorite", s.handleUnfavorite)
	r.Post("/users/{uid}/items/{id}/rating", s.handleRate)
	r.Post("/useritems/{id}/rating", s.handleRate)
	r.Delete("/users/{uid}/items/{id}/rating", s.handleUnrate)

	// Playback.
	r.Get("/items/{id}/playbackinfo", s.handlePlaybackInfo)
	r.Post("/items/{id}/playbackinfo", s.handlePlaybackInfo)
	r.Post("/livestreams/open", s.handleOpenLiveStream)
	r.Post("/livestreams/close", s.handleOK)
	r.Get("/videos/{id}/stream", s.handleStream)
	r.Head("/videos/{id}/stream", s.handleStream)
	r.Get("/videos/{id}/stream.{ext}", s.handleStream)
	r.Head("/videos/{id}/stream.{ext}", s.handleStream)
	r.Get("/videos/{id}/original.{ext}", s.handleStream)
	r.Get("/items/{id}/download", s.handleStream)
	r.Get("/videos/{id}/master.m3u8", s.handleMaster)
	r.Get("/videos/{id}/main.m3u8", s.handleMaster)
	r.Get("/videos/{id}/hls/{sid}/*", s.handleHLS)
	r.Get("/videos/{id}/{source}/subtitles/{index}/stream.{ext}", s.handleSubtitle)
	r.Get("/videos/{id}/{source}/subtitles/{index}/{start}/stream.{ext}", s.handleSubtitle)
	r.Delete("/videos/activeencodings", s.handleStopEncoding)
	r.Post("/sessions/playing", s.handlePlaying)
	r.Post("/sessions/playing/progress", s.handlePlayingProgress)
	r.Post("/sessions/playing/stopped", s.handlePlayingStopped)
	r.Post("/sessions/playing/ping", s.handlePlayingPing)
}

// ---------------------------------------------------------------------------
// Authentication middleware

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sess := s.sessionFor(r); sess != nil {
			next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
			return
		}
		w.Header().Set("X-Application-Error-Code", "Unauthorized")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"Message": "Invalid token"})
	})
}

// sessionFor resolves the request's token to a signed-in viewer, or nil.
func (s *Server) sessionFor(r *http.Request) *session {
	p := queryParams(r)
	a := parseAuth(r, p)
	if a.Token == "" {
		return nil
	}
	row, ok := s.d.Store.lookup(r.Context(), a.Token)
	if !ok {
		return nil
	}
	u := s.activeUser(r.Context(), row.UserID)
	if u == nil {
		return nil
	}
	ip := clientIP(r)
	sess := &session{User: u, Token: a.Token, IP: ip, Remote: !isLocal(ip),
		DeviceID: firstNonEmpty(a.DeviceID, row.DeviceID), Device: firstNonEmpty(a.Device, row.Device),
		Client: firstNonEmpty(a.Client, row.Client), Version: firstNonEmpty(a.Version, row.Version)}
	if sess.DeviceID == "" {
		// An app that names no device is told apart by its token.
		sess.DeviceID = "token-" + tokenHash(a.Token)[:12]
	}
	if policy, ok := s.d.AccessPolicy.(core.UserPolicySource); ok {
		sess.User = policy.UserPolicy(u)
	}
	return sess
}

// optionalSession is for artwork, which apps may request without a token.
func (s *Server) optionalSession(r *http.Request) *session { return s.sessionFor(r) }

func (s *Server) activeUser(ctx context.Context, id int64) *core.User {
	if a, ok := s.d.Auth.(core.ActiveUserSource); ok {
		u, err := a.ActiveUser(ctx, id)
		if err != nil {
			return nil
		}
		return u
	}
	users, err := s.d.Auth.Users(ctx)
	if err != nil {
		return nil
	}
	for i := range users {
		if users[i].ID == id {
			return &users[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Visibility

func (s *Server) titleAllowed(u *core.User, t *core.Title) bool {
	if t == nil || t.Adult {
		return false
	}
	if u != nil && catalog.RatingExceeds(t.ContentRating, u.MaxContentRating) {
		return false
	}
	return s.d.AccessPolicy == nil || s.d.AccessPolicy.Allowed(u, t)
}

// canSee applies the section limits the owner set for a viewer. Sections
// are 1 = Movies and 2 = TV Shows, as in the Plex front end.
func (s *Server) canSee(u *core.User, t core.MediaType) bool {
	if u == nil || u.IsOwner || len(u.AllowedSections) == 0 {
		return true
	}
	want := 1
	if t == core.TypeShow || t == core.TypeSeason || t == core.TypeEpisode {
		want = 2
	}
	for _, id := range u.AllowedSections {
		if id == want {
			return true
		}
	}
	return false
}

func (s *Server) titleVisible(u *core.User, t *core.Title) bool {
	return s.titleAllowed(u, t) && s.canSee(u, t.Type)
}

func (s *Server) visible(ctx context.Context, u *core.User, ts []core.Title) []core.Title {
	out := make([]core.Title, 0, len(ts))
	for i := range ts {
		if s.titleVisible(u, &ts[i]) {
			out = append(out, ts[i])
		}
	}
	if s.d.FilterAvailable != nil && len(out) > 0 {
		out = s.d.FilterAvailable(ctx, out)
	}
	return out
}

// visibleOne is the check for a single title opened by id; children also
// inherit their show's policy.
func (s *Server) visibleOne(ctx context.Context, u *core.User, t *core.Title) bool {
	if !s.titleVisible(u, t) {
		return false
	}
	if t.ShowKey != 0 {
		show, err := s.d.Catalog.Get(ctx, t.ShowKey)
		if err != nil || !s.titleVisible(u, show) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Responses

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleOK(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleNoImage(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (s *Server) handleEmptyText(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css")
}
func (s *Server) handleEmptyArray(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}
func (s *Server) handleEmptyItems(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, core.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, core.ErrUnauthorized):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		s.log.Warn("jellyfin request failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
