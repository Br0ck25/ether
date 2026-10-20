package episodeorder

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// A preference reset on one node drops the other node's cached layouts.
func TestPreferenceChangeDropsOtherNodeCache(t *testing.T) {
	db := store.OpenTest(t)
	a, b := New(db, nil, nil), New(db, nil, nil)
	hub := clusterbus.NewHub()
	a.SetBus(hub.Node("a"))
	b.SetBus(hub.Node("b"))
	defer a.SetBus(nil)
	defer b.SetBus(nil)
	b.mu.Lock()
	b.cache["k"] = cachedLayout{expires: time.Now().Add(time.Minute)}
	b.mu.Unlock()
	if err := a.Reset(context.Background(), 1, core.ShowKey(1399)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.cache)
		b.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("node B kept its cached layout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
