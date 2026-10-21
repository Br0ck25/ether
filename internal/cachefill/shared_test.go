package cachefill

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/segcache"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
)

func TestSharedShelfLeaseFencesPinsAndManifest(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := segcache.OpenShared(t.TempDir(), 1024, db, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := NewSharedStore(db, "a"), NewSharedStore(db, "b")
	if err = a.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err = b.acquire(ctx); !errors.Is(err, errLease) {
		t.Fatalf("two workers admitted: %v", err)
	}
	if err = a.pin(ctx, c, "film", true); err != nil {
		t.Fatal(err)
	}
	if err = a.save(ctx, []byte(`{"items":[]}`), Status{State: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err = b.pause(true); err != nil {
		t.Fatal(err)
	}
	if _, st, err := a.load(ctx); err != nil || !st.Paused {
		t.Fatalf("pause not shared: %+v %v", st, err)
	}
	if _, err = db.Exec(`UPDATE cachefill_shared SET expires=0`); err != nil {
		t.Fatal(err)
	}
	if err = b.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.pin(ctx, c, "film", false); !errors.Is(err, errLease) {
		t.Fatalf("former worker unpinned new shelf: %v", err)
	}
	if err = a.save(ctx, []byte(`{}`), Status{}); !errors.Is(err, errLease) {
		t.Fatalf("former worker rewrote manifest: %v", err)
	}
	a.release()
	if err = b.renew(ctx); err != nil {
		t.Fatalf("former worker released newer lease: %v", err)
	}
	if len(c.Pins()["film"]) != 1 {
		t.Fatal("shelf lost protection")
	}
	b.release()
}
func TestSharedShelfHandoffReusesCompletedSources(t *testing.T) {
	m, _, eng, res := harness(t)
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	c, err := segcache.OpenShared(dir, 1<<30, db, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.deps.Cache = c
	eng.cache = c
	m.deps.Shared = NewSharedStore(db, "a")
	if err = m.runPass(ctx); err != nil {
		t.Fatal(err)
	}
	d := m.deps
	d.Shared = NewSharedStore(db, "b")
	other, err := New(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	other.Wake()
	if err = other.runPass(ctx); err != nil {
		t.Fatal(err)
	}
	if res.calls.Load() != 2 || other.Status().Ready != 2 {
		t.Fatalf("handoff refetched shelf: calls %d ready %d", res.calls.Load(), other.Status().Ready)
	}
}

func TestStandaloneNASShelfSurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	local, nas := t.TempDir(), t.TempDir()
	db, err := store.Open(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	c, err := segcache.OpenShared(nas, 1024, db, "standalone", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := NewSharedStore(db, "standalone")
	if err := a.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.save(ctx, []byte(`{"items":[]}`), Status{State: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := a.pause(true); err != nil {
		t.Fatal(err)
	}
	a.release()
	c.Close()
	db.Close()
	db, err = store.Open(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := NewSharedStore(db, "standalone")
	if err := b.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	defer b.release()
	doc, status, err := b.load(ctx)
	if err != nil || string(doc) != `{"items":[]}` || !status.Paused {
		t.Fatalf("shelf restart: %s %+v %v", doc, status, err)
	}
}
