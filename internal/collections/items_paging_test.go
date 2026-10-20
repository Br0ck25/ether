package collections_test

import (
	"math"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// offset+limit must not overflow into a negative slice bound when a caller
// asks for "everything" with a huge limit.
func TestItemsHugeLimitDoesNotOverflow(t *testing.T) {
	e := newEnv(t)
	created, err := e.svc.CreateSpec(e.ctx, spec("Big Hits", "movie", catalogSource(), rl("votes", "gte", 9000)))
	if err != nil {
		t.Fatal(err)
	}
	key := core.SmartKey(mustInt(t, created.ID))
	all, err := e.svc.Items(e.ctx, owner, key, 0, 1000)
	if err != nil || all.Total < 3 {
		t.Fatalf("items: %+v %v", all, err)
	}
	got, err := e.svc.Items(e.ctx, owner, key, 1, math.MaxInt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != all.Total || len(got.Items) != all.Total-1 || got.Items[0].Key != all.Items[1].Key {
		t.Fatalf("huge limit page: %d items of %d", len(got.Items), got.Total)
	}
	// TMDB franchises page the same way (collection 10 has two members).
	fr, err := e.svc.Items(e.ctx, owner, core.CollectionKey(10), 1, math.MaxInt)
	if err != nil {
		t.Fatalf("franchise: %v", err)
	}
	if fr.Total != 2 || len(fr.Items) != 1 {
		t.Fatalf("franchise huge limit page: %d items of %d", len(fr.Items), fr.Total)
	}
}
