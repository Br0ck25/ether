package debrid

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// cachedOnlyOff is the owner switching "Cached only" off.
func cachedOnlyOff(h *harness) {
	cfg := h.svc.Config()
	cfg.CachedOnly = false
	h.svc.UpdateConfig(cfg)
}

// With cached-only off, torrents the provider reports uncached stay in the
// results (after cached ones) so playback can send them to download.
func TestIndexerKeepsUncachedTorrentsWhenDownloadsAllowed(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	h.provs["torbox-0"].bulk = true
	h.provs["torbox-0"].cached[testHash] = true
	cachedOnlyOff(h)
	uncached := strings.Repeat("99", 20)
	inner := &fakeInner{results: []core.Release{
		{ID: "t-unc", Name: "Uncached", Protocol: core.ProtocolTorrent, InfoHash: uncached},
		{ID: "t-cached", Name: "Cached", Protocol: core.ProtocolTorrent, InfoHash: testHash},
	}}
	got, err := NewIndexer(inner, h.svc).Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Movie"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "t-cached,t-unc" {
		t.Fatalf("results %v, want the uncached torrent kept after the cached one", ids)
	}
}

// A provider with a bulk availability check (TorBox) reports the torrent
// uncached during the search; with cached-only off, playback must still send
// it to download, once per title, and play it when the download is done.
func TestAcquireSendsKnownUncachedTorrentToDownload(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	tb := h.provs["torbox-0"]
	tb.bulk = true
	cachedOnlyOff(h)
	other := strings.Repeat("cd", 20)
	if cached, definitive := h.svc.CheckCached(t.Context(), []string{testHash, other}); !definitive || cached[testHash] {
		t.Fatalf("availability check: %v %v", cached, definitive)
	}

	// Background checks (detail-page warm-up) never start downloads.
	bg := core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error { return nil })
	if _, err := h.svc.Acquire(bg, torrentRelease("r1")); !errors.Is(err, core.ErrNotCached) || tb.downloading != 0 {
		t.Fatalf("background check: err=%v downloading=%d", err, tb.downloading)
	}

	first := torrentRelease("r1")
	first.TitleIDs = core.IDs{TMDB: 603}
	if _, err := h.svc.Acquire(t.Context(), first); !errors.Is(err, core.ErrDownloadQueued) {
		t.Fatalf("first play: %v, want ErrDownloadQueued", err)
	}
	if tb.downloading != 1 {
		t.Fatalf("downloads started: %d, want 1", tb.downloading)
	}

	// Another release of the same title is not sent as well.
	second := torrentRelease("r2")
	second.InfoHash = other
	second.TitleIDs = first.TitleIDs
	if _, err := h.svc.Acquire(t.Context(), second); errors.Is(err, core.ErrDownloadQueued) || tb.downloading != 1 {
		t.Fatalf("second release: err=%v downloading=%d", err, tb.downloading)
	}

	// The download finished: the queued release now plays, from the
	// torrent already on the account.
	tb.mu.Lock()
	tb.cached[testHash] = true
	tb.mu.Unlock()
	if _, err := h.svc.Acquire(t.Context(), first); err != nil {
		t.Fatalf("after download: %v", err)
	}
	if tb.adds != 1 {
		t.Fatalf("torrents added: %d, want 1 (the queued one reused)", tb.adds)
	}
}

// Cached-only on keeps the old behaviour: nothing is sent to download.
func TestAcquireCachedOnlyNeverDownloads(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	tb := h.provs["torbox-0"]
	tb.bulk = true
	h.svc.CheckCached(t.Context(), []string{testHash})
	if _, err := h.svc.Acquire(t.Context(), torrentRelease("r1")); !errors.Is(err, core.ErrNotCached) || errors.Is(err, core.ErrDownloadQueued) || tb.downloading != 0 {
		t.Fatalf("cached-only: err=%v downloading=%d", err, tb.downloading)
	}
}
