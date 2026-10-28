//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withUmask runs the test under the common 022 umask so group and other
// bits would show up if a file were created too open.
func withUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestOpenCreatesOwnerOnlyDataDir(t *testing.T) {
	withUmask(t)
	parent := filepath.Join(t.TempDir(), "share")
	dir := filepath.Join(parent, "ether")
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, dir); got != 0o700 {
		t.Fatalf("data dir mode = %o, want 700", got)
	}
	if got := perm(t, parent); got != 0o755 {
		t.Fatalf("parent mode = %o, want 755", got)
	}
	for _, f := range []string{"config.json", CredentialKeyFile} {
		if got := perm(t, filepath.Join(dir, f)); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", f, got)
		}
	}
}

func TestOpenKeepsExistingDataDirMode(t *testing.T) {
	withUmask(t)
	dir := filepath.Join(t.TempDir(), "ether")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, dir); got != 0o750 {
		t.Fatalf("existing data dir mode = %o, want 750 kept", got)
	}
}

func TestEnsureDataDirRejectsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ether")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDataDir(p); err == nil {
		t.Fatal("EnsureDataDir on a regular file succeeded")
	}
}
