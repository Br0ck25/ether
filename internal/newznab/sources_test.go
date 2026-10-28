package newznab

import (
	"context"
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestManagerSourceInventory(t *testing.T) {
	m := NewManager(nil, nil, Options{})
	if err := m.MissingSources(); !errors.Is(err, core.ErrNoSources) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("no indexers: %v", err)
	}
	m.Reload([]config.Indexer{
		{ID: "nzb", Name: "nzb", URL: "http://nzb.invalid", Enabled: false},
		{ID: "tz", Name: "tz", URL: "http://tz.invalid", Protocol: "torrent", Enabled: false},
	})
	if err := m.MissingSources(); !errors.Is(err, core.ErrNoSources) {
		t.Fatalf("only disabled indexers: %v", err)
	}
	m.Reload([]config.Indexer{
		{ID: "nzb", Name: "nzb", URL: "http://nzb.invalid", Enabled: true},
		{ID: "tz", Name: "tz", URL: "http://tz.invalid", Protocol: "torrent", Enabled: false},
	})
	if err := m.MissingSources(); err != nil {
		t.Fatalf("enabled indexer: %v", err)
	}
	if m.HasTorrentIndexer() {
		t.Fatal("a disabled Torznab indexer counted as a torrent source")
	}
	// The dashboard status carries the same facts.
	rem, err := m.Remaining(context.Background())
	if err != nil || len(rem) != 2 || !rem[0].Enabled || rem[0].Torrent || rem[1].Enabled || !rem[1].Torrent {
		t.Fatalf("remaining: %+v %v", rem, err)
	}
	for _, ix := range []config.Indexer{
		{ID: "tz", Name: "tz", URL: "http://tz.invalid", Protocol: "torrent", Enabled: true},
		{ID: "addon", Name: "addon", URL: "http://addon.invalid/manifest.json", Kind: config.IndexerKindStremio, Enabled: true},
		{ID: "site", Name: "site", URL: "http://site.invalid", Kind: config.IndexerKindCardigann, Protocol: "torrent", Enabled: true},
	} {
		m.Reload([]config.Indexer{ix})
		if !m.HasTorrentIndexer() {
			t.Errorf("%s: not counted as a torrent source", ix.ID)
		}
	}
}
