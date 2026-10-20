package newznab

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestTorrentDescriptorReusePreservesEpisodeAndRanking(t *testing.T) {
	doc, hash := sampleTorrent()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(doc)
	}))
	defer srv.Close()
	c := New(config.Indexer{ID: "site", URL: srv.URL, Enabled: true, DailyGrabs: 1}, nil, Options{})
	r := core.Release{ID: "pack", NZBURL: srv.URL + "/pack.torrent", Name: "Space Pilot Season 1", Target: &core.ReleaseTarget{Season: 1, Episode: 1}}
	first, err := c.ResolveTorrent(t.Context(), r)
	if err != nil || first.InfoHash != hash {
		t.Fatalf("first descriptor: %+v %v", first, err)
	}
	r.Target = &core.ReleaseTarget{Season: 1, Episode: 2, ShowYear: 2005, EpisodeTitle: "Stone and Sky"}
	r.Score = 97
	second, err := c.ResolveTorrent(t.Context(), r)
	if err != nil || second.InfoHash != hash || second.Target != r.Target || second.Score != r.Score || hits.Load() != 1 {
		t.Fatalf("descriptor repeated a grab or replaced request identity: %+v err=%v hits=%d", second, err, hits.Load())
	}
}

func TestTorrentDescriptorExpiresAndConfigChangeInvalidatesIt(t *testing.T) {
	doc, _ := sampleTorrent()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(doc) }))
	defer srv.Close()
	now := time.Now()
	cfg := config.Indexer{ID: "site", URL: srv.URL, Enabled: true}
	c := New(cfg, nil, Options{Now: func() time.Time { return now }})
	r := core.Release{ID: "r", NZBURL: srv.URL + "/r.torrent"}
	for i := 0; i < 2; i++ {
		if _, err := c.ResolveTorrent(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("uncached retry: %d downloads", hits.Load())
	}
	cfg.APIKey = "replacement"
	c.UpdateConfig(cfg)
	if _, err := c.ResolveTorrent(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatal("new indexer configuration reused an old descriptor")
	}
	now = now.Add(torrentDescriptorTTL + time.Second)
	if _, err := c.ResolveTorrent(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Fatal("expired descriptor was reused")
	}
}
