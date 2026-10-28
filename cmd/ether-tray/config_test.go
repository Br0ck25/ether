package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestOpenServerConfigNeverCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Ether")
	if _, err := openServerConfig(dir); err == nil {
		t.Fatal("missing config opened without error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("tray created the server data dir: %v", err)
	}
}

// Reading an existing, unsealed config must not rewrite it or mint a
// credential key in the server's data directory.
func TestOpenServerConfigNeverWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	doc := []byte(`{"server":{"port":32500},"updates":{"channel":"beta"}}`)
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := openServerConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 32500 || c.Updates.Channel != "beta" {
		t.Fatalf("read %+v %+v", c.Server, c.Updates)
	}
	if got, _ := os.ReadFile(path); string(got) != string(doc) {
		t.Fatalf("config.json rewritten:\n%s", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("tray created files in the data dir: %v", entries)
	}
}

func TestServerBaseURLFollowsBindAddress(t *testing.T) {
	for _, tc := range []struct {
		srv  config.Server
		want string
	}{
		{config.Server{}, "http://127.0.0.1:32400"},
		{config.Server{Port: 32401, BindAddress: "0.0.0.0"}, "http://127.0.0.1:32401"},
		{config.Server{Port: 32400, BindAddress: "::"}, "http://127.0.0.1:32400"},
		{config.Server{Port: 32400, BindAddress: "192.168.1.20"}, "http://192.168.1.20:32400"},
		{config.Server{Port: 8080, BindAddress: "fd00::5"}, "http://[fd00::5]:8080"},
	} {
		if got := serverBaseURL(tc.srv); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.srv, got, tc.want)
		}
	}
}
