package stream

import (
	"context"
	"github.com/braedonsaunders/ether/internal/segcache"
	"testing"
)

func TestDurablePrefetchRequiresRetainedSegments(t *testing.T) {
	c, err := segcache.Open(t.TempDir(), 1<<30, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f := newFakeFetcher()
	doc := post(t, f, 100_000, postFile{name: "durable.mkv", data: mkvBytes(1, 1_000_000)})
	e := newTestEngine(t, f, c, Options{})
	if err = e.DurablePrefetch(context.Background(), "durable", doc, 0, 500_000); err != nil {
		t.Fatal(err)
	}
	c.SetPolicy(segcache.Policy{Mode: "off"})
	if err = e.DurablePrefetch(context.Background(), "disabled", doc, 0, 500_000); err == nil {
		t.Fatal("a bypassed cache reported durable success")
	}
	if err = e.DurablePrefetch(context.Background(), "disabled", doc, -10, 8); err == nil {
		t.Fatal("negative offset skipped retained-span verification")
	}
}
