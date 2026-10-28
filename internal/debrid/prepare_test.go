package debrid

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type prepareInner struct {
	fakeInner
	active, peak, resolved atomic.Int64
	gate                   <-chan struct{}
}

func (p *prepareInner) ResolveTorrent(ctx context.Context, rel core.Release) (core.Release, error) {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for old := p.peak.Load(); n > old && !p.peak.CompareAndSwap(old, n); old = p.peak.Load() {
	}
	p.resolved.Add(1)
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return rel, ctx.Err()
		}
	}
	rel.InfoHash = fmt.Sprintf("%040s", rel.ID)
	return rel, nil
}

func TestPrepareSourcesChecksInBulkWithoutAddingTorrents(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	p := h.provs["torbox-0"]
	p.bulk = true
	gate := make(chan struct{})
	inner := &prepareInner{gate: gate}
	x := NewIndexer(inner, h.svc)
	var releases []core.Release
	for i := 1; i <= 8; i++ {
		releases = append(releases, core.Release{ID: fmt.Sprint(i), Protocol: core.ProtocolTorrent, Score: float64(100 - i)})
	}
	p.cached[fmt.Sprintf("%040s", "8")] = true
	done := make(chan []core.Release, 1)
	go func() { done <- x.PrepareSources(t.Context(), releases) }()
	deadline := time.After(2 * time.Second)
	for inner.active.Load() < 4 {
		select {
		case <-deadline:
			t.Fatal("descriptor lookups did not overlap")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(gate)
	got := <-done
	if inner.peak.Load() > 4 || inner.active.Load() != 0 || inner.resolved.Load() != 8 {
		t.Fatalf("worker bounds/leak: peak=%d active=%d resolved=%d", inner.peak.Load(), inner.active.Load(), inner.resolved.Load())
	}
	if p.callCount("check") != 1 || p.callCount("acquire") != 0 || p.callCount("find") != 0 {
		t.Fatalf("unexpected provider operations: %+v", p.calls)
	}
	if !h.svc.KnownCached(got[7].InfoHash) || got[7].ID != releases[7].ID || got[7].Score != releases[7].Score {
		t.Fatal("availability or original ranking was lost")
	}
	if releases[0].InfoHash != "" {
		t.Fatal("mutated the caller's search result cache")
	}
}

func TestPrepareSourcesCancellationKeepsUnknownCandidates(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	inner := &prepareInner{gate: make(chan struct{})}
	x := NewIndexer(inner, h.svc)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got := x.PrepareSources(ctx, []core.Release{{ID: "1", Protocol: core.ProtocolTorrent}})
	if len(got) != 1 || got[0].ID != "1" || inner.active.Load() != 0 || h.provs["torbox-0"].callCount("acquire") != 0 {
		t.Fatal("cancelled discovery lost a candidate or left a worker running")
	}
}
