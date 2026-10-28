package debrid

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// inventoryInner is an inner indexer that reports its configured sources.
type inventoryInner struct {
	fakeInner
	missing error
	torrent bool
}

func (i *inventoryInner) MissingSources() error   { return i.missing }
func (i *inventoryInner) HasTorrentIndexer() bool { return i.torrent }

func TestIndexerMissingSources(t *testing.T) {
	h := newHarness(t, 1<<20, "realdebrid")
	usenet := false
	inner := &inventoryInner{}
	x := NewIndexer(inner, h.svc)
	// Without the Usenet check wired, only "no indexers at all" is known.
	if err := x.MissingSources(); err != nil {
		t.Fatalf("unwired: %v", err)
	}
	x.SetUsenetReady(func() bool { return usenet })
	if err := x.MissingSources(); !errors.Is(err, core.ErrNoTorrentSources) || !errors.Is(err, core.ErrNoSources) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("debrid only, no torrent indexer: %v", err)
	}
	inner.torrent = true
	if err := x.MissingSources(); err != nil {
		t.Fatalf("torrent indexer present: %v", err)
	}
	inner.torrent, usenet = false, true
	if err := x.MissingSources(); err != nil {
		t.Fatalf("usenet provider present: %v", err)
	}
	inner.missing = core.ErrNoSources
	if err := x.MissingSources(); !errors.Is(err, core.ErrNoSources) || errors.Is(err, core.ErrNoTorrentSources) {
		t.Fatalf("no indexers: %v", err)
	}
	// Without debrid, a missing torrent indexer is not a problem.
	inner.missing, usenet = nil, false
	h.svc.UpdateConfig(config.Debrid{})
	if err := x.MissingSources(); err != nil {
		t.Fatalf("debrid off: %v", err)
	}
}

// A provider paused after rejecting its key keeps reporting ErrAuth, so a
// later play can say the key was rejected rather than "not available".
func TestAuthPauseStaysClassifiable(t *testing.T) {
	h := newHarness(t, 1<<20, "realdebrid")
	h.cacheEverywhere(testHash)
	p := h.provs["realdebrid-0"]
	p.mu.Lock()
	p.authFail = true
	p.mu.Unlock()
	for i := range 2 {
		_, err := h.svc.Acquire(context.Background(), torrentRelease("t"))
		if !errors.Is(err, ErrAuth) || !errors.Is(err, core.ErrDebridAuth) {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if i == 1 && !strings.Contains(err.Error(), "paused") {
			t.Fatalf("second acquire should hit the auth pause: %v", err)
		}
	}
}
