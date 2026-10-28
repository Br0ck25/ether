package debrid

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

type failingDurableCache struct{ core.SegmentCache }

func (f failingDurableCache) Put(string, int, []byte) error  { return errors.New("disk full") }
func (f failingDurableCache) Get(string, int) ([]byte, bool) { return nil, false }
func (f failingDurableCache) Has(string, int) bool           { return false }
func TestDurablePrefetchFailsOnDiskWriteFailure(t *testing.T) {
	r := newEngineRig(t, 256<<10, -1)
	r.eng.cache = failingDurableCache{r.cache}
	if err := r.eng.DurablePrefetch(context.Background(), r.rel.ID, r.pl, 0, 256<<10); err == nil {
		t.Fatal("failed cache writes reported complete")
	}
}
func TestDurablePrefetchCoversEveryBlock(t *testing.T) {
	r := newEngineRig(t, 256<<10, -1)
	if err := r.eng.DurablePrefetch(context.Background(), r.rel.ID, r.pl, 0, 256<<10); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if !r.cache.Has(r.rel.ID, i) {
			t.Fatalf("missing block %d", i)
		}
	}
}
