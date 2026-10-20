package debrid

import (
	"context"
	"errors"
	"strings"
	"testing"

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
