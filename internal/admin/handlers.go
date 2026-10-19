package admin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// ---- Setup status ----

// SetupStep is one wizard step's completion state.
type SetupStep struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Complete bool   `json:"complete"`
	Required bool   `json:"required"`
	Detail   string `json:"detail,omitempty"`
}

// SetupStatus derives wizard progress from the stored config.
func SetupStatus(cfg config.Config) (steps []SetupStep, complete bool) {
	plexDone := cfg.Plex.Claimed && cfg.Plex.Token != ""
	plexDetail := "not signed in"
	if plexDone {
		plexDetail = "claimed"
		if cfg.Plex.OwnerUsername != "" {
			plexDetail = "claimed by " + cfg.Plex.OwnerUsername
		}
	} else if cfg.Plex.Token != "" {
		plexDetail = "signed in, not claimed"
	}
	enabledProviders := 0
	for _, p := range cfg.Usenet.Providers {
		if p.Enabled {
			enabledProviders++
		}
	}
	enabledIndexers := 0
	for _, ix := range cfg.Indexers {
		if ix.Enabled {
			enabledIndexers++
		}
	}
	cacheDone := strings.TrimSpace(cfg.Cache.Dir) != "" && cfg.Cache.BudgetBytes > 0
	steps = []SetupStep{
		{ID: "plex", Title: "Sign in to plex.tv and claim", Complete: plexDone, Required: true, Detail: plexDetail},
		{ID: "usenet", Title: "Usenet providers", Complete: enabledProviders > 0, Required: true,
			Detail: fmt.Sprintf("%d enabled", enabledProviders)},
		{ID: "indexers", Title: "Newznab indexers", Complete: enabledIndexers > 0, Required: true,
			Detail: fmt.Sprintf("%d enabled", enabledIndexers)},
		{ID: "cache", Title: "Cache folder and disk budget", Complete: cacheDone, Required: true, Detail: cfg.Cache.Dir},
		{ID: "optional", Title: "Optional integrations", Complete: true, Required: false,
			Detail: "TMDB key, Trakt, OpenSubtitles, LLM, cam policy"},
	}
	complete = cfg.SetupComplete
	return steps, complete
}

func (h *Handler) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	steps, complete := SetupStatus(h.deps.Config.Get())
	requiredDone := true
	for _, s := range steps {
		if s.Required && !s.Complete {
			requiredDone = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setupComplete": complete, "requiredComplete": requiredDone, "steps": steps,
	})
}

func (h *Handler) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Force bool `json:"force"`
	}
	_ = decodeJSONLenient(r, &body) // empty body tolerated
	steps, _ := SetupStatus(h.deps.Config.Get())
	if !body.Force {
		for _, s := range steps {
			if s.Required && !s.Complete {
				writeError(w, http.StatusConflict, "setup_incomplete",
					"required step \""+s.ID+"\" is not complete (send {\"force\":true} to finish anyway)")
				return
			}
		}
	}
	if err := h.deps.Config.Update(func(c *config.Config) error { c.SetupComplete = true; return nil }); err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", "could not save configuration")
		return
	}
	h.log.Info("admin: setup marked complete", "forced", body.Force)
	h.handleSetupStatus(w, r)
}

func (h *Handler) handleSetupReopen(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Config.Update(func(c *config.Config) error { c.SetupComplete = false; return nil }); err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", "could not save configuration")
		return
	}
	h.handleSetupStatus(w, r)
}

// ---- Plex sign-in ----

func (h *Handler) handlePINStart(w http.ResponseWriter, r *http.Request) {
	if h.deps.PlexAuth == nil {
		writeNotImplemented(w, "plex.tv sign-in")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	id, code, authURL, err := h.deps.PlexAuth.StartPIN(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_pin_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "code": code, "authUrl": authURL})
}

func (h *Handler) handlePINPoll(w http.ResponseWriter, r *http.Request) {
	if h.deps.PlexAuth == nil {
		writeNotImplemented(w, "plex.tv sign-in")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	done, err := h.deps.PlexAuth.PollPIN(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_pin_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"done": done})
}

func (h *Handler) handleClaim(w http.ResponseWriter, r *http.Request) {
	if h.deps.PlexAuth == nil {
		writeNotImplemented(w, "plex.tv claim")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	if err := h.deps.PlexAuth.Claim(ctx); err != nil {
		writeError(w, http.StatusBadGateway, "plex_claim_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handlePlexStatus(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.Config.Get()
	out := map[string]any{
		"claimed":       cfg.Plex.Claimed,
		"signedIn":      cfg.Plex.Token != "",
		"ownerUsername": cfg.Plex.OwnerUsername,
		"ownerPlexId":   cfg.Plex.OwnerPlexID,
	}
	if h.deps.PlexAuth != nil {
		out["detail"] = h.safeStatus(r, func(ctx context.Context) any { return h.deps.PlexAuth.Status() })
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Testers ----

type testResult struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

func (h *Handler) runTest(w http.ResponseWriter, r *http.Request, name string, fn func(ctx context.Context) error) {
	if fn == nil {
		writeNotImplemented(w, name+" testing")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	start := time.Now()
	err := func() (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("tester panicked: %v", rec)
			}
		}()
		return fn(ctx)
	}()
	res := testResult{OK: err == nil, DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
	}
	// A failed test is a successful API call: 200 with ok:false so the
	// wizard can show the provider's error inline.
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleTestUsenet(w http.ResponseWriter, r *http.Request) {
	var p config.UsenetProvider
	if err := decodeJSONLenient(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	p = unredactProvider(p, h.deps.Config.Get())
	if errs := ValidateConfig(withProvider(p)); len(errs) > 0 {
		writeValidationError(w, errs)
		return
	}
	var fn func(ctx context.Context) error
	if h.deps.TestUsenet != nil {
		fn = func(ctx context.Context) error { return h.deps.TestUsenet(ctx, p) }
	}
	h.runTest(w, r, "usenet", fn)
}

func withProvider(p config.UsenetProvider) config.Config {
	c := config.Defaults(os.TempDir())
	c.Usenet.Providers = []config.UsenetProvider{p}
	return c
}

func (h *Handler) handleTestIndexer(w http.ResponseWriter, r *http.Request) {
	var ix config.Indexer
	if err := decodeJSONLenient(r, &ix); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	ix = unredactIndexer(ix, h.deps.Config.Get())
	var fn func(ctx context.Context) error
	if h.deps.TestIndexer != nil {
		fn = func(ctx context.Context) error { return h.deps.TestIndexer(ctx, ix) }
	}
	h.runTest(w, r, "indexer", fn)
}

func (h *Handler) handleTestDebrid(w http.ResponseWriter, r *http.Request) {
	var p config.DebridProvider
	if err := decodeJSONLenient(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	p = unredactDebridProvider(p, h.deps.Config.Get())
	c := config.Defaults(os.TempDir())
	c.Debrid.Providers = []config.DebridProvider{p}
	if errs := ValidateConfig(c); len(errs) > 0 {
		writeValidationError(w, errs)
		return
	}
	var fn func(ctx context.Context) error
	if h.deps.TestDebrid != nil {
		fn = func(ctx context.Context) error { return h.deps.TestDebrid(ctx, p) }
	}
	h.runTest(w, r, "debrid", fn)
}

func (h *Handler) handleTestTMDB(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	_ = decodeJSONLenient(r, &body)
	key := body.Key
	if isRedacted(key) || key == "" {
		key = h.deps.Config.Get().Catalog.TMDBKey
	}
	var fn func(ctx context.Context) error
	if h.deps.TestTMDB != nil {
		fn = func(ctx context.Context) error { return h.deps.TestTMDB(ctx, key) }
	}
	h.runTest(w, r, "tmdb", fn)
}

func (h *Handler) handleTestOpenSubtitles(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.Config.Get().OpenSubtitles
	_ = decodeJSONLenient(r, &cfg)
	prev := h.deps.Config.Get()
	cfg.APIKey = preserve(cfg.APIKey, prev.OpenSubtitles.APIKey)
	cfg.Password = preserve(cfg.Password, prev.OpenSubtitles.Password)
	var fn func(ctx context.Context) error
	if h.deps.TestOpenSubtitles != nil {
		fn = func(ctx context.Context) error { return h.deps.TestOpenSubtitles(ctx, cfg) }
	}
	h.runTest(w, r, "opensubtitles", fn)
}

func (h *Handler) handleTestLLM(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.Config.Get().LLM
	_ = decodeJSONLenient(r, &cfg)
	cfg.APIKey = preserve(cfg.APIKey, h.deps.Config.Get().LLM.APIKey)
	var fn func(ctx context.Context) error
	if h.deps.TestLLM != nil {
		fn = func(ctx context.Context) error { return h.deps.TestLLM(ctx, cfg) }
	}
	h.runTest(w, r, "llm", fn)
}

func (h *Handler) handleTestTrakt(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.Config.Get().Trakt
	_ = decodeJSONLenient(r, &cfg)
	cfg.ClientSecret = preserve(cfg.ClientSecret, h.deps.Config.Get().Trakt.ClientSecret)
	var fn func(ctx context.Context) error
	if h.deps.TestTrakt != nil {
		fn = func(ctx context.Context) error { return h.deps.TestTrakt(ctx, cfg) }
	}
	h.runTest(w, r, "trakt", fn)
}

// ---- Dashboard / status / traces ----

func (h *Handler) statusProviders() map[string]StatusFunc {
	out := map[string]StatusFunc{}
	add := func(name string, fn StatusFunc) {
		if fn != nil {
			out[name] = fn
		}
	}
	add("sessions", h.deps.SessionsStatus)
	add("cache", h.deps.CacheStatus)
	add("providers", h.deps.ProvidersStatus)
	add("indexers", h.deps.IndexersStatus)
	add("catalog", h.deps.CatalogStatus)
	add("remote", h.deps.RemoteStatus)
	add("hwaccel", h.deps.HWAccelStatus)
	add("updates", h.deps.UpdatesStatus)
	add("traces", h.deps.TracesStatus)
	for name, fn := range h.deps.ExtraStatus {
		add(name, fn)
	}
	return out
}

// safeStatus calls a provider with a timeout and panic recovery, in the
// request goroutine (providers are expected to be quick snapshots).
func (h *Handler) safeStatus(r *http.Request, fn StatusFunc) any {
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.StatusTimeout)
	defer cancel()
	var out any
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				h.log.Error("admin: status provider panicked", "panic", rec)
				out = map[string]any{"error": fmt.Sprintf("status provider failed: %v", rec)}
			}
		}()
		out = fn(ctx)
	}()
	if ctx.Err() == context.DeadlineExceeded {
		return map[string]any{"error": "status provider timed out"}
	}
	return out
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	providers := h.statusProviders()
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	sort.Strings(names)
	sections := map[string]any{}
	for _, n := range names {
		sections[n] = h.safeStatus(r, providers[n])
	}
	steps, complete := SetupStatus(h.deps.Config.Get())
	writeJSON(w, http.StatusOK, map[string]any{
		"sections": sections, "names": names,
		"setupComplete": complete, "setupSteps": steps, "version": h.version,
	})
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	fn, ok := h.statusProviders()[name]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_status", "no status provider named "+strconv.Quote(name))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "value": h.safeStatus(r, fn)})
}

func (h *Handler) handleTraces(w http.ResponseWriter, r *http.Request) {
	if h.deps.ResolverTraces != nil {
		ctx, cancel := context.WithTimeout(r.Context(), h.deps.StatusTimeout)
		defer cancel()
		v, err := h.deps.ResolverTraces(ctx)
		if err != nil {
			writeError(w, http.StatusBadGateway, "traces_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"traces": v})
		return
	}
	if h.deps.TracesStatus != nil {
		writeJSON(w, http.StatusOK, map[string]any{"traces": h.safeStatus(r, h.deps.TracesStatus)})
		return
	}
	writeNotImplemented(w, "resolver traces")
}

// ---- Disk ----

func (h *Handler) handleDisk(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = h.deps.Config.Get().Cache.Dir
	}
	// Statfs needs an existing path: walk up to the nearest existing parent.
	probe := path
	for probe != "" {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	usage := diskUsage
	if h.deps.DiskUsage != nil {
		usage = h.deps.DiskUsage
	}
	total, free, err := usage(probe)
	if err != nil {
		writeError(w, http.StatusBadRequest, "disk_failed", "could not read free space for "+path+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": path, "probedPath": probe,
		"totalBytes": total, "freeBytes": free, "usedBytes": total - free,
		"cacheBudgetBytes": h.deps.Config.Get().Cache.BudgetBytes,
	})
}

// ---- Users ----

func (h *Handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if h.deps.Users == nil {
		writeNotImplemented(w, "user administration")
		return
	}
	users, err := h.deps.Users.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "users_failed", err.Error())
		return
	}
	if users == nil {
		users = []core.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if h.deps.Users == nil {
		writeNotImplemented(w, "user administration")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", "user id must be a number")
		return
	}
	// Merge onto the stored user so PATCH-style partial bodies work and
	// identity fields the client omitted are not blanked.
	existing, err := h.deps.Users.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "users_failed", err.Error())
		return
	}
	var u *core.User
	for i := range existing {
		if existing[i].ID == id {
			uu := existing[i]
			u = &uu
			break
		}
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	if err := decodeJSONLenient(r, u); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	u.ID = id // path wins over body
	if u.StreamLimit < 0 {
		writeValidationError(w, []FieldError{{Field: "streamLimit", Message: "must be >= 0"}})
		return
	}
	if err := h.deps.Users.UpdateUser(r.Context(), *u); err != nil {
		writeError(w, http.StatusBadGateway, "users_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// ---- Logs ----

func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			if n > 5000 {
				n = 5000
			}
			limit = n
		}
	}
	entries := h.logs.Entries(limit, r.URL.Query().Get("level"), r.URL.Query().Get("q"))
	if entries == nil {
		entries = []LogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": h.logs.Len()})
}

func (h *Handler) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	h.logs.Clear()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
