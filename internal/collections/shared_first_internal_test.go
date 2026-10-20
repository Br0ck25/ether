package collections

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// Over the cap, titles meeting a shared rule keep their places ahead of
// per-viewer candidates, whatever the sort order put first.
func TestSharedFirstKeepsSharedMatchesUnderTheCap(t *testing.T) {
	var kept []*cand
	unshared := map[core.RatingKey]bool{}
	for i := 1; i <= 6; i++ {
		k := core.MovieKey(int64(i))
		kept = append(kept, &cand{e: &entry{key: k}})
		if i <= 4 { // the four best-sorted are only per-viewer candidates
			unshared[k] = true
		}
	}
	out := sharedFirst(kept, unshared, 3)
	if out[0].e.key != core.MovieKey(5) || out[1].e.key != core.MovieKey(6) || out[2].e.key != core.MovieKey(1) {
		t.Fatalf("order %v %v %v", out[0].e.key, out[1].e.key, out[2].e.key)
	}
	if len(unshared) != 1 || !unshared[core.MovieKey(1)] {
		t.Fatalf("unshared after cap: %v", unshared)
	}
	if got := sharedFirst(kept[:2], unshared, 3); len(got) != 2 {
		t.Fatalf("under the cap reordered: %v", got)
	}
}
