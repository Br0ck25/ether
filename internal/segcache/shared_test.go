package segcache

import (
	"bytes"
	"context"
	"github.com/braedonsaunders/ether/internal/store"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSharedCacheCrossNode(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	a, err := OpenShared(dir, 1024, db, "node-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenShared(dir, 1024, db, "node-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = a.Put("film", 0, []byte("same bytes")); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.Get("film", 0); !ok || string(got) != "same bytes" {
		t.Fatalf("peer miss: %q %v", got, ok)
	}
	if !b.Has("film", 0) || b.ReleaseBytes("film") != 10 {
		t.Fatal("peer index not shared")
	}
	if err = a.Pin("film", "owner"); err != nil {
		t.Fatal(err)
	}
	b.SetBudget(1)
	if !a.Has("film", 0) {
		t.Fatal("peer evicted owner pin")
	}
	if err = b.Unpin("film", "owner"); err != nil {
		t.Fatal(err)
	}
	b.Sweep()
	if a.Has("film", 0) {
		t.Fatal("shared unpin did not allow eviction")
	}
}
func TestSharedCachePinsAreNodeOwned(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	a, err := OpenShared(dir, 1024, db, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenShared(dir, 1024, db, "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Put("tv", 0, []byte("episode")); err != nil {
		t.Fatal(err)
	}
	if err = a.Pin("tv", "playback:live"); err != nil {
		t.Fatal(err)
	}
	if err = b.Unpin("tv", "playback:live"); err != nil {
		t.Fatal(err)
	}
	b.SetBudget(1)
	if !b.Has("tv", 0) {
		t.Fatal("node b removed node a live protection")
	}
	if _, err = db.Exec(`UPDATE segcache_pins SET expires=1`); err != nil {
		t.Fatal(err)
	}
	b.Sweep()
	if a.Has("tv", 0) {
		t.Fatal("crashed node pin never expired")
	}
}
func TestSharedCacheConcurrentWritesAndMissingMount(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	a, err := OpenShared(dir, 4096, db, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenShared(dir, 4096, db, "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := a
			if i%2 == 0 {
				c = b
			}
			if err := c.Put("same", 0, bytes.Repeat([]byte{byte(i)}, 64)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if n, _ := a.Usage(); n != 64 {
		t.Fatalf("double counted concurrent puts: %d", n)
	}
	if data, ok := b.Get("same", 0); !ok || len(data) != 64 {
		t.Fatal("published file damaged")
	}
	if _, err = OpenShared(t.TempDir(), 4096, db, "wrong-mount", nil); err == nil {
		t.Fatal("accepted different mount")
	}
	if err = os.Rename(filepath.Join(dir, ".ether-shared-cache"), filepath.Join(dir, "marker-away")); err != nil {
		t.Fatal(err)
	}
	if err = a.Put("missing", 0, []byte("no fallback")); err == nil {
		t.Fatal("wrote to unmounted directory")
	}
	if a.Has("same", 0) {
		t.Fatal("served unavailable mount")
	}
}

// A standalone NAS uses a persistent database on the server, never on the share.
func TestStandaloneNASReopenAndIndependentServerRejection(t *testing.T) {
	ctx := context.Background()
	local, nas := t.TempDir(), t.TempDir()
	db, err := store.Open(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	c, err := OpenShared(nas, 1024, db, "standalone", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put("episode", 0, []byte("cached episode")); err != nil {
		t.Fatal(err)
	}
	if err := c.Pin("episode", "owner"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	db.Close()
	db, err = store.Open(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err = OpenShared(nas, 1024, db, "standalone", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, ok := c.Get("episode", 0); !ok || string(got) != "cached episode" {
		t.Fatalf("restart lost media: %q %v", got, ok)
	}
	c.SetBudget(1)
	if !c.Has("episode", 0) {
		t.Fatal("restart lost owner pin")
	}
	other, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := OpenShared(nas, 1024, other, "standalone", nil); err == nil {
		t.Fatal("unrelated server adopted pool")
	}
	if _, err := os.Stat(filepath.Join(nas, "ether.db")); !os.IsNotExist(err) {
		t.Fatalf("database on NAS: %v", err)
	}
}
