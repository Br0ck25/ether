package config

import (
	"os"
	"strings"
	"testing"
)

func TestDebridDefaultsAndSealedKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c := s.Get().Debrid; !c.CachedOnly || !c.RemoveUncached || c.EnabledProviders() {
		t.Fatalf("defaults %+v", c)
	}
	if err := s.Update(func(c *Config) error {
		c.Debrid.Providers = []DebridProvider{{ID: "rd", Kind: DebridRealDebrid, APIKey: "debrid-secret-key", Enabled: true}}
		c.Indexers = []Indexer{{ID: "tz", URL: "https://tz", Protocol: "torrent"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "debrid-secret-key") {
		t.Fatal("debrid API key stored in clear text")
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := s2.Get()
	if c.Debrid.Providers[0].APIKey != "debrid-secret-key" || !c.Debrid.EnabledProviders() || !c.Indexers[0].IsTorrent() {
		t.Fatalf("reload %+v", c.Debrid)
	}
	// An existing config without a debrid section keeps the defaults.
	if err := os.WriteFile(s.Path(), []byte(`{"server":{"port":32400}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c := s3.Get().Debrid; !c.CachedOnly || !c.RemoveUncached {
		t.Fatalf("legacy config defaults %+v", c)
	}
}
