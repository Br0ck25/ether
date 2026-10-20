package collections

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
)

func bareService() *Service {
	return &Service{idx: &index{checkedAt: time.Now()}, members: map[int64]*memberSet{}, personal: map[string]personalEntry{}}
}

// A collection edit or index change on one node reaches the other's caches.
func TestCollectionChangesReachOtherNode(t *testing.T) {
	a, b := bareService(), bareService()
	hub := clusterbus.NewHub()
	a.SetBus(hub.Node("a"))
	b.SetBus(hub.Node("b"))
	defer a.SetBus(nil)
	defer b.SetBus(nil)
	b.mu.Lock()
	b.members[5] = &memberSet{}
	b.members[6] = &memberSet{}
	b.mu.Unlock()

	a.changed(5)
	a.notify("index")
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.mu.Lock()
		_, has5 := b.members[5]
		_, has6 := b.members[6]
		b.mu.Unlock()
		b.idx.mu.RLock()
		checked := b.idx.checkedAt
		b.idx.mu.RUnlock()
		if !has5 && checked.IsZero() {
			if !has6 {
				t.Fatal("unrelated collection dropped")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("has5=%v checked=%v", has5, checked)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
