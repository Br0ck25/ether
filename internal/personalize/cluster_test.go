package personalize

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/core"
)

// A home layout saved on one node is served by another after invalidation.
func TestHomeLayoutChangeReachesOtherNode(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	e2, err := New(Options{DB: h.db, Catalog: h.cat, Logger: quietLogger(), Now: h.clock.Now, Location: time.UTC, RefreshEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	hub := clusterbus.NewHub()
	h.e.SetBus(hub.Node("a"))
	e2.SetBus(hub.Node("b"))

	d, _ := h.e.HomeLayout(ctx)
	d.Viewers = []core.ViewerLayout{{UserID: 1, Pages: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{{Row: KindTrending, Status: "always", Slot: 2}}}}}}
	if _, err := h.e.SetHomeLayout(ctx, d); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := e2.HomeLayout(ctx)
		if len(got.Viewers) == 1 && got.Viewers[0].UserID == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node B still serves %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
