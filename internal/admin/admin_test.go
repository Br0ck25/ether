package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

const ownerToken = "owner-secret-token"

func newStore(t *testing.T, mutate func(*config.Config)) *config.Store {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		if err := store.Update(func(c *config.Config) error { mutate(c); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func seededConfig(c *config.Config) {
	c.Plex.Token = ownerToken
	c.Plex.Claimed = true
	c.Plex.OwnerUsername = "alice"
	c.Usenet.Providers = []config.UsenetProvider{{
		ID: "p1", Name: "Main", Host: "news.example.com", Port: 563, TLS: true,
		Username: "u1", Password: "pw-one", Connections: 20, Enabled: true,
	}, {
		ID: "p2", Name: "Block", Host: "block.example.com", Port: 563, TLS: true,
		Username: "u2", Password: "pw-two", Connections: 5, Backfill: true, Enabled: true,
	}}
	c.Indexers = []config.Indexer{{ID: "i1", Name: "Idx", URL: "https://idx.example.com", APIKey: "ix-key", Enabled: true}}
	c.Catalog.TMDBKey = "tmdb-key"
	c.LLM.APIKey = "llm-key"
	c.OpenSubtitles.APIKey = "os-key"
	c.OpenSubtitles.Password = "os-pw"
	c.Trakt.ClientSecret = "trakt-secret"
}

// storedOwner is an IsOwnerToken that accepts the owner token stored in
// config, standing in for internal/auth.
func storedOwner(store *config.Store) func(context.Context, string) bool {
	return func(_ context.Context, tok string) bool {
		stored := store.Get().Plex.Token
		return stored != "" && tok == stored
	}
}

// req builds a request as the SPA would: LAN peer, CSRF header, JSON.
func req(method, target, body string) *http.Request {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rdr)
	r.RemoteAddr = "192.168.1.50:49152"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Ether-Request", "1")
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestRedactionRoundTrip(t *testing.T) {
	store := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = true })
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})

	r := req(http.MethodGet, "/ether/api/config", "")
	r.Header.Set("X-Plex-Token", ownerToken)
	rec := serve(h, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{ownerToken, "pw-one", "pw-two", "ix-key", "tmdb-key", "llm-key", "os-key", "os-pw", "trakt-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret %q leaked in GET config", secret)
		}
	}
	if !strings.Contains(body, admin.RedactedSecret) {
		t.Fatal("expected redaction placeholder in GET config")
	}

	// Send it back verbatim (envelope + placeholders), providers reordered.
	var env struct {
		Config config.Config `json:"config"`
	}
	decode(t, rec, &env)
	env.Config.Usenet.Providers[0], env.Config.Usenet.Providers[1] = env.Config.Usenet.Providers[1], env.Config.Usenet.Providers[0]
	env.Config.Server.FriendlyName = "Living Room"
	raw, _ := json.Marshal(env)
	r2 := req(http.MethodPut, "/ether/api/config", string(raw))
	r2.Header.Set("X-Plex-Token", ownerToken)
	rec2 := serve(h, r2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("PUT config: %d %s", rec2.Code, rec2.Body)
	}
	got := store.Get()
	if got.Server.FriendlyName != "Living Room" {
		t.Fatalf("friendlyName not saved: %q", got.Server.FriendlyName)
	}
	if got.Plex.Token != ownerToken || got.Catalog.TMDBKey != "tmdb-key" || got.Indexers[0].APIKey != "ix-key" {
		t.Fatal("scalar secret not preserved")
	}
	// Reordering must not swap passwords (matched by ID).
	if got.Usenet.Providers[0].ID != "p2" || got.Usenet.Providers[0].Password != "pw-two" {
		t.Fatalf("provider order/password mismatch: %+v", got.Usenet.Providers[0])
	}
	if got.Usenet.Providers[1].Password != "pw-one" {
		t.Fatalf("provider password swapped: %+v", got.Usenet.Providers[1])
	}
	// A genuinely new secret replaces the stored one.
	got.Indexers[0].APIKey = "fresh-key"
	if err := store.Update(func(c *config.Config) error { *c = got; return nil }); err != nil {
		t.Fatal(err)
	}
	if store.Get().Indexers[0].APIKey != "fresh-key" {
		t.Fatal("new secret not stored")
	}
}

func TestAuthMatrix(t *testing.T) {
	setupStore := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = false })
	doneStore := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = true })

	cases := []struct {
		name       string
		store      *config.Store
		remoteAddr string
		token      string
		want       int
	}{
		{"pre-setup LAN no token", setupStore, "10.0.0.5:1", "", 200},
		{"pre-setup localhost no token", setupStore, "127.0.0.1:1", "", 200},
		{"pre-setup WAN no token", setupStore, "203.0.113.9:1", "", 403},
		{"pre-setup WAN owner token", setupStore, "203.0.113.9:1", ownerToken, 200},
		{"post-setup LAN no token", doneStore, "10.0.0.5:1", "", 401},
		{"post-setup wrong token", doneStore, "10.0.0.5:1", "nope", 401},
		{"post-setup owner token", doneStore, "203.0.113.9:1", ownerToken, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := admin.New(admin.Deps{Config: tc.store, IsOwnerToken: storedOwner(tc.store)})
			r := req(http.MethodGet, "/ether/api/setup/status", "")
			r.RemoteAddr = tc.remoteAddr
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if rec := serve(h, r); rec.Code != tc.want {
				t.Fatalf("got %d want %d (%s)", rec.Code, tc.want, rec.Body)
			}
		})
	}

	// IsOwnerToken alone decides; the stored token is not consulted.
	h := admin.New(admin.Deps{Config: doneStore, IsOwnerToken: func(_ context.Context, tok string) bool {
		return tok == "custom-owner"
	}})
	r := req(http.MethodGet, "/ether/api/config", "")
	r.Header.Set("X-Plex-Token", "custom-owner")
	if rec := serve(h, r); rec.Code != 200 {
		t.Fatalf("custom owner token rejected: %d", rec.Code)
	}
	r = req(http.MethodGet, "/ether/api/config", "")
	r.Header.Set("X-Plex-Token", ownerToken)
	if rec := serve(h, r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stored token accepted without IsOwnerToken: %d", rec.Code)
	}
}

func TestCSRFHeaderRequiredForMutations(t *testing.T) {
	store := newStore(t, seededConfig)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	r := req(http.MethodPost, "/ether/api/setup/complete", `{"force":true}`)
	r.Header.Del("X-Ether-Request")
	if rec := serve(h, r); rec.Code != http.StatusForbidden {
		t.Fatalf("mutation without X-Ether-Request: got %d want 403", rec.Code)
	}
	if store.Get().SetupComplete {
		t.Fatal("CSRF-blocked request still mutated state")
	}
}

func TestSetupStatusAndGating(t *testing.T) {
	store := newStore(t, func(c *config.Config) {
		c.Usenet.Providers = []config.UsenetProvider{{ID: "p1", Host: "n", Port: 563, Enabled: true}}
		// no plex claim, no indexer → required steps incomplete
	})
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})

	rec := serve(h, req(http.MethodGet, "/ether/api/setup/status", ""))
	var st struct {
		SetupComplete    bool              `json:"setupComplete"`
		RequiredComplete bool              `json:"requiredComplete"`
		Steps            []admin.SetupStep `json:"steps"`
	}
	decode(t, rec, &st)
	if st.RequiredComplete || len(st.Steps) != 5 {
		t.Fatalf("bad setup status: %+v", st)
	}

	rec = serve(h, req(http.MethodPost, "/ether/api/setup/complete", `{}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("complete with missing steps: got %d want 409 (%s)", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodPost, "/ether/api/setup/complete", `{"force":true}`))
	if rec.Code != 200 || !store.Get().SetupComplete {
		t.Fatalf("forced complete failed: %d", rec.Code)
	}
	rec = serve(h, req(http.MethodPost, "/ether/api/setup/reopen", `{}`))
	// Now setup IS complete and no owner token is configured → reopen is
	// intentionally locked out (fail closed), proving post-setup auth.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reopen without token after setup: got %d want 401", rec.Code)
	}
}

func TestValidationErrors(t *testing.T) {
	store := newStore(t, nil)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	body := `{"port":70000,"friendlyName":""}`
	rec := serve(h, req(http.MethodPatch, "/ether/api/config/server", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid section: got %d (%s)", rec.Code, rec.Body)
	}
	var e struct {
		Error struct {
			Code   string             `json:"code"`
			Fields []admin.FieldError `json:"fields"`
		} `json:"error"`
	}
	decode(t, rec, &e)
	if e.Error.Code != "validation" || len(e.Error.Fields) < 2 {
		t.Fatalf("bad validation payload: %+v", e.Error)
	}
	if store.Get().Server.Port == 70000 {
		t.Fatal("invalid config was saved")
	}
	// Unknown section.
	rec = serve(h, req(http.MethodGet, "/ether/api/config/nope", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown section: got %d", rec.Code)
	}
}

func TestSectionPatchPreservesUnsetAndSecrets(t *testing.T) {
	store := newStore(t, seededConfig)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	rec := serve(h, req(http.MethodPatch, "/ether/api/config/catalog", `{"minVotes":42,"tmdbKey":"`+admin.RedactedSecret+`"}`))
	if rec.Code != 200 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	got := store.Get()
	if got.Catalog.MinVotes != 42 {
		t.Fatalf("minVotes not patched: %d", got.Catalog.MinVotes)
	}
	if got.Catalog.TMDBKey != "tmdb-key" {
		t.Fatal("patched secret placeholder overwrote stored key")
	}
	if got.Catalog.Language != "en-US" {
		t.Fatal("unspecified field was blanked by patch")
	}
}

type fakePlex struct{ polled bool }

func (f *fakePlex) StartPIN(context.Context) (string, string, string, error) {
	return "pin-1", "ABCD", "https://plex.tv/claim", nil
}
func (f *fakePlex) PollPIN(_ context.Context, id string) (bool, error) {
	f.polled = true
	return id == "pin-1", nil
}
func (f *fakePlex) Claim(context.Context) error { return nil }
func (f *fakePlex) Status() any                 { return map[string]any{"claimed": true} }

func TestPlexPINFlow(t *testing.T) {
	store := newStore(t, nil)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store), PlexAuth: &fakePlex{}})
	rec := serve(h, req(http.MethodPost, "/ether/api/plex/pin", `{}`))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ABCD"`) {
		t.Fatalf("pin start: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/plex/pin/pin-1", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done":true`) {
		t.Fatalf("pin poll: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/plex/status", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"detail":{"claimed":true}`) {
		t.Fatalf("plex status: %d %s", rec.Code, rec.Body)
	}
}

func TestTestersUnredactAndReport(t *testing.T) {
	store := newStore(t, seededConfig)
	var gotPassword string
	h := admin.New(admin.Deps{
		Config: store, IsOwnerToken: storedOwner(store),
		TestUsenet: func(_ context.Context, p config.UsenetProvider) error {
			gotPassword = p.Password
			return errors.New("connection refused")
		},
		TestTMDB: func(_ context.Context, key string) error {
			if key != "tmdb-key" {
				return errors.New("stored key not used for placeholder")
			}
			return nil
		},
	})
	body := `{"id":"p1","name":"Main","host":"news.example.com","port":563,"tls":true,"username":"u1","password":"` + admin.RedactedSecret + `","connections":20,"enabled":true}`
	rec := serve(h, req(http.MethodPost, "/ether/api/test/usenet", body))
	if rec.Code != 200 {
		t.Fatalf("test usenet: %d %s", rec.Code, rec.Body)
	}
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	decode(t, rec, &res)
	if res.OK || res.Error != "connection refused" {
		t.Fatalf("failed test should be ok:false with error: %+v", res)
	}
	if gotPassword != "pw-one" {
		t.Fatalf("tester received placeholder instead of stored password: %q", gotPassword)
	}
	rec = serve(h, req(http.MethodPost, "/ether/api/test/tmdb", `{"key":"`+admin.RedactedSecret+`"}`))
	decode(t, rec, &res)
	if !res.OK {
		t.Fatalf("tmdb test with stored key: %+v", res)
	}
}

type fakeUsers struct {
	mu    sync.Mutex
	users []core.User
}

func (f *fakeUsers) Users(context.Context) ([]core.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.User(nil), f.users...), nil
}
func (f *fakeUsers) UpdateUser(_ context.Context, u core.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == u.ID {
			f.users[i] = u
			return nil
		}
	}
	return core.ErrNotFound
}

func TestUsersEndpoints(t *testing.T) {
	store := newStore(t, nil)
	us := &fakeUsers{users: []core.User{{ID: 7, Username: "kid", Title: "Kid"}}}
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store), Users: us})

	rec := serve(h, req(http.MethodPatch, "/ether/api/users/7", `{"streamLimit":1,"maxContentRating":"PG","subtitleLanguages":["en"]}`))
	if rec.Code != 200 {
		t.Fatalf("update user: %d %s", rec.Code, rec.Body)
	}
	got := us.users[0]
	if got.StreamLimit != 1 || got.MaxContentRating != "PG" || got.Username != "kid" {
		t.Fatalf("user not merged/updated: %+v", got)
	}
	if rec := serve(h, req(http.MethodPatch, "/ether/api/users/99", `{"streamLimit":1}`)); rec.Code != 404 {
		t.Fatalf("missing user: got %d want 404", rec.Code)
	}
	if rec := serve(h, req(http.MethodPatch, "/ether/api/users/7", `{"streamLimit":-2}`)); rec.Code != 400 {
		t.Fatalf("negative limit: got %d want 400", rec.Code)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/users", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"streamLimit":1`) {
		t.Fatalf("list users: %d %s", rec.Code, rec.Body)
	}
}

func TestDashboardDiskAndLogs(t *testing.T) {
	store := newStore(t, nil)
	buf := admin.NewLogBuffer(10)
	log := slog.New(admin.TeeHandler(slog.NewTextHandler(io.Discard, nil), buf))
	log.Info("server started", "port", 32400)
	log.Warn("cache nearly full")

	h := admin.New(admin.Deps{
		Config: store, IsOwnerToken: storedOwner(store), Logs: buf,
		CacheStatus: func(context.Context) any { return map[string]any{"used": 40, "budget": 100} },
		ExtraStatus: map[string]admin.StatusFunc{
			"boom":   func(context.Context) any { panic("kaput") },
			"widget": func(context.Context) any { return map[string]any{"streams": 2} },
		},
		ResolverTraces: func(context.Context) (any, error) { return []string{"trace-1"}, nil },
		DiskUsage:      func(string) (int64, int64, error) { return 1000, 250, nil },
	})
	rec := serve(h, req(http.MethodGet, "/ether/api/dashboard", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"budget":100`) {
		t.Fatalf("dashboard: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "kaput") {
		t.Fatal("panicking status provider was not contained")
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/status", ""))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"streams":2}` {
		t.Fatalf("widget status: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/traces", ""))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"traces":["trace-1"]}` {
		t.Fatalf("traces: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/disk?path=/tmp/cache", ""))
	var disk struct {
		TotalBytes int64 `json:"totalBytes"`
		FreeBytes  int64 `json:"freeBytes"`
	}
	decode(t, rec, &disk)
	if disk.TotalBytes != 1000 || disk.FreeBytes != 250 {
		t.Fatalf("disk: %+v", disk)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/logs?q=cache", ""))
	if !strings.Contains(rec.Body.String(), "cache nearly full") || strings.Contains(rec.Body.String(), "server started") {
		t.Fatalf("log filter failed: %s", rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/logs?level=warn", ""))
	if strings.Contains(rec.Body.String(), "server started") {
		t.Fatalf("level filter failed: %s", rec.Body)
	}
}

func TestSPAFallbackAndAssets(t *testing.T) {
	store := newStore(t, func(c *config.Config) { c.SetupComplete = true })
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	// Deep client route with no token: SPA shell is public so the login
	// prompt can render; API data stays gated.
	rec := serve(h, req(http.MethodGet, "/ether/settings/providers", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("SPA fallback: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/unknown", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown API post-setup without token: got %d want 401", rec.Code)
	}
	// Bootstrap is public and secret-free.
	rec = serve(h, req(http.MethodGet, "/ether/api/bootstrap", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"setupComplete":true`) {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}
}

func TestLogBufferRingAndConcurrency(t *testing.T) {
	buf := admin.NewLogBuffer(50)
	log := slog.New(buf.Handler())
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				log.Info("tick", "i", i)
			}
		}()
	}
	wg.Wait()
	if buf.Len() != 50 {
		t.Fatalf("ring should cap at 50, got %d", buf.Len())
	}
	entries := buf.Entries(5, "", "")
	if len(entries) != 5 {
		t.Fatalf("limit: got %d", len(entries))
	}
	withGroup := slog.New(buf.Handler()).WithGroup("resolver").With("title", "Dune")
	withGroup.Info("picked release")
	found := buf.Entries(0, "", "resolver.title=dune")
	if len(found) != 1 {
		t.Fatalf("group attr flattening failed: %v", buf.Entries(3, "", ""))
	}
}

func TestArraySectionPut(t *testing.T) {
	store := newStore(t, seededConfig)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	body := `[{"id":"i1","name":"Idx","url":"https://idx.example.com","apiKey":"` + admin.RedactedSecret + `","dailyLimit":100,"dailyGrabs":0,"priority":0,"enabled":true,"movieCategories":null,"tvCategories":null},{"id":"i2","name":"Two","url":"https://two.example.com","apiKey":"k2","dailyLimit":0,"dailyGrabs":0,"priority":1,"enabled":true,"movieCategories":null,"tvCategories":null}]`
	rec := serve(h, req(http.MethodPut, "/ether/api/config/indexers", body))
	if rec.Code != 200 {
		t.Fatalf("PUT indexers section: %d %s", rec.Code, rec.Body)
	}
	got := store.Get()
	if len(got.Indexers) != 2 || got.Indexers[0].APIKey != "ix-key" || got.Indexers[0].DailyLimit != 100 || got.Indexers[1].APIKey != "k2" {
		t.Fatalf("array section not saved/unredacted: %+v", got.Indexers)
	}
	// GET section round-trips redacted.
	rec = serve(h, req(http.MethodGet, "/ether/api/config/indexers", ""))
	if strings.Contains(rec.Body.String(), "ix-key") || !strings.Contains(rec.Body.String(), admin.RedactedSecret) {
		t.Fatalf("section GET leaks or lacks placeholder: %s", rec.Body)
	}
}

func TestValidateConfigDirect(t *testing.T) {
	good := config.Defaults(t.TempDir())
	if errs := admin.ValidateConfig(good); len(errs) != 0 {
		t.Fatalf("defaults should validate: %v", errs)
	}
	good.Quality.Default = "missing"
	good.Indexers = []config.Indexer{{URL: "ftp://x"}}
	if errs := admin.ValidateConfig(good); len(errs) < 2 {
		t.Fatalf("expected >=2 errors, got %v", errs)
	}
}
