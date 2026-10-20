package newznab

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

type fakeBackend struct {
	searches, fetches, resolves int
	cfg                         config.Indexer
}

func (f *fakeBackend) Search(ctx context.Context, req core.SearchRequest, reserve func(context.Context, string) error) ([]core.Release, error) {
	if err := reserve(ctx, "hit"); err != nil {
		return nil, err
	}
	f.searches++
	return []core.Release{{GUID: "g1", Name: "Film.2020.1080p.WEB-DL", NZBURL: "https://site.invalid/nzb/1"}}, nil
}

func (f *fakeBackend) FetchNZB(ctx context.Context, r core.Release, reserve func(context.Context, string) error) ([]byte, error) {
	if err := reserve(ctx, "grab"); err != nil {
		return nil, err
	}
	f.fetches++
	return []byte("<nzb></nzb>"), nil
}

func (f *fakeBackend) ResolveTorrent(ctx context.Context, r core.Release, reserve func(context.Context, string) error) (core.Release, error) {
	if err := reserve(ctx, "grab"); err != nil {
		return r, err
	}
	f.resolves++
	r.InfoHash = "0123456789abcdef0123456789abcdef01234567"
	return r, nil
}

func TestBuiltinBackendSharesBudgetsCacheAndRouting(t *testing.T) {
	ctx := context.Background()
	fb := &fakeBackend{}
	dir := t.TempDir()
	cfg := config.Indexer{ID: "site", Name: "Site", URL: "https://site.invalid", Kind: config.IndexerKindCardigann, Definition: "fake", Enabled: true, DailyLimit: 1, DailyGrabs: 1}
	m := NewManager(nil, []config.Indexer{cfg}, Options{NZBDir: dir, Backend: func(c config.Indexer) (Backend, error) {
		fb.cfg = c
		return fb, nil
	}})
	req := core.SearchRequest{Type: core.TypeMovie, Title: "Film", Year: 2020}
	rels, err := m.Search(ctx, req)
	if err != nil || len(rels) != 1 {
		t.Fatalf("search: %v %v", rels, err)
	}
	r := rels[0]
	if r.Indexer != "Site" || r.ID == "" || r.Info.Height != 1080 || fb.cfg.Definition != "fake" {
		t.Fatalf("stamping = %+v", r)
	}
	if _, err := m.Search(ctx, req); err != nil || fb.searches != 1 {
		t.Fatalf("cache not used: %d %v", fb.searches, err)
	}
	if _, err := m.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Other"}); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("hit budget not enforced: %v", err)
	}
	body, err := m.FetchNZB(ctx, r)
	if err != nil || string(body) != "<nzb></nzb>" {
		t.Fatalf("fetch: %q %v", body, err)
	}
	if _, err := os.Stat(m.Clients()[0].nzbDiskPath(r.ID)); err != nil {
		t.Fatalf("NZB not kept on disk: %v", err)
	}
	// The stored NZB is reused without spending the (exhausted) grab budget.
	if _, err := m.FetchNZB(ctx, r); err != nil || fb.fetches != 1 {
		t.Fatalf("refetch: %d %v", fb.fetches, err)
	}
	torrent := core.Release{Indexer: "Site", Name: "T", Protocol: core.ProtocolTorrent, NZBURL: "https://site.invalid/t/1"}
	if _, err := m.ResolveTorrent(ctx, torrent); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("grab budget not enforced on torrents: %v", err)
	}
}

func TestBuiltinWithoutBackendFails(t *testing.T) {
	cfg := config.Indexer{ID: "site", Name: "Site", Kind: config.IndexerKindCardigann, Enabled: true}
	c := New(cfg, nil, Options{})
	if _, err := c.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "x"}); err == nil {
		t.Fatal("built-in indexer searched without a backend")
	}
}
