package debrid

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// These tests stall the upstream itself, mid-body over a real socket:
// the fake CDN sends valid 206 headers and some bytes, flushes, then
// goes silent without closing (see mediaServer.stallAfter/stallEvery).
// They complement the synthetic-reader stalls in sweep_fixes_test.go
// (offset 0, transport wrapper) and the pre-header stall in
// TestEngineHedgesSlowFirstByte.

func mediaRanges(m *mediaServer) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ranges...)
}

// waitNoInflight proves every upstream handler the test started has
// exited: a stalled handler only returns when its client disconnects,
// so a leaked request or connection shows up here.
func waitNoInflight(t *testing.T, m *mediaServer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.inflight.Load() == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%d media handlers still running (leaked request?)", m.inflight.Load())
}

type readAtContexter interface {
	ReadAtContext(context.Context, []byte, int64) (int, error)
}

// A seek-like ReadAt jump starts a fresh run of freshRunBlocks blocks.
// When that run stalls mid-body two blocks in, recovery must keep the
// completed blocks, resume the same file at the first incomplete block
// with the run's original end, return the exact bytes the caller asked
// for, and leave no stalled request behind. The source must then keep
// serving exact bytes at other positions.
func TestSeekRunMidBodyStallRecoversExactBytes(t *testing.T) {
	r := newEngineRig(t, 32*testBlock, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1 // no speculative requests: counts stay exact
	r.svc.UpdateConfig(cfg)
	r.eng.stall = 100 * time.Millisecond

	seek := int64(16 * testBlock)
	runEnd := seek + freshRunBlocks*testBlock - 1
	initialRange := fmt.Sprintf("bytes=%d-%d", seek, runEnd)
	r.media.mu.Lock()
	r.media.stallAfter["realdebrid-1|"+initialRange] = 2*testBlock + 123
	r.media.mu.Unlock()

	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rc := src.(readAtContexter)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Establish a position at the head first, so the stalled read below
	// is a genuine seek: old-position read-ahead is abandoned and the
	// new position is served by a fresh run.
	head := make([]byte, 4096)
	if n, err := rc.ReadAtContext(ctx, head, 0); err != nil || n != len(head) ||
		!bytes.Equal(head, r.media.data[:len(head)]) {
		t.Fatalf("head read: n=%d err=%v", n, err)
	}
	headRange := fmt.Sprintf("bytes=%d-%d", 0, freshRunBlocks*testBlock-1)

	got := make([]byte, 4*testBlock)
	start := time.Now()
	n, err := rc.ReadAtContext(ctx, got, seek)
	if err != nil || n != len(got) || !bytes.Equal(got, r.media.data[seek:seek+int64(len(got))]) {
		t.Fatalf("seek read n=%d err=%v", n, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("stall recovery not bounded: %v", d)
	}

	ranges := mediaRanges(r.media)
	wantRetry := fmt.Sprintf("bytes=%d-%d", seek+2*testBlock, runEnd)
	if len(ranges) != 3 || ranges[0] != headRange || ranges[1] != initialRange || ranges[2] != wantRetry {
		t.Fatalf("ranges = %v, want [%s %s %s] (head run, fresh %d-block seek run, resume at incomplete block)",
			ranges, headRange, initialRange, wantRetry, freshRunBlocks)
	}
	waitNoInflight(t, r.media)

	// The same source survives: exact bytes on a backward jump to
	// never-fetched blocks and on a forward jump past the recovered run.
	for _, off := range []int64{10 * testBlock, 28 * testBlock} {
		buf := make([]byte, 2*testBlock)
		if n, err := rc.ReadAtContext(ctx, buf, off); err != nil || n != len(buf) ||
			!bytes.Equal(buf, r.media.data[off:off+int64(len(buf))]) {
			t.Fatalf("post-stall read at %d: n=%d err=%v", off, n, err)
		}
	}
	waitNoInflight(t, r.media)
	if st := r.svc.Stats()[0]; st.State != "ok" || st.Failures != 0 {
		t.Fatalf("a recovered body stall marked the provider: %+v", st)
	}
}

// Cancelling a ReadAtContext whose upstream body has stalled mid-run
// must return promptly with the context error, must not wedge the
// block flights or leak the stalled request (the abandoned run ends
// via the stall timer and its bounded retry), and the same source must
// then serve the same bytes exactly.
func TestReadAtCancelDuringMidBodyStallLeavesSourceUsable(t *testing.T) {
	r := newEngineRig(t, 32*testBlock, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	r.eng.stall = 150 * time.Millisecond

	initialRange := fmt.Sprintf("bytes=%d-%d", 0, freshRunBlocks*testBlock-1)
	r.media.mu.Lock()
	r.media.stallAfter["realdebrid-1|"+initialRange] = testBlock + 10
	r.media.mu.Unlock()

	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rc := src.(readAtContexter)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := rc.ReadAtContext(ctx, make([]byte, 4*testBlock), 0)
		done <- result{n, err}
	}()
	// Cancel only once the stalled handler is actually running, so the
	// cancellation lands mid-body (first block delivered, second stuck).
	deadline := time.Now().Add(2 * time.Second)
	for r.media.inflight.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.media.inflight.Load() == 0 {
		t.Fatal("stalled request never reached the media server")
	}
	time.Sleep(30 * time.Millisecond)
	cancelAt := time.Now()
	cancel()
	select {
	case res := <-done:
		if !errors.Is(res.err, context.Canceled) || res.n > testBlock {
			t.Fatalf("cancelled read: n=%d err=%v", res.n, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock a stalled ReadAt")
	}
	if d := time.Since(cancelAt); d > time.Second {
		t.Fatalf("cancellation took %v", d)
	}

	// The same source and the same bytes, now without cancellation.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	got := make([]byte, 4*testBlock)
	if n, err := rc.ReadAtContext(ctx2, got, 0); err != nil || n != len(got) ||
		!bytes.Equal(got, r.media.data[:len(got)]) {
		t.Fatalf("read after cancel: n=%d err=%v", n, err)
	}
	waitNoInflight(t, r.media)
	// Initial stalled request + its bounded in-run retry; the re-read is
	// served from the recovered run's blocks. Nothing unbounded, and no
	// handler lingers after it settles.
	reqs := r.media.requests.Load()
	if reqs > 3 {
		t.Fatalf("media requests = %d, want <= 3", reqs)
	}
	time.Sleep(50 * time.Millisecond)
	if r.media.inflight.Load() != 0 || r.media.requests.Load() != reqs {
		t.Fatalf("requests still active after recovery: inflight=%d requests %d -> %d",
			r.media.inflight.Load(), reqs, r.media.requests.Load())
	}
	if st := r.svc.Stats()[0]; st.State != "ok" || st.Failures != 0 {
		t.Fatalf("a cancelled read marked the provider: %+v", st)
	}
}

// When every media response stalls mid-body, one read must give up
// after the existing retry budget (maxRangeAttempts requests for the
// run), report a transient source failure — never missing or corrupt —
// leak no request, and the same source must read exact bytes once the
// upstream recovers.
func TestPersistentMidBodyStallExhaustsBudgetAndRecovers(t *testing.T) {
	r := newEngineRig(t, 8*testBlock, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	r.eng.stall = 80 * time.Millisecond
	r.media.stallEvery.Store(123)

	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rc := src.(readAtContexter)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err = rc.ReadAtContext(ctx, make([]byte, testBlock), 0)
	if !errors.Is(err, core.ErrSourceUnavailable) || errors.Is(err, core.ErrArticleMissing) ||
		errors.Is(err, core.ErrArticleCorrupt) {
		t.Fatalf("persistent stall err = %v, want transient ErrSourceUnavailable", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("persistent stall took %v to give up", d)
	}
	if reqs := r.media.requests.Load(); reqs != maxRangeAttempts {
		t.Fatalf("media requests = %d, want the %d-attempt budget", reqs, maxRangeAttempts)
	}
	waitNoInflight(t, r.media)

	r.media.stallEvery.Store(0)
	got := make([]byte, 2*testBlock)
	if n, err := rc.ReadAtContext(ctx, got, 0); err != nil || n != len(got) ||
		!bytes.Equal(got, r.media.data[:len(got)]) {
		t.Fatalf("read after upstream recovery: n=%d err=%v", n, err)
	}
	waitNoInflight(t, r.media)
	if st := r.svc.Stats()[0]; st.State != "ok" || st.Failures != 0 {
		t.Fatalf("body stalls marked the provider: %+v", st)
	}
}
