package admin_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
)

func builtinIndexer() config.Indexer {
	return config.Indexer{ID: "site1", Name: "Site", URL: "https://site.example.com/", Kind: config.IndexerKindCardigann, Definition: "somesite",
		Protocol: "torrent", Enabled: true, Settings: map[string]string{"username": "bob", "sort": "seeders"},
		SecretSettings: map[string]string{"password": "site-password", "passkey": "site-passkey"}}
}

func TestBuiltinIndexerSecretsRedactedAndPreserved(t *testing.T) {
	store := newStore(t, func(c *config.Config) {
		seededConfig(c)
		c.SetupComplete = true
		c.Indexers = append(c.Indexers, builtinIndexer())
	})
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	r := req(http.MethodGet, "/ether/api/config/indexers", "")
	r.Header.Set("X-Plex-Token", ownerToken)
	rec := serve(h, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	for _, secret := range []string{"site-password", "site-passkey"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("secret setting %q leaked", secret)
		}
	}
	if !strings.Contains(rec.Body.String(), "seeders") {
		t.Fatal("ordinary settings should be visible")
	}
	var out struct {
		Value []config.Indexer `json:"value"`
	}
	decode(t, rec, &out)
	// Send the list back reordered with one placeholder kept and one secret changed.
	list := []config.Indexer{out.Value[1], out.Value[0]}
	list[0].SecretSettings["passkey"] = "new-passkey"
	raw, _ := json.Marshal(list)
	r2 := req(http.MethodPut, "/ether/api/config/indexers", string(raw))
	r2.Header.Set("X-Plex-Token", ownerToken)
	if rec2 := serve(h, r2); rec2.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec2.Code, rec2.Body)
	}
	got := store.Get().Indexers[0]
	if got.ID != "site1" || got.SecretSettings["password"] != "site-password" || got.SecretSettings["passkey"] != "new-passkey" {
		t.Fatalf("secrets = %+v", got.SecretSettings)
	}
	if store.Get().Indexers[1].APIKey != "ix-key" {
		t.Fatal("newznab key lost on reorder")
	}
	// The exported helper restores placeholders for unsaved tests.
	probe := builtinIndexer()
	probe.SecretSettings = map[string]string{"password": admin.RedactedSecret, "passkey": "typed"}
	probe = admin.UnredactIndexer(probe, store.Get())
	if probe.SecretSettings["password"] != "site-password" || probe.SecretSettings["passkey"] != "typed" {
		t.Fatalf("UnredactIndexer = %+v", probe.SecretSettings)
	}
}

func TestBuiltinIndexerValidation(t *testing.T) {
	c := config.Defaults(t.TempDir())
	ix := builtinIndexer()
	ix.Definition = ""
	c.Indexers = []config.Indexer{ix, {ID: "x", URL: "https://x.example.com", Kind: "weird"}}
	c.IndexerSites.FlareSolverrURL = "not a url"
	errs := admin.ValidateConfig(c)
	want := map[string]bool{"indexers[0].definition": false, "indexers[1].kind": false, "indexerSites.flareSolverrUrl": false}
	for _, e := range errs {
		if _, ok := want[e.Field]; ok {
			want[e.Field] = true
		}
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("no validation error for %s (got %v)", f, errs)
		}
	}
}
