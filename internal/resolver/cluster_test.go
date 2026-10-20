package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/store"
)

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Bad marks, verifications, availability and reputation written on one node
// are seen by the other node's caches.
func TestResolverCachesFollowOtherNode(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	a := New(&fakeIndexer{}, nil, db, testCfg(), Options{})
	b := New(&fakeIndexer{}, nil, db, testCfg(), Options{})
	defer a.Close()
	defer b.Close()
	hub := clusterbus.NewHub()
	a.SetBus(hub.Node("a"))
	b.SetBus(hub.Node("b"))

	// B verified a release and cached availability; then A marks it bad
	// and flips availability.
	b.mu.Lock()
	b.verified["rel-1"] = time.Now()
	b.mu.Unlock()
	b.setAvailability(ctx, 42, true, time.Hour)
	if err := a.MarkBad(ctx, "rel-1", "missing articles"); err != nil {
		t.Fatal(err)
	}
	a.setAvailability(ctx, 42, false, time.Hour)
	a.recordReputation(ctx, "idx", true)
	a.recordReputation(ctx, "idx", true)
	a.recordReputation(ctx, "idx", false)

	waitUntil(t, "B sees bad mark", func() bool { return b.isBad("rel-1") })
	b.mu.RLock()
	_, stillVerified := b.verified["rel-1"]
	b.mu.RUnlock()
	if stillVerified {
		t.Fatal("B kept the bad release's health verification")
	}
	waitUntil(t, "B re-reads availability", func() bool {
		e, ok := b.getAvailability(ctx, 42)
		return ok && !e.available
	})
	waitUntil(t, "B reloads reputation", func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		r := b.reps["idx"]
		return r.successes == 2 && r.failures == 1
	})
	// A bus resync drops everything and reloads what is persisted.
	hub.Resync("b")
	waitUntil(t, "resync reload", func() bool { return b.isBad("rel-1") })
}
