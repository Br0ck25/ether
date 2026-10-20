package safety

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/store"
)

// A policy saved on one node applies on the other after the invalidation.
func TestPolicyChangeReachesOtherNode(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	a, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	hub := clusterbus.NewHub()
	a.SetBus(hub.Node("a"))
	b.SetBus(hub.Node("b"))
	defer a.Close()
	defer b.Close()
	if err := a.Put(ctx, 7, Policy{StreamLimit: 2, RemoteDisabled: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.Get(7).StreamLimit != 2 {
		if time.Now().After(deadline) {
			t.Fatal("node B never saw the policy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !b.Get(7).RemoteDisabled {
		t.Fatalf("policy %+v", b.Get(7))
	}
}
