package debrid

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// A source's cached markers stand in for a provider's own answer: a cached
// hint ranks the torrent as known cached, and with CachedOnly a not-cached
// hint skips the provider without adding the torrent. A provider's own
// answer always wins, and a hint for another service changes nothing.
func TestApplyHints(t *testing.T) {
	h := newHarness(t, 1<<20, config.DebridRealDebrid, config.DebridTorBox)
	rd, tb := h.provs["realdebrid-0"], h.provs["torbox-1"]
	rel := torrentRelease("r1")

	hinted := rel
	hinted.DebridCached = []string{config.DebridAllDebrid}
	h.svc.ApplyHints(t.Context(), []core.Release{hinted})
	if h.svc.KnownCached(testHash) {
		t.Fatal("a hint for a service without an account counted")
	}

	hinted.DebridCached = []string{config.DebridTorBox}
	h.svc.ApplyHints(t.Context(), []core.Release{hinted})
	if !h.svc.KnownCached(testHash) {
		t.Fatal("cached hint ignored")
	}

	// Real-Debrid reported as not holding it: skipped without an add;
	// TorBox, hinted cached and holding it, serves the release.
	hinted.DebridUncached = []string{config.DebridRealDebrid}
	h.svc.ApplyHints(t.Context(), []core.Release{hinted})
	tb.mu.Lock()
	tb.cached[testHash] = true
	tb.mu.Unlock()
	if _, err := h.svc.Acquire(t.Context(), rel); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if n := rd.callCount("acquire"); n != 0 {
		t.Fatalf("hinted-uncached provider was asked %d times", n)
	}

	// TorBox's own answer (cached, now recorded) wins over a later hint.
	contrary := rel
	contrary.DebridUncached = []string{config.DebridTorBox}
	h.svc.ApplyHints(t.Context(), []core.Release{contrary})
	if !h.svc.KnownCached(testHash) {
		t.Fatal("a hint replaced the provider's own answer")
	}
}

// Without CachedOnly an uncached torrent is left to download, so a
// not-cached hint must not stop the add.
func TestApplyHintsUncachedIgnoredWhenDownloading(t *testing.T) {
	h := newHarness(t, 1<<20, config.DebridRealDebrid)
	cfg := h.svc.Config()
	cfg.CachedOnly = false
	h.svc.UpdateConfig(cfg)
	rd := h.provs["realdebrid-0"]
	rel := torrentRelease("r1")
	rel.DebridUncached = []string{config.DebridRealDebrid}
	h.svc.ApplyHints(t.Context(), []core.Release{rel})
	h.cacheEverywhere(testHash)
	if _, err := h.svc.Acquire(t.Context(), rel); err != nil || rd.callCount("acquire") != 1 {
		t.Fatalf("acquire: %v (calls %d)", err, rd.callCount("acquire"))
	}
}
