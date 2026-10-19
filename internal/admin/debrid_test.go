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

func debridConfig(c *config.Config) {
	seededConfig(c)
	c.SetupComplete = true
	c.Debrid.Providers = []config.DebridProvider{
		{ID: "rd", Name: "RD", Kind: "realdebrid", APIKey: "rd-secret", Enabled: true},
		{ID: "tb", Name: "TB", Kind: "torbox", APIKey: "tb-secret", Priority: 1, Enabled: true},
	}
	c.Indexers = append(c.Indexers, config.Indexer{ID: "tz", Name: "Torznab", URL: "https://tz.example.com", APIKey: "tz-key", Enabled: true, Protocol: "torrent"})
}

func TestDebridSecretsRedactedAndPreserved(t *testing.T) {
	store := newStore(t, debridConfig)
	h := admin.New(admin.Deps{Config: store})
	r := req(http.MethodGet, "/ether/api/config/debrid", "")
	r.Header.Set("X-Plex-Token", ownerToken)
	rec := serve(h, r)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "rd-secret") || strings.Contains(rec.Body.String(), "tb-secret") {
		t.Fatalf("GET debrid: %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Value config.Debrid `json:"value"`
	}
	decode(t, rec, &env)
	if env.Value.Providers[0].APIKey != admin.RedactedSecret || !env.Value.CachedOnly {
		t.Fatalf("value %+v", env.Value)
	}
	// Reorder and send placeholders back: keys follow their IDs.
	env.Value.Providers[0], env.Value.Providers[1] = env.Value.Providers[1], env.Value.Providers[0]
	raw, _ := json.Marshal(env.Value)
	put := req(http.MethodPut, "/ether/api/config/debrid", string(raw))
	put.Header.Set("X-Plex-Token", ownerToken)
	if rec = serve(h, put); rec.Code != 200 {
		t.Fatalf("PUT debrid: %d %s", rec.Code, rec.Body)
	}
	got := store.Get().Debrid.Providers
	if got[0].ID != "tb" || got[0].APIKey != "tb-secret" || got[1].APIKey != "rd-secret" {
		t.Fatalf("keys %+v", got)
	}
	// Validation: unknown kind, enabled without key, bad protocol.
	bad := `{"providers":[{"id":"x","kind":"nope","enabled":true}]}`
	put = req(http.MethodPut, "/ether/api/config/debrid", bad)
	put.Header.Set("X-Plex-Token", ownerToken)
	if rec = serve(h, put); rec.Code == 200 || !strings.Contains(rec.Body.String(), "debrid.providers[0].kind") || !strings.Contains(rec.Body.String(), "debrid.providers[0].apiKey") {
		t.Fatalf("validation: %d %s", rec.Code, rec.Body)
	}
	errs := admin.ValidateConfig(func() config.Config {
		c := store.Get()
		c.Indexers[0].Protocol = "ftp"
		return c
	}())
	if len(errs) == 0 || !strings.Contains(errs[0].Field, "protocol") {
		t.Fatalf("protocol validation %+v", errs)
	}
}

func TestDebridTesterUnredacts(t *testing.T) {
	store := newStore(t, debridConfig)
	var gotKey string
	h := admin.New(admin.Deps{Config: store, TestDebrid: func(_ context.Context, p config.DebridProvider) error {
		gotKey = p.APIKey
		return nil
	}})
	body := `{"id":"rd","name":"RD","kind":"realdebrid","apiKey":"` + admin.RedactedSecret + `","enabled":true}`
	r := req(http.MethodPost, "/ether/api/test/debrid", body)
	r.Header.Set("X-Plex-Token", ownerToken)
	rec := serve(h, r)
	var res struct {
		OK bool `json:"ok"`
	}
	decode(t, rec, &res)
	if !res.OK || gotKey != "rd-secret" {
		t.Fatalf("test debrid: %s key %q", rec.Body, gotKey)
	}
}
