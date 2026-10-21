package stream

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestReadAheadKeepsForegroundHeadroomInSmallHotCache(t *testing.T) {
	const part = 256 << 10
	f := newFakeFetcher()
	doc := post(t, f, part, postFile{name: "budget.mkv", data: mkvBytes(9, 8<<20)})
	e := newTestEngine(t, f, nil, Options{HotBufferBytes: 4 << 20, ReadAheadMin: 4 << 20, ReadAheadMax: 4 << 20, ReadAheadParallel: 64})
	src := openSource(t, e, "budget", doc).(*source)
	// Finish speculation before foreground assembly to make cache pressure
	// deterministic, independent of network/decoder completion ordering.
	src.readAhead(0, 1<<20)
	src.raWG.Wait()
	// With 4 MiB hot capacity the 1 MiB foreground read and 2 MiB ahead
	// coexist; scheduling the configured 4 MiB ahead would evict useful bytes.
	src.mu.Lock()
	window := src.cursors[0].window
	frontier := src.cursors[0].frontier
	src.mu.Unlock()
	if window != 2<<20 || frontier != 3<<20 {
		t.Fatalf("window=%d frontier=%d exceed cache headroom", window, frontier)
	}
	if f.calls(postID("budget.mkv", 13)) != 0 {
		t.Fatal("speculation fetched beyond its useful hot-cache window")
	}
	buf := make([]byte, 1<<20)
	for off := int64(0); off < 3<<20; off += int64(len(buf)) {
		if n, err := src.readRaw(context.Background(), buf, off, core.PriorityLive); err != nil || n != len(buf) {
			t.Fatalf("read%d: n=%d err=%v", off, n, err)
		}
	}
	for i := 1; i <= 12; i++ {
		if calls := f.calls(postID("budget.mkv", i)); calls != 1 {
			t.Fatalf("part%d refetched before consumption: %d", i, calls)
		}
	}
}

func TestReadAheadSkipsWhenDemandExhaustsHotBudget(t *testing.T) {
	f := newFakeFetcher()
	doc := post(t, f, 256<<10, postFile{name: "full.mkv", data: mkvBytes(8, 8<<20)})
	e := newTestEngine(t, f, nil, Options{HotBufferBytes: 2 << 20, ReadAheadMin: 4 << 20, ReadAheadMax: 4 << 20})
	src := openSource(t, e, "full", doc).(*source)
	f.resetCounts()
	src.readAhead(0, 2<<20)
	src.raWG.Wait()
	if f.totalCalls() != 0 {
		t.Fatalf("speculation with no cache headroom made%d calls", f.totalCalls())
	}
}

func TestReadAheadProductionWindowUnaffectedByCacheCap(t *testing.T) {
	// Arithmetic-only fixture: scheduling reaches EOF immediately, so this
	// checks the cursor budget without allocating/fetching 64 MiB of data.
	f := newFakeFetcher()
	doc := post(t, f, 256<<10, postFile{name: "default.mkv", data: mkvBytes(7, 512<<10)})
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "default", doc).(*source)
	src.mu.Lock()
	src.cursors = []*cursor{{next: 1 << 20, window: 64 << 20, frontier: 1 << 20}}
	src.mu.Unlock()
	src.readAhead(1<<20, 4<<20)
	src.raWG.Wait()
	src.mu.Lock()
	window := src.cursors[0].window
	src.mu.Unlock()
	if window != 64<<20 {
		t.Fatalf("production read-ahead shrank to%d", window)
	}
}
