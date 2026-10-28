package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCredentialsEncryptedAndPortable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(c *Config) error {
		c.Plex.Token = "test-sensitive-token"
		c.Usenet.Providers = []UsenetProvider{{ID: "fake", Username: "test-user", Password: "test-password"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"test-sensitive-token", "test-user", "test-password"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("plaintext secret persisted")
		}
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().Plex.Token != "test-sensitive-token" || s.Get().Usenet.Providers[0].Password != "test-password" {
		t.Fatal("round trip failed")
	}
	copyDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(copyDir, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(copyDir); err == nil {
		t.Fatal("missing key accepted")
	}
	key, err := os.ReadFile(filepath.Join(dir, CredentialKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(copyDir, CredentialKeyFile), key, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(copyDir)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Get().Plex.Token != s.Get().Plex.Token {
		t.Fatal("portable restore failed")
	}
	got := s.Get()
	got.Usenet.Providers[0].Password = "mutated"
	if s.Get().Usenet.Providers[0].Password == "mutated" {
		t.Fatal("snapshot aliases config")
	}
}
func TestPlainConfigMigrationAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"plex":{"token":"migration-test-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().Plex.Token != "migration-test-token" {
		t.Fatal("migration failed")
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "migration-test-token") {
		t.Fatal("migration did not seal")
	}
	if err = os.WriteFile(filepath.Join(dir, CredentialKeyFile), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestIndexerSecretSettingsSealed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(c *Config) error {
		c.Indexers = []Indexer{{ID: "site", Kind: IndexerKindCardigann, Definition: "fake",
			Settings:       map[string]string{"sort": "seeders-visible"},
			SecretSettings: map[string]string{"passkey": "test-passkey-value", "cookie": "test-cookie-value"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"test-passkey-value", "test-cookie-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("plaintext secret setting %q persisted", secret)
		}
	}
	if !strings.Contains(string(raw), "seeders-visible") {
		t.Fatal("ordinary setting should stay readable")
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get().Indexers[0]
	if got.SecretSettings["passkey"] != "test-passkey-value" || got.SecretSettings["cookie"] != "test-cookie-value" || !got.IsBuiltin() {
		t.Fatalf("round trip failed: %+v", got)
	}
}
