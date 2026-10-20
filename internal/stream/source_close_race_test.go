package stream

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCloseRacingReadAhead closes sources while sequential readers keep
// scheduling read-ahead. Read-ahead must never start a fetch goroutine after
// Close has begun waiting for them (a WaitGroup Add racing Wait, and work
// outliving the source's release reference).
func TestCloseRacingReadAhead(t *testing.T) {
	f := newFakeFetcher()
	f.latency = time.Millisecond
	data := mkvBytes(31, 2_000_000)
	doc := post(t, f, 50_000, postFile{name: "race.mkv", data: data})
	e := newTestEngine(t, f, nil, Options{ReadAheadMin: 400_000, ReadAheadMax: 1_000_000, ReadAheadParallel: 4})
	for round := 0; round < 40; round++ {
		src, err := e.Open(context.Background(), "race", doc)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func(r int) {
				defer wg.Done()
				buf := make([]byte, 16<<10)
				off := int64(r) * 400_000
				for i := 0; i < 50; i++ {
					if _, err := src.ReadAt(buf, off); err != nil {
						return
					}
					off += int64(len(buf))
				}
			}(r)
		}
		time.Sleep(time.Duration(round%5) * time.Millisecond)
		_ = src.Close()
		wg.Wait()
	}
	if n := e.Stats().ActiveSources; n != 0 {
		t.Fatalf("%d sources still active", n)
	}
}

// TestScheduleAfterCloseStartsNothing covers a reader that passed
// readAhead's closed check just before Close: its schedule call must not
// start read-ahead once Close has run.
func TestScheduleAfterCloseStartsNothing(t *testing.T) {
	f := newFakeFetcher()
	data := mkvBytes(32, 1_000_000)
	doc := post(t, f, 50_000, postFile{name: "late.mkv", data: data})
	e := newTestEngine(t, f, nil, Options{})
	ms, err := e.Open(context.Background(), "late", doc)
	if err != nil {
		t.Fatal(err)
	}
	s := ms.(*source)
	_ = s.Close()
	// Open left part 1 in the hot buffer; scheduling must stop at part 2
	// (50 000) instead of starting workers for the rest of the window.
	if got := s.schedule(0, s.plan.size); got > 50_000 {
		t.Fatalf("schedule after Close scheduled up to %d, want at most 50000", got)
	}
	if n := len(s.raSem); n != 0 {
		t.Fatalf("%d read-ahead slots taken after Close", n)
	}
}

// TestTrimDropsHotSegmentsBeforeReacquire: trimming a release drops its hot
// segments before acquire returns, so a later re-open of the same NZB
// (same digest) never has its fresh segments evicted by a stale drop.
func TestTrimDropsHotSegmentsBeforeReacquire(t *testing.T) {
	f := newFakeFetcher()
	e := newTestEngine(t, f, nil, Options{MaxReleases: 1})
	docA := post(t, f, 1000, postFile{name: "a.mkv", data: mkvBytes(41, 5000)})
	docB := post(t, f, 1000, postFile{name: "b.mkv", data: mkvBytes(42, 5000)})
	srcA := openSource(t, e, "a", docA)
	digestA := srcA.(*source).rel.digest
	_ = srcA.Close()
	relB, err := e.acquire(docB)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(relB)
	e.hot.mu.Lock()
	defer e.hot.mu.Unlock()
	for k := range e.hot.items {
		if k.rel == digestA {
			t.Fatalf("hot segment %d of the trimmed release still cached after acquire returned", k.seg)
		}
	}
}

// TestHotHitTeachesFreshReleaseIndex: a fetch that outlived its release
// state (a reader's speculative fetch finishing after the release was
// trimmed) leaves segments in the hot buffer under the NZB digest. A fresh
// release state for the same NZB that is served those segments from memory
// must learn their exact ranges, or a non-uniform post never locates the
// offset and reads fail as corrupt.
func TestHotHitTeachesFreshReleaseIndex(t *testing.T) {
	f := newFakeFetcher()
	data := mkvBytes(43, 1_000_000)
	sizes := []int{100_000, 37_000, 150_000, 99_999, 1, 200_000, 50_000, 150_000, 100_000, 113_000}
	docA := post(t, f, 0, postFile{name: "lin.mkv", data: data, parts: sizes})
	docB := post(t, f, 1000, postFile{name: "other.mkv", data: mkvBytes(44, 5000)})
	e := newTestEngine(t, f, nil, Options{MaxReleases: 1})
	srcA := openSource(t, e, "lin", docA)
	old := srcA.(*source).rel
	_ = srcA.Close()
	_ = openSource(t, e, "other", docB) // trims the release state of A
	ctx := context.Background()
	for i := 1; i < len(sizes)-1; i++ { // the lingering fetches complete
		if _, err := e.segment(ctx, old, old.files[0], i, 0, fetchOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	src := openSource(t, e, "lin", docA)
	if src.(*source).rel == old {
		t.Fatal("release state was not rebuilt")
	}
	checkRandomReads(t, src, data, 9, 100)
}
