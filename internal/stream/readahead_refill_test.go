package stream

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/archive/archivetest"
	"github.com/braedonsaunders/ether/internal/core"
)

type refillFetcher struct {
	*fakeFetcher
	gateID                   string
	gatePriority             core.Priority
	gate, started, cancelled chan struct{}
	once                     sync.Once
	activeRA, maxRA          atomic.Int32
}

func (f *refillFetcher) BodyPart(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	if prio == core.PriorityReadahead {
		n := f.activeRA.Add(1)
		defer f.activeRA.Add(-1)
		for {
			old := f.maxRA.Load()
			if n <= old || f.maxRA.CompareAndSwap(old, n) {
				break
			}
		}
	}
	if id == f.gateID && prio == f.gatePriority {
		f.once.Do(func() { close(f.started) })
		select {
		case <-f.gate:
		case <-ctx.Done():
			if f.cancelled != nil {
				close(f.cancelled)
			}
			return nil, core.ArticlePart{}, ctx.Err()
		}
	}
	return f.fakeFetcher.BodyPart(ctx, id, groups, prio)
}
func waitRefillSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func TestReadAheadRefillsWhileForegroundChunkIsStalled(t *testing.T) {
	const part = 32 << 10
	base := newFakeFetcher()
	base.latency = time.Millisecond
	f := &refillFetcher{fakeFetcher: base, gateID: postID("refill.mkv", 2), gatePriority: core.PriorityLive, gate: make(chan struct{}), started: make(chan struct{})}
	doc := post(t, base, part, postFile{name: "refill.mkv", data: mkvBytes(9, 64*part)})
	e := newTestEngine(t, f, nil, Options{ReadAheadMin: 8 * part, ReadAheadMax: 8 * part, ReadAheadParallel: 2})
	src := openSource(t, e, "refill", doc).(*source)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.gate) }) })
	done := make(chan error, 1)
	go func() { _, err := src.ReadAt(make([]byte, 4*part), 0); done <- err }()
	waitRefillSignal(t, f.started, "foreground did not reach its stalled article")
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for base.calls(postID("refill.mkv", 12)) == 0 {
		select {
		case <-deadline.C:
			t.Fatal("freed read-ahead slots were not refilled while foreground stalled")
		case <-poll.C:
		}
	}
	src.raWG.Wait()
	select {
	case err := <-done:
		t.Fatalf("foreground escaped its gate: %v", err)
	default:
	}
	if max := f.maxRA.Load(); max > 2 {
		t.Fatalf("refill exceeded worker cap: %d", max)
	}
	if base.calls(postID("refill.mkv", 13)) != 0 {
		t.Fatal("refill exceeded existing cursor window")
	}
	for i := 5; i <= 12; i++ {
		if calls := base.calls(postID("refill.mkv", i)); calls != 1 {
			t.Fatalf("duplicate/missing frontier claim for part%d: %d", i, calls)
		}
	}
	release.Do(func() { close(f.gate) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("foreground did not finish")
	}
}

func TestReadAheadRefillDoesNotReviveEvictedSeekCursor(t *testing.T) {
	const part = 64 << 10
	base := newFakeFetcher()
	f := &refillFetcher{fakeFetcher: base, gateID: postID("seeks.mkv", 2), gatePriority: core.PriorityReadahead, gate: make(chan struct{}), started: make(chan struct{})}
	doc := post(t, base, part, postFile{name: "seeks.mkv", data: mkvBytes(8, 18<<20)})
	e := newTestEngine(t, f, nil, Options{ReadAheadMin: 2 * part, ReadAheadMax: 2 * part, ReadAheadParallel: 1})
	src := openSource(t, e, "seeks", doc).(*source)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.gate) }) })
	src.readAhead(0, part)
	waitRefillSignal(t, f.started, "old cursor did not start")
	src.mu.Lock()
	old := src.cursors[0]
	src.mu.Unlock()
	for i := 1; i <= maxCursors; i++ {
		src.readAhead(int64(i)*4<<20, part)
	}
	if _, _, valid := src.cursorWindow(old); valid {
		t.Fatal("seek did not evict oldest cursor")
	}
	release.Do(func() { close(f.gate) })
	src.raWG.Wait()
	if base.calls(postID("seeks.mkv", 3)) != 0 {
		t.Fatal("completion revived an evicted cursor")
	}
	if max := f.maxRA.Load(); max > 1 {
		t.Fatalf("seek refill exceeded cap: %d", max)
	}
}

func TestReadAheadFailedArchiveVerificationNeedsNewDemand(t *testing.T) {
	base := newFakeFetcher()
	vols := buildRAR(t, []archivetest.RARFile{{Name: "archive.mkv", Data: mkvBytes(7, 2<<20)}}, archivetest.RAROptions{Version: 5, VolumeSize: 256 << 10, NewNumbering: true})
	doc := postVolumes(t, base, 64<<10, vols, newStyle("refill-archive"))
	e := newTestEngine(t, base, nil, Options{ReadAheadMin: 64 << 10, ReadAheadMax: 64 << 10, ReadAheadParallel: 2, Retries: -1})
	src := openSource(t, e, "archive", doc).(*source)
	if src.plan.isVerified(2) {
		t.Fatal("fixture's third archive volume unexpectedly verified")
	}
	id := src.plan.vols[2].msgID(0)
	base.setMissing(id)
	start := src.plan.layout().ext[2].inner
	src.readAhead(start-1, 1)
	src.raWG.Wait()
	calls := base.calls(id)
	if calls != 1 {
		t.Fatalf("failed verification calls=%d", calls)
	}
	for range 20 {
		src.kickReadAhead()
	}
	src.raWG.Wait()
	if base.calls(id) != calls {
		t.Fatal("completion wakeup retried a failed archive header")
	}
	src.readAhead(start-1, 1)
	src.raWG.Wait()
	if base.calls(id) != calls+1 {
		t.Fatal("new foreground demand did not permit one retry")
	}
}

func TestReadAheadRefillCloseCancelsAndJoinsScheduler(t *testing.T) {
	const part = 32 << 10
	base := newFakeFetcher()
	f := &refillFetcher{fakeFetcher: base, gateID: postID("close-refill.mkv", 2), gatePriority: core.PriorityReadahead, gate: make(chan struct{}), started: make(chan struct{}), cancelled: make(chan struct{})}
	doc := post(t, base, part, postFile{name: "close-refill.mkv", data: mkvBytes(6, 64*part)})
	e := newTestEngine(t, f, nil, Options{ReadAheadMin: 8 * part, ReadAheadMax: 8 * part, ReadAheadParallel: 1})
	src := openSource(t, e, "close-refill", doc).(*source)
	src.readAhead(0, part)
	waitRefillSignal(t, f.started, "read-ahead did not start")
	done := make(chan struct{})
	go func() { _ = src.Close(); close(done) }()
	waitRefillSignal(t, f.cancelled, "Close did not cancel pending article")
	waitRefillSignal(t, done, "Close did not join refill scheduler")
	for range 20 {
		src.kickReadAhead()
	}
	src.mu.Lock()
	pumping := src.raPumping
	src.mu.Unlock()
	if pumping || len(src.raSem) != 0 {
		t.Fatalf("closed source restarted scheduler: pump=%v workers=%d", pumping, len(src.raSem))
	}
	if base.calls(postID("close-refill.mkv", 3)) != 0 {
		t.Fatal("closed source refilled another article")
	}
}
