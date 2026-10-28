package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// inventoryIndexer reports a missing-sources error like debrid.Indexer.
type inventoryIndexer struct {
	fakeIndexer
	missing error
}

func (i *inventoryIndexer) MissingSources() error { return i.missing }

// Nothing to search is a setup problem: Resolve says so without searching
// and without recording the title as unavailable.
func TestResolveReportsMissingSources(t *testing.T) {
	idx := &inventoryIndexer{missing: core.ErrNoTorrentSources}
	svc := New(idx, fakeHealth{}, nil, testCfg(), Options{})
	ctx := context.Background()
	title := matrixTitle()
	_, err := svc.Resolve(ctx, core.ResolveRequest{Title: title, Client: webCaps()})
	if !errors.Is(err, core.ErrNoTorrentSources) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("err = %v", err)
	}
	if n := idx.searches.Load(); n != 0 {
		t.Fatalf("searched %d times with no sources", n)
	}
	if e, ok := svc.getAvailability(ctx, int64(title.Key)); ok && !e.available {
		t.Fatal("missing sources recorded the title as unavailable")
	}
	// Once a source exists the title resolves normally.
	idx.missing = nil
	idx.releases = movieReleases()
	if res, err := svc.Resolve(ctx, core.ResolveRequest{Title: title, Client: webCaps()}); err != nil || res == nil {
		t.Fatalf("after adding a source: %+v %v", res, err)
	}
}
