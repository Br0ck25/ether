package debrid

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

type resolvingInner struct {
	fakeInner
	resolved int
}

func (r *resolvingInner) ResolveTorrent(_ context.Context, rel core.Release) (core.Release, error) {
	r.resolved++
	rel.InfoHash = testHash
	return rel, nil
}

func TestIndexerSearchFiltersAndOrdersTorrents(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	h.provs["torbox-0"].bulk = true
	uncached := strings.Repeat("99", 20)
	h.provs["torbox-0"].cached[testHash] = true
	inner := &fakeInner{results: []core.Release{
		{ID: "u1", Name: "Usenet"},
		{ID: "t-unc", Name: "Uncached", Protocol: core.ProtocolTorrent, InfoHash: uncached},
		{ID: "t-nohash", Name: "NoHash", Protocol: core.ProtocolTorrent},
		{ID: "t-cached", Name: "Cached", Protocol: core.ProtocolTorrent, InfoHash: testHash},
	}}
	x := NewIndexer(inner, h.svc)
	got, err := x.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Movie"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "t-cached,u1,t-nohash" {
		t.Fatalf("results %v", ids)
	}
	// With no provider, torrents disappear entirely and Usenet is untouched.
	h.svc.UpdateConfig(config.Debrid{})
	got, _ = x.Search(context.Background(), core.SearchRequest{})
	if len(got) != 1 || got[0].ID != "u1" {
		t.Fatalf("no provider: %+v", got)
	}
}

func TestIndexerFetchDispatch(t *testing.T) {
	h := newHarness(t, 1<<20, "realdebrid")
	h.cacheEverywhere(testHash)
	inner := &resolvingInner{}
	x := NewIndexer(inner, h.svc)
	ctx := context.Background()
	b, err := x.FetchNZB(ctx, core.Release{ID: "u", Name: "Usenet"})
	if err != nil || string(b) != "<nzb/>" || inner.fetched.Load() != 1 {
		t.Fatalf("usenet: %q %v", b, err)
	}
	// A .torrent-only result is resolved to a hash through the inner indexer.
	b, err = x.FetchNZB(ctx, core.Release{ID: "t", Name: "Movie.2024", Protocol: core.ProtocolTorrent, NZBURL: "http://x/1.torrent"})
	if err != nil || !IsPayload(b) || inner.resolved != 1 {
		t.Fatalf("torrent: %v resolved %d", err, inner.resolved)
	}
	h.svc.UpdateConfig(config.Debrid{})
	if _, err := x.FetchNZB(ctx, core.Release{ID: "t", Protocol: core.ProtocolTorrent, InfoHash: testHash}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("no provider: %v", err)
	}
}

// streamingInner answers one fast indexer at once and a slow one later.
type streamingInner struct {
	fakeInner
	fast, slow []core.Release
	release    chan struct{}
	checksSeen chan int
	h          *harness
}

func (s *streamingInner) SearchWith(ctx context.Context, _ core.SearchRequest, arrived func([]core.Release)) ([]core.Release, error) {
	arrived(s.fast)
	// Let the fast batch's cache check run while the slow indexer works.
	deadline := time.After(2 * time.Second)
	for s.h.provs["torbox-0"].callCount("check") == 0 {
		select {
		case <-deadline:
			return nil, errors.New("no early cache check")
		case <-time.After(5 * time.Millisecond):
		}
	}
	arrived(s.slow)
	return append(append([]core.Release(nil), s.fast...), s.slow...), nil
}

func TestIndexerChecksCacheAsIndexersAnswer(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	h.provs["torbox-0"].bulk = true
	other := strings.Repeat("cd", 20)
	h.provs["torbox-0"].cached[testHash] = true
	inner := &streamingInner{h: h,
		fast: []core.Release{{ID: "fast", Name: "Fast", Protocol: core.ProtocolTorrent, InfoHash: testHash}},
		slow: []core.Release{{ID: "slow", Name: "Slow", Protocol: core.ProtocolTorrent, InfoHash: other}, {ID: "u", Name: "Usenet"}},
	}
	got, err := NewIndexer(inner, h.svc).Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Movie"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	// The uncached slow torrent is dropped, the cached fast one leads.
	if strings.Join(ids, ",") != "fast,u" {
		t.Fatalf("results %v", ids)
	}
	if n := h.provs["torbox-0"].callCount("check"); n != 2 {
		t.Fatalf("cache checks = %d, want one per answering indexer", n)
	}
}
