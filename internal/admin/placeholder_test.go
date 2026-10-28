package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
)

// The "••••" placeholder only ever stands for the stored secret of the
// same entry going to the same destination.

const ph = admin.RedactedSecret

func ownerReq(method, target, body string) *http.Request {
	r := req(method, target, body)
	r.Header.Set("X-Plex-Token", ownerToken)
	return r
}

func placeholderStore(t *testing.T) *config.Store {
	return newStore(t, func(c *config.Config) {
		seededConfig(c)
		c.SetupComplete = true
		c.LLM.Provider = config.LLMProviderCustom
		c.LLM.BaseURL = "http://llm.lan:8080/v1"
		c.OpenSubtitles.Username = "os-user"
		c.Trakt.ClientID = "trakt-client"
		c.Debrid.Providers = []config.DebridProvider{{ID: "rd", Name: "RD", Kind: "realdebrid", APIKey: "rd-secret", Enabled: true}}
		c.Indexers = append(c.Indexers, config.Indexer{ID: "site", Name: "Site", URL: "https://site.example.com", Kind: config.IndexerKindCardigann,
			Definition: "site-def", SecretSettings: map[string]string{"password": "site-pw"}, Enabled: true})
	})
}

func TestPlaceholderSaveRequiresUnchangedDestination(t *testing.T) {
	store := placeholderStore(t)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	before := store.Get()

	put := func(section, body string) (int, string) {
		rec := serve(h, ownerReq(http.MethodPut, "/ether/api/config/"+section, body))
		return rec.Code, rec.Body.String()
	}
	usenet := func(id, host string, port int, user, pw string) string {
		b, _ := json.Marshal(map[string]any{"providers": []config.UsenetProvider{{ID: id, Name: "Main", Host: host, Port: port, TLS: true, Username: user, Password: pw, Connections: 20, Enabled: true}}})
		return string(b)
	}
	refused := []struct{ name, section, body, field string }{
		{"usenet host", "usenet", usenet("p1", "evil.example.com", 563, "u1", ph), "usenet.providers[0].password"},
		{"usenet port", "usenet", usenet("p1", "news.example.com", 119, "u1", ph), "usenet.providers[0].password"},
		{"usenet user", "usenet", usenet("p1", "news.example.com", 563, "other", ph), "usenet.providers[0].password"},
		{"new entry inherits nothing", "usenet", usenet("p9", "news.example.com", 563, "u1", ph), "usenet.providers[0].password"},
		{"no id, no positional fallback", "usenet", usenet("", "news.example.com", 563, "u1", ph), "usenet.providers[0].password"},
		{"indexer url", "indexers", `[{"id":"i1","name":"Idx","url":"https://evil.example.com","apiKey":"` + ph + `","enabled":true}]`, "indexers[0].apiKey"},
		{"indexer kind", "indexers", `[{"id":"site","name":"Site","url":"https://site.example.com","kind":"newznab","secretSettings":{"password":"` + ph + `"},"enabled":true}]`, "indexers[0].secretSettings.password"},
		{"indexer definition", "indexers", `[{"id":"site","name":"Site","url":"https://site.example.com","kind":"cardigann","definition":"other","secretSettings":{"password":"` + ph + `"},"enabled":true}]`, "indexers[0].secretSettings.password"},
		{"debrid kind", "debrid", `{"providers":[{"id":"rd","name":"RD","kind":"torbox","apiKey":"` + ph + `","enabled":true}]}`, "debrid.providers[0].apiKey"},
		{"llm base url", "llm", `{"provider":"custom","baseUrl":"https://evil.example.com/v1","apiKey":"` + ph + `"}`, "llm.apiKey"},
		// A patch that leaves the key out must not carry it to the new endpoint.
		{"llm base url, key omitted", "llm", `{"baseUrl":"https://evil.example.com/v1"}`, "llm.apiKey"},
		{"llm provider", "llm", `{"provider":"openai"}`, "llm.apiKey"},
		{"opensubtitles user", "opensubtitles", `{"username":"mallory"}`, "openSubtitles.password"},
		{"trakt client", "trakt", `{"clientId":"other-client"}`, "trakt.clientSecret"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			code, body := put(tc.section, tc.body)
			if code != http.StatusBadRequest || !strings.Contains(body, `"`+tc.field+`"`) {
				t.Fatalf("got %d %s, want a validation error on %s", code, body, tc.field)
			}
			for _, secret := range []string{"pw-one", "ix-key", "site-pw", "rd-secret", "llm-key", "os-pw", "trakt-secret"} {
				if strings.Contains(body, secret) {
					t.Fatalf("error response leaks %q", secret)
				}
			}
			if after := store.Get(); after.Usenet.Providers[0].Host != before.Usenet.Providers[0].Host || after.LLM.BaseURL != before.LLM.BaseURL ||
				len(after.Indexers) != len(before.Indexers) || after.Debrid.Providers[0].Kind != "realdebrid" || after.Trakt.ClientID != "trakt-client" || after.OpenSubtitles.Username != "os-user" {
				t.Fatalf("refused save changed the stored config: %+v", after)
			}
		})
	}

	// An explicit new secret with a new destination is fine.
	if code, body := put("usenet", usenet("p1", "new.example.com", 563, "u1", "fresh-pw")); code != 200 {
		t.Fatalf("explicit secret: %d %s", code, body)
	}
	if p := store.Get().Usenet.Providers[0]; p.Host != "new.example.com" || p.Password != "fresh-pw" {
		t.Fatalf("explicit secret not saved: %+v", p)
	}
	if code, body := put("llm", `{"baseUrl":"https://new.example.com/v1","apiKey":"fresh-llm"}`); code != 200 || store.Get().LLM.APIKey != "fresh-llm" {
		t.Fatalf("explicit llm key: %d %s", code, body)
	}
	// Non-destination edits keep the stored secrets.
	if code, body := put("llm", `{"model":"m2"}`); code != 200 || store.Get().LLM.APIKey != "fresh-llm" {
		t.Fatalf("model edit: %d %s %+v", code, body, store.Get().LLM)
	}
	if code, body := put("opensubtitles", `{"enabled":true}`); code != 200 || store.Get().OpenSubtitles.Password != "os-pw" {
		t.Fatalf("opensubtitles edit: %d %s", code, body)
	}
}

func TestPlaceholderRoundTripUnchanged(t *testing.T) {
	store := placeholderStore(t)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	want := store.Get()
	rec := serve(h, ownerReq(http.MethodGet, "/ether/api/config", ""))
	if rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	var env struct {
		Config config.Config `json:"config"`
	}
	decode(t, rec, &env)
	// Reorder lists and normalise case: secrets follow their IDs.
	c := env.Config
	c.Usenet.Providers[0], c.Usenet.Providers[1] = c.Usenet.Providers[1], c.Usenet.Providers[0]
	c.Usenet.Providers[1].Host = "NEWS.example.com"
	c.Indexers[0], c.Indexers[1] = c.Indexers[1], c.Indexers[0]
	c.Indexers[1].URL = "https://idx.example.com/"
	c.LLM.BaseURL = "http://llm.lan:8080/v1/"
	raw, _ := json.Marshal(c)
	if rec = serve(h, ownerReq(http.MethodPut, "/ether/api/config", string(raw))); rec.Code != 200 {
		t.Fatalf("round trip: %d %s", rec.Code, rec.Body)
	}
	got := store.Get()
	if got.Usenet.Providers[0].Password != "pw-two" || got.Usenet.Providers[1].Password != "pw-one" ||
		got.Indexers[0].SecretSettings["password"] != "site-pw" || got.Indexers[1].APIKey != "ix-key" ||
		got.Debrid.Providers[0].APIKey != "rd-secret" || got.LLM.APIKey != want.LLM.APIKey ||
		got.OpenSubtitles.Password != "os-pw" || got.OpenSubtitles.APIKey != "os-key" || got.Trakt.ClientSecret != "trakt-secret" ||
		got.Catalog.TMDBKey != "tmdb-key" || got.Plex.Token != ownerToken {
		t.Fatalf("round trip lost a secret: %+v", got)
	}
}

func TestPlaceholderTestersStayBound(t *testing.T) {
	store := placeholderStore(t)
	var called []string
	var seen string
	note := func(name, secret string) error { called = append(called, name); seen = secret; return nil }
	h := admin.New(admin.Deps{
		Config: store, IsOwnerToken: storedOwner(store),
		TestUsenet: func(_ context.Context, p config.UsenetProvider) error { return note("usenet", p.Password) },
		TestIndexer: func(_ context.Context, ix config.Indexer) error {
			return note("indexer", ix.APIKey+ix.SecretSettings["password"])
		},
		TestDebrid: func(_ context.Context, p config.DebridProvider) error { return note("debrid", p.APIKey) },
		TestLLM:    func(_ context.Context, c config.LLM) error { return note("llm", c.APIKey) },
		LLMModels: func(_ context.Context, c config.LLM) ([]admin.LLMModel, error) {
			return nil, note("models", c.APIKey)
		},
		TestOpenSubtitles: func(_ context.Context, c config.OpenSubtitles) error {
			return note("opensubtitles", c.APIKey+"/"+c.Password)
		},
		TestTrakt: func(_ context.Context, c config.Trakt) error { return note("trakt", c.ClientSecret) },
	})
	refused := []struct{ path, body string }{
		{"/test/usenet", `{"id":"p1","host":"evil.example.com","port":563,"tls":true,"username":"u1","password":"` + ph + `"}`},
		{"/test/usenet", `{"id":"p1","host":"news.example.com","port":563,"tls":false,"username":"u1","password":"` + ph + `"}`},
		{"/test/usenet", `{"host":"news.example.com","port":563,"tls":true,"username":"u1","password":"` + ph + `"}`},
		{"/test/indexer", `{"id":"i1","url":"https://evil.example.com","apiKey":"` + ph + `"}`},
		{"/test/indexer", `{"url":"https://idx.example.com","apiKey":"` + ph + `"}`},
		{"/test/indexer", `{"id":"site","url":"https://evil.example.com","kind":"cardigann","definition":"site-def","secretSettings":{"password":"` + ph + `"}}`},
		{"/test/debrid", `{"id":"rd","kind":"premiumize","apiKey":"` + ph + `","enabled":true}`},
		{"/test/debrid", `{"kind":"realdebrid","apiKey":"` + ph + `","enabled":true}`},
		{"/test/llm", `{"provider":"custom","baseUrl":"https://evil.example.com/v1","apiKey":"` + ph + `"}`},
		{"/test/llm", `{"baseUrl":"https://evil.example.com/v1"}`},
		{"/llm/models", `{"provider":"custom","baseUrl":"https://evil.example.com/v1","apiKey":"` + ph + `"}`},
		{"/llm/models", `{"provider":"openrouter"}`},
		{"/test/indexer", `{"id":"site","url":"https://site.example.com","kind":"cardigann","definition":"site-def","settings":{"sitelink":"https://evil.example.com"},"secretSettings":{"password":"` + ph + `"}}`},
		{"/test/indexer", `{"id":"i1","url":"https://idx.example.com","protocol":"torrent","apiKey":"` + ph + `"}`},
		{"/test/opensubtitles", `{"username":"mallory","password":"` + ph + `"}`},
		{"/test/trakt", `{"clientId":"other-client"}`},
	}
	for _, tc := range refused {
		rec := serve(h, ownerReq(http.MethodPost, "/ether/api"+tc.path, tc.body))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "secret_required") {
			t.Fatalf("%s %s: %d %s", tc.path, tc.body, rec.Code, rec.Body)
		}
		if len(called) != 0 {
			t.Fatalf("%s %s reached the tester with %q", tc.path, tc.body, seen)
		}
	}

	// Malformed bodies never fall back to the stored settings.
	for _, path := range []string{"/test/llm", "/llm/models", "/test/trakt", "/test/opensubtitles", "/test/tmdb", "/test/usenet", "/test/indexer", "/test/debrid"} {
		rec := serve(h, ownerReq(http.MethodPost, "/ether/api"+path, `{"baseUrl":"https://evil.example.com/v1",`))
		if rec.Code != http.StatusBadRequest || len(called) != 0 {
			t.Fatalf("%s malformed body: %d %s, tester calls %v", path, rec.Code, rec.Body, called)
		}
	}

	allowed := []struct{ path, body, want string }{
		{"/test/usenet", `{"id":"p1","host":"News.Example.com","port":563,"tls":true,"username":"u1","password":"` + ph + `"}`, "pw-one"},
		{"/test/usenet", `{"id":"p1","host":"evil.example.com","port":563,"tls":true,"username":"u1","password":"typed"}`, "typed"},
		{"/test/indexer", `{"id":"i1","url":"https://idx.example.com/","apiKey":"` + ph + `"}`, "ix-key"},
		{"/test/indexer", `{"id":"site","url":"https://site.example.com","kind":"cardigann","definition":"site-def","secretSettings":{"password":"` + ph + `"}}`, "site-pw"},
		{"/test/debrid", `{"id":"rd","kind":"realdebrid","apiKey":"` + ph + `","enabled":true}`, "rd-secret"},
		{"/test/llm", `{"model":"other"}`, "llm-key"},
		{"/test/llm", `{"provider":"custom","baseUrl":"http://llm.lan:8080/v1","apiKey":"` + ph + `"}`, "llm-key"},
		{"/test/llm", `{"baseUrl":"https://evil.example.com/v1","apiKey":"typed"}`, "typed"},
		{"/llm/models", `{"apiKey":"` + ph + `"}`, "llm-key"},
		{"/test/opensubtitles", `{"username":"os-user","password":"` + ph + `"}`, "os-key/os-pw"},
		{"/test/trakt", `{"clientId":"trakt-client","clientSecret":"` + ph + `"}`, "trakt-secret"},
	}
	for _, tc := range allowed {
		called, seen = nil, ""
		rec := serve(h, ownerReq(http.MethodPost, "/ether/api"+tc.path, tc.body))
		if rec.Code != 200 || len(called) != 1 || seen != tc.want {
			t.Fatalf("%s %s: %d %s secret %q", tc.path, tc.body, rec.Code, rec.Body, seen)
		}
	}
}

// A built-in site's secrets are bound to the FlareSolverr service that
// challenged requests (and their query strings) are relayed through.
func TestPlaceholderBoundToFlareSolverr(t *testing.T) {
	store := placeholderStore(t)
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	rec := serve(h, ownerReq(http.MethodPatch, "/ether/api/config/indexerSites", `{"flareSolverrUrl":"https://evil.example.com"}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"indexers[1].secretSettings.password"`) || strings.Contains(rec.Body.String(), "indexers[0]") {
		t.Fatalf("flaresolverr change kept site secret: %d %s", rec.Code, rec.Body)
	}
	if store.Get().IndexerSites.FlareSolverrURL != "" {
		t.Fatal("refused change was saved")
	}
}

func TestUnredactIndexerUnboundIsEmpty(t *testing.T) {
	prev := placeholderStore(t).Get()
	got := admin.UnredactIndexer(config.Indexer{ID: "site", URL: "https://evil.example.com", Kind: config.IndexerKindCardigann, Definition: "site-def",
		SecretSettings: map[string]string{"password": ph}}, prev)
	if got.SecretSettings["password"] != "" {
		t.Fatalf("stored secret followed a changed URL: %+v", got)
	}
	got = admin.UnredactIndexer(config.Indexer{ID: "site", URL: "https://site.example.com", Kind: config.IndexerKindCardigann, Definition: "site-def",
		SecretSettings: map[string]string{"password": ph}}, prev)
	if got.SecretSettings["password"] != "site-pw" {
		t.Fatalf("bound secret not restored: %+v", got)
	}
}
