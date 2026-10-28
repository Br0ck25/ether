package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKeygenNeverOverwritesAKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(path, []byte("ETHER_UPDATE_PRIVATE_KEY=existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdKeygen([]string{"-out", path}); err == nil {
		t.Fatal("keygen replaced an existing key file")
	}
	if b, _ := os.ReadFile(path); string(b) != "ETHER_UPDATE_PRIVATE_KEY=existing\n" {
		t.Fatalf("existing key file changed: %q", b)
	}
}

func TestKeygenWritesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.txt")
	if err := cmdKeygen([]string{"-out", path}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "ETHER_UPDATE_PRIVATE_KEY=") {
		t.Fatalf("key file %q, %v", b, err)
	}
	if _, err := loadPrivateKey(path); err != nil {
		t.Fatalf("generated key file does not load: %v", err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
		}
	}
}
