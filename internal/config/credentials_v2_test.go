package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// v2Fixture opens a store holding several secrets with destinations.
func v2Fixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *Config) error {
		c.Plex.Token = "plex-token-value"
		c.Catalog.TMDBKey = "tmdb-key-value"
		c.Usenet.Providers = []UsenetProvider{
			{ID: "a", Host: "news-a.test", Port: 563, TLS: true, Username: "user-a", Password: "pass-a"},
			{ID: "b", Host: "news-b.test", Port: 563, TLS: true, Username: "user-b", Password: "pass-b"},
		}
		c.Indexers = []Indexer{
			{ID: "ix1", URL: "https://ix1.test", APIKey: "apikey-one"},
			{ID: "ix2", URL: "https://ix2.test", APIKey: "apikey-two"},
			{ID: "site", Kind: IndexerKindCardigann, Definition: "def", URL: "https://site.test",
				SecretSettings: map[string]string{"passkey": "passkey-value"}},
		}
		c.LLM.Provider, c.LLM.APIKey = "openai", "llm-key-value"
		c.OpenSubtitles.APIKey = "os-key-value"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

var fixtureSecrets = []string{"plex-token-value", "tmdb-key-value", "user-a", "pass-a", "user-b", "pass-b", "apikey-one", "apikey-two", "passkey-value", "llm-key-value", "os-key-value"}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func writeDoc(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func obj(v any, path ...any) map[string]any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = v.(map[string]any)[k]
		case int:
			v = v.([]any)[k]
		}
	}
	return v.(map[string]any)
}

func assertNoPlaintext(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range fixtureSecrets {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("plaintext %q at rest", secret)
		}
	}
	return string(raw)
}

func TestCredentialsStayV1UntilUpgrade(t *testing.T) {
	s, dir := v2Fixture(t)
	raw := assertNoPlaintext(t, s.Path())
	if strings.Contains(raw, sealedPrefixV2) || strings.Contains(raw, credentialFormatKey) || s.CredentialsUpgraded() {
		t.Fatal("v2 written before UpgradeCredentials")
	}
	if err := s.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw = assertNoPlaintext(t, s.Path())
	if strings.Contains(raw, sealedPrefix) || !strings.Contains(raw, sealedPrefixV2) || !strings.Contains(raw, `"credentialFormat": 2`) {
		t.Fatalf("upgrade did not rewrite every credential in v2:\n%s", raw)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Get()
	if !again.CredentialsUpgraded() || got.Plex.Token != "plex-token-value" || got.Catalog.TMDBKey != "tmdb-key-value" ||
		got.Usenet.Providers[1].Password != "pass-b" || got.Indexers[2].SecretSettings["passkey"] != "passkey-value" {
		t.Fatalf("v2 round trip: upgraded=%v %+v", again.CredentialsUpgraded(), got.Usenet.Providers)
	}
	if cfg, err := Read(dir); err != nil || cfg.LLM.APIKey != "llm-key-value" {
		t.Fatalf("Read v2: %v", err)
	}
	// Idempotent; later saves stay v2.
	if err := again.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := again.Update(func(c *Config) error { c.Server.FriendlyName = "x"; return nil }); err != nil {
		t.Fatal(err)
	}
	if raw := assertNoPlaintext(t, again.Path()); strings.Contains(raw, sealedPrefix) {
		t.Fatal("save after upgrade wrote v1")
	}
}

func TestLegacyFilesMigrate(t *testing.T) {
	for name, legacy := range map[string]func(key []byte) []byte{
		"plaintext": func([]byte) []byte {
			return []byte(`{"plex":{"token":"plex-token-value"},"usenet":{"providers":[{"id":"a","host":"h","username":"user-a","password":"pass-a"}]}}`)
		},
		"v1": func(key []byte) []byte {
			b, err := sealDocument([]byte(`{"plex":{"token":"plex-token-value"},"usenet":{"providers":[{"id":"a","host":"h","username":"user-a","password":"pass-a"}]}}`), key, false)
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			key := make([]byte, 32)
			key[0] = 7
			if err := InstallCredentialKey(dir, key); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), legacy(key), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if s.KeyCreated() {
				t.Fatal("existing key reported as created")
			}
			assertNoPlaintext(t, s.Path())
			if err := s.UpgradeCredentials(context.Background()); err != nil {
				t.Fatal(err)
			}
			raw := assertNoPlaintext(t, s.Path())
			if strings.Contains(raw, sealedPrefix) {
				t.Fatal("legacy value left after upgrade")
			}
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if c := s.Get(); c.Plex.Token != "plex-token-value" || c.Usenet.Providers[0].Password != "pass-a" || c.Usenet.Providers[0].Username != "user-a" {
				t.Fatalf("migrated values: %+v", c.Usenet.Providers)
			}
		})
	}
}

// tamper upgrades the fixture, applies edit to its stored document and
// reports Open's error.
func tamper(t *testing.T, edit func(doc map[string]any)) error {
	t.Helper()
	s, dir := v2Fixture(t)
	if err := s.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, s.Path())
	edit(doc)
	writeDoc(t, s.Path(), doc)
	_, err := Open(dir)
	return err
}

func TestV2RejectsRepointedAndMovedSecrets(t *testing.T) {
	cases := map[string]func(doc map[string]any){
		"usenet host repointed": func(doc map[string]any) { obj(doc, "usenet", "providers", 0)["host"] = "evil.test" },
		"usenet port changed":   func(doc map[string]any) { obj(doc, "usenet", "providers", 0)["port"] = 119 },
		"usenet tls disabled":   func(doc map[string]any) { obj(doc, "usenet", "providers", 0)["tls"] = false },
		"indexer url repointed": func(doc map[string]any) { obj(doc, "indexers", 0)["url"] = "https://evil.test" },
		"site url repointed":    func(doc map[string]any) { obj(doc, "indexers", 2)["url"] = "https://evil.test" },
		"llm provider changed":  func(doc map[string]any) { obj(doc, "llm")["provider"] = "custom" },
		"llm endpoint added":    func(doc map[string]any) { obj(doc, "llm")["baseUrl"] = "https://evil.test/v1" },
		"password moved between providers": func(doc map[string]any) {
			obj(doc, "usenet", "providers", 1)["password"] = obj(doc, "usenet", "providers", 0)["password"]
		},
		"username and password swapped": func(doc map[string]any) {
			p := obj(doc, "usenet", "providers", 0)
			p["username"], p["password"] = p["password"], p["username"]
		},
		"api key moved between indexers": func(doc map[string]any) {
			obj(doc, "indexers", 1)["apiKey"] = obj(doc, "indexers", 0)["apiKey"]
		},
		"provider id changed": func(doc map[string]any) { obj(doc, "usenet", "providers", 0)["id"] = "c" },
		"token moved to another section": func(doc map[string]any) {
			obj(doc, "openSubtitles")["apiKey"] = obj(doc, "plex")["token"]
		},
		"secret setting renamed": func(doc map[string]any) {
			ss := obj(doc, "indexers", 2, "secretSettings")
			ss["cookie"] = ss["passkey"]
			delete(ss, "passkey")
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tamper(t, edit); err == nil {
				t.Fatal("tampered credential opened")
			}
		})
	}
}

func TestV2SurvivesReorderAndUnrelatedEdits(t *testing.T) {
	err := tamper(t, func(doc map[string]any) {
		u := obj(doc, "usenet")
		ps := u["providers"].([]any)
		u["providers"] = []any{ps[1], ps[0]}
		ix := doc["indexers"].([]any)
		doc["indexers"] = []any{ix[2], ix[0], ix[1]}
		obj(doc, "usenet", "providers", 0)["connections"] = 40
		obj(doc, "server")["friendlyName"] = "renamed"
	})
	if err != nil {
		t.Fatalf("reordered lists or edited non-destination fields: %v", err)
	}
}

func TestV2RejectsLegacyReplayAndMalformedValues(t *testing.T) {
	cases := map[string]func(t *testing.T, doc map[string]any, key []byte){
		// A v1 ciphertext of the same value (an old backup) replayed
		// into an upgraded document.
		"legacy replay": func(t *testing.T, doc map[string]any, key []byte) {
			a, _ := newAEAD(key)
			v1, err := sealWithAAD(a, sealedPrefix, "pass-a", []byte("password"))
			if err != nil {
				t.Fatal(err)
			}
			obj(doc, "usenet", "providers", 0)["password"] = v1
		},
		"bad base64": func(_ *testing.T, doc map[string]any, _ []byte) { obj(doc, "plex")["token"] = sealedPrefixV2 + "!!!" },
		"truncated":  func(_ *testing.T, doc map[string]any, _ []byte) { obj(doc, "plex")["token"] = sealedPrefixV2 + "AAAA" },
		"bit flip": func(_ *testing.T, doc map[string]any, _ []byte) {
			v := obj(doc, "plex")["token"].(string)
			last := v[len(v)-1]
			if last == 'A' {
				last = 'B'
			} else {
				last = 'A'
			}
			obj(doc, "plex")["token"] = v[:len(v)-1] + string(last)
		},
		"future prefix": func(_ *testing.T, doc map[string]any, _ []byte) { obj(doc, "plex")["token"] = "ether-sealed:v3:abc" },
		"future format": func(_ *testing.T, doc map[string]any, _ []byte) { doc[credentialFormatKey] = 3 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			s, dir := v2Fixture(t)
			if err := s.UpgradeCredentials(context.Background()); err != nil {
				t.Fatal(err)
			}
			doc := readDoc(t, s.Path())
			edit(t, doc, s.CredentialKey())
			writeDoc(t, s.Path(), doc)
			before, _ := os.ReadFile(s.Path())
			if _, err := Open(dir); err == nil {
				t.Fatal("opened")
			}
			after, _ := os.ReadFile(s.Path())
			if string(before) != string(after) {
				t.Fatal("a configuration that did not open was rewritten")
			}
		})
	}
}

// An older release reads a v2 value as an opaque string and, when it
// saves, seals that string again in v1 without the format marker.
func TestOlderReleaseRewriteUnwraps(t *testing.T) {
	s, dir := v2Fixture(t)
	if err := s.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	delete(doc, credentialFormatKey)
	asSeenByOld, _ := json.Marshal(doc)
	rewritten, err := sealDocument(asSeenByOld, s.CredentialKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Get(); c.Usenet.Providers[0].Password != "pass-a" || c.Plex.Token != "plex-token-value" || c.Indexers[2].SecretSettings["passkey"] != "passkey-value" {
		t.Fatalf("double-sealed values: %+v", c.Usenet.Providers[0])
	}
	if again.CredentialsUpgraded() {
		t.Fatal("a document without the marker is not strict")
	}
}

func TestV2WrongAndLostKey(t *testing.T) {
	s, dir := v2Fixture(t)
	if err := s.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	// Lost key: never silently replaced when only v2 values are present.
	lost := t.TempDir()
	if err := os.WriteFile(filepath.Join(lost, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(lost); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := os.Stat(filepath.Join(lost, CredentialKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a new credential key was generated over v2 credentials")
	}
	// Wrong key.
	other := make([]byte, 32)
	other[5] = 1
	if err := InstallCredentialKey(lost, other); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(lost); err == nil {
		t.Fatal("wrong key accepted")
	}
	// Restore with the original key works.
	restored := t.TempDir()
	if err := os.WriteFile(filepath.Join(restored, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallCredentialKey(restored, s.CredentialKey()); err != nil {
		t.Fatal(err)
	}
	r, err := Open(restored)
	if err != nil || r.Get().Usenet.Providers[0].Password != "pass-a" || !r.CredentialsUpgraded() {
		t.Fatalf("restore: %v", err)
	}
	_ = dir
}

func TestSealedFieldsAndCompatibility(t *testing.T) {
	s, _ := Open(t.TempDir())
	// Existing Seal/Unseal consumers keep the v1 name-bound format.
	sealed, err := s.Seal("identity/cert", []byte("pem"))
	if err != nil || !strings.HasPrefix(sealed, sealedPrefix) {
		t.Fatal(sealed, err)
	}
	// Before the upgrade, fields are stored as given.
	if v, err := s.SealField("secret", "t", "c", "1"); err != nil || v != "secret" {
		t.Fatalf("pre-upgrade field: %q %v", v, err)
	}
	if v, legacy, err := s.OpenField("secret", "t", "c", "1"); err != nil || !legacy || v != "secret" {
		t.Fatalf("legacy field: %q %v %v", v, legacy, err)
	}
	if err := s.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Unseal("identity/cert", sealed); err != nil || string(got) != "pem" {
		t.Fatal("Seal value unreadable after upgrade")
	}
	if again, _ := s.Seal("identity/cert", []byte("pem")); !strings.HasPrefix(again, sealedPrefix) {
		t.Fatal("Seal format changed")
	}
	v, err := s.SealField("secret", "t", "c", "1")
	if err != nil || !strings.HasPrefix(v, sealedPrefixV2) || strings.Contains(v, "secret") {
		t.Fatalf("field: %q %v", v, err)
	}
	if got, legacy, err := s.OpenField(v, "t", "c", "1"); err != nil || legacy || got != "secret" {
		t.Fatalf("open field: %q %v %v", got, legacy, err)
	}
	for _, ctx := range [][]string{{"t", "c", "2"}, {"t", "d", "1"}, {"t", "c"}, {"t", "c1", ""}, {"t", "c", "1", "dest"}} {
		if _, _, err := s.OpenField(v, ctx...); err == nil {
			t.Fatalf("field opened under %q", ctx)
		}
	}
	if _, _, err := s.OpenField(sealedPrefixV2+"???", "t", "c", "1"); err == nil {
		t.Fatal("malformed field opened")
	}
	if _, _, err := s.OpenField(sealed, "identity/cert"); err == nil {
		t.Fatal("v1 Seal value accepted as a field")
	}
	if v, err := s.SealField("", "t", "c", "1"); err != nil || v != "" {
		t.Fatal("empty field sealed")
	}
	if _, _, err := OpenWith(nil, v, "t", "c", "1"); !errors.Is(err, ErrSealedWithoutKey) {
		t.Fatalf("nil sealer opened a sealed field: %v", err)
	}
	if got, legacy, err := OpenWith(nil, "plain", "t"); err != nil || !legacy || got != "plain" {
		t.Fatal("nil sealer plaintext")
	}
	// Errors name the table and column only.
	_, _, err = s.OpenField(v, "t", "c", "user-42", "https://evil.test")
	if err == nil || strings.Contains(err.Error(), "user-42") || strings.Contains(err.Error(), "evil") {
		t.Fatalf("error leaks context: %v", err)
	}
}

func TestSharedCredentialUpgrade(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dirA, dirB := clusterNodes(t)
	backend := &memShared{}
	a, err := Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Update(func(c *Config) error {
		c.Usenet.Providers = []UsenetProvider{{ID: "p", Host: "news.test", Port: 563, Username: "user-a", Password: "pass-a"}}
		c.Plex.Token = "plex-token-value"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.AttachShared(ctx, backend, SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AttachShared(ctx, backend, SharedOptions{PollInterval: -1, NoSeed: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(backend.doc.Doc, sealedPrefixV2) {
		t.Fatal("shared settings written in v2 before the upgrade")
	}
	if err := a.UpgradeCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	doc := backend.doc.Doc
	if strings.Contains(doc, sealedPrefix) || !strings.Contains(doc, sealedPrefixV2) || strings.Contains(doc, "pass-a") || !strings.Contains(doc, credentialFormatKey) {
		t.Fatalf("shared settings not upgraded:\n%s", doc)
	}
	if backend.doc.Version != 2 || a.SharedVersion() != 2 {
		t.Fatalf("version %d/%d", backend.doc.Version, a.SharedVersion())
	}
	// A node not yet upgraded reads v2 shared settings with the cluster key.
	if err := b.RefreshShared(ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.Get().Usenet.Providers[0]; got.Password != "pass-a" || got.Username != "user-a" {
		t.Fatalf("node B: %+v", got)
	}
	// A second upgrade (another node) finds nothing to do.
	if err := b.UpgradeCredentials(ctx); err != nil || backend.doc.Version != 2 {
		t.Fatalf("second upgrade: %v version %d", err, backend.doc.Version)
	}
	// A repointed host in the shared document does not open on any node.
	var m map[string]any
	_ = json.Unmarshal([]byte(doc), &m)
	obj(m, "usenet", "providers", 0)["host"] = "evil.test"
	bad, _ := json.Marshal(m)
	backend.mu.Lock()
	backend.doc.Doc, backend.doc.Version = string(bad), 3
	backend.mu.Unlock()
	if err := b.RefreshShared(ctx); err == nil {
		t.Fatal("repointed shared credential opened")
	}
	if b.Get().Usenet.Providers[0].Host != "news.test" {
		t.Fatal("refused shared settings were applied")
	}
}

func TestDataDirWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if w := DataDirWarning(dir); w != "" {
		t.Fatalf("private directory warned: %q", w)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w := DataDirWarning(dir)
	if !strings.Contains(w, "0755") || strings.Contains(w, dir) {
		t.Fatalf("warning: %q", w)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatal("permissions changed")
	}
}
