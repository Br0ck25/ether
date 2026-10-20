package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomicReplacesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	// A stale fixed-name temp from an older build must not interfere.
	if err := os.WriteFile(p+".tmp", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "two" {
		t.Fatalf("content %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "config.json" && e.Name() != "config.json.tmp" {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestWriteFileAtomicFailureLeavesOriginal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := writeFileAtomic(p, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Renaming onto a non-empty directory fails; the original must survive
	// and no temp file may be left behind.
	target := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(target, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("bad"), 0o600); err == nil {
		t.Fatal("expected rename onto a directory to fail")
	}
	if b, _ := os.ReadFile(p); string(b) != "good" {
		t.Fatalf("original changed: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("unexpected files %v", names)
	}
}
