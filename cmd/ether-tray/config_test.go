package main

import (
	"os"
	"path/filepath"
	"testing"
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
