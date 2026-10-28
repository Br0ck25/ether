package transcode

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMuxOutputReaderAllowsRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.m3u8")
	if err := os.WriteFile(path, []byte("old playlist"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openMuxOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(path+".tmp", []byte("new playlist"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Delete sharing permits renaming an open file. Replacing an open
	// destination is a separate Windows restriction; final playlist
	// recovery handles that case after the producer exits.
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatalf("reader blocked rename: %v", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(f)
	if err != nil || string(old) != "old playlist" {
		t.Fatalf("held reader = %q, %v", old, err)
	}
	newData, err := readMuxOutput(path)
	if err != nil || string(newData) != "new playlist" {
		t.Fatalf("replaced playlist = %q, %v", newData, err)
	}
}
