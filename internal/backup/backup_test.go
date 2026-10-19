package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSnapshotRestoreIntegrityAndRetention(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, e := store.Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	config := filepath.Join(dir, "config.json")
	if e = os.WriteFile(config, []byte(`{"server":{"friendlyName":"test"},"plex":{"token":"private"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	s := New(db, config, filepath.Join(dir, "backups"), 1)
	if e = s.Configure(ctx, Settings{Retain: 1, IntervalHours: 0}); e != nil {
		t.Fatal(e)
	}
	first, e := s.Create(ctx)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.Create(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if snap.Name == first.Name {
		t.Fatal("snapshot collision")
	}
	list, e := s.List()
	if e != nil || len(list) != 1 {
		t.Fatalf("retention %+v %v", list, e)
	}
	f, e := os.Open(filepath.Join(s.dir, snap.Name))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	target := filepath.Join(dir, "restored")
	if e = RestoreBundle(ctx, f, target); e != nil {
		t.Fatal(e)
	}
	if e = validateDB(ctx, filepath.Join(target, "ether.db")); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(target, "config.json"))
	if e != nil || !strings.Contains(string(b), "private") {
		t.Fatalf("config %s %v", b, e)
	}
	st, e := os.Stat(filepath.Join(s.dir, snap.Name))
	if e != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0600) {
		t.Fatal("backup exposes credentials")
	}
	f.Seek(0, 0)
	if e = RestoreBundle(ctx, f, target); e == nil {
		t.Fatal("overwrote existing restore")
	}
}
func TestRejectTraversalAndTamperedBundle(t *testing.T) {
	for _, name := range []string{"../escape", "ether.db"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			files := map[string]string{name: "corrupted", "config.json": `{"server":{}}`, "manifest.json": ""}
			if name == "ether.db" {
				manifest := Manifest{Version: 1, SHA256: map[string]string{"ether.db": "wrong", "config.json": "wrong"}}
				b, _ := json.Marshal(manifest)
				files["manifest.json"] = string(b)
			}
			for path, content := range files {
				tw.WriteHeader(&tar.Header{Name: path, Mode: 0600, Size: int64(len(content))})
				tw.Write([]byte(content))
			}
			tw.Close()
			gz.Close()
			dest := filepath.Join(t.TempDir(), "target")
			if e := RestoreBundle(context.Background(), &buf, dest); e == nil {
				t.Fatal("unsafe backup accepted")
			}
			if _, e := os.Stat(dest); !os.IsNotExist(e) {
				t.Fatal("failed restore left destination")
			}
		})
	}
}

func TestEncryptedCredentialsSurvivePortableBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Update(func(c *config.Config) error { c.Plex.Token = "backup-test-private"; return nil }); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, cfg.Path(), filepath.Join(dir, "backups"), 2)
	snap, err := s.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(s.dir, snap.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	target := filepath.Join(dir, "restored")
	if err = RestoreBundle(ctx, f, target); err != nil {
		t.Fatal(err)
	}
	restored, err := config.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Get().Plex.Token != "backup-test-private" {
		t.Fatal("restored credentials differ")
	}
	raw, _ := os.ReadFile(filepath.Join(target, "config.json"))
	if strings.Contains(string(raw), "backup-test-private") {
		t.Fatal("restore exposed credentials")
	}
}
