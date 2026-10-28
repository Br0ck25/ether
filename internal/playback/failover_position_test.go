package playback

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// movingHLS reports a player position that the test can move mid-failover.
type movingHLS struct {
	*fakeHLS
	at atomic.Int64
}

func (h *movingHLS) RequestedPosition() time.Duration { return time.Duration(h.at.Load()) }

func wrapRequested(h *harness, id string, at time.Duration) (*fakeHLS, *movingHLS) {
	actual := h.tr.addHLS(id)
	wrapped := &movingHLS{fakeHLS: actual}
	wrapped.at.Store(int64(at))
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return wrapped, true }
	return actual, wrapped
}

func awaitSwap(t *testing.T, h *fakeHLS) swapCall {
	t.Helper()
	select {
	case sw := <-h.swapC:
		return sw
	case <-time.After(5 * time.Second):
		t.Fatal("no background failover")
		return swapCall{}
	}
}

// A Matroska demuxer reads the Cues at the file tail on every seek. When
// that tail read hits a missing article, the failover must resume where the
// player is, not at the time the tail offset maps to.
func TestLoopbackTailReadFailureResumesAtPlayerPosition(t *testing.T) {
	a := mkv("A", 100_000, 100*time.Second)
	h := newHarness(t, []*fakeRelease{a, mkv("B", 100_000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "c1")
	actual, _ := wrapRequested(h, ps.ID, 12*time.Second)
	a.setFail(99_000, 100_000, nil)

	resp, _, err := get(t, h.m.InputURL(ps.ID), map[string]string{"Range": "bytes=99000-"})
	if resp.StatusCode != 206 || err == nil {
		t.Fatalf("want truncated 206, got %d %v", resp.StatusCode, err)
	}
	if sw := awaitSwap(t, actual); sw.At != 12*time.Second {
		t.Fatalf("tail read failure resumed at %v, want the player's 12s", sw.At)
	}
}

// A player seek while candidates are still being opened moves the resume
// point; the position captured when the read failed is stale by then.
func TestLoopbackFailoverFollowsSeekDuringCandidateOpen(t *testing.T) {
	a := mkv("A", 100_000, 100*time.Second)
	h := newHarness(t, []*fakeRelease{a, mkv("B", 100_000, 100*time.Second)}, []string{"A", "B"})
	entered, release := make(chan struct{}), make(chan struct{})
	h.m.deps.FetchNZB = func(ctx context.Context, r core.Release) ([]byte, error) {
		if r.ID == "B" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []byte("nzb-" + r.ID), nil
	}
	ps := h.prepare(owner, movieKey, "c1")
	actual, wrapped := wrapRequested(h, ps.ID, 12*time.Second)
	a.setFail(20_000, 30_000, nil)

	resp, _, err := get(t, h.m.InputURL(ps.ID), map[string]string{"Range": "bytes=10000-"})
	if resp.StatusCode != 206 || err == nil {
		t.Fatalf("want truncated 206, got %d %v", resp.StatusCode, err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("failover never opened the alternate")
	}
	wrapped.at.Store(int64(40 * time.Second))
	close(release)
	if sw := awaitSwap(t, actual); sw.At != 40*time.Second {
		t.Fatalf("failover resumed at %v, want the post-seek 40s", sw.At)
	}
}

// Explicit positions are evidence from the caller and are kept.
func TestExplicitFailoverKeepsRequestedTime(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, 100*time.Second), mkv("B", 100_000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "c1")
	actual, _ := wrapRequested(h, ps.ID, 12*time.Second)
	if _, err := h.m.Failover(context.Background(), ps.ID, 42*time.Second); err != nil {
		t.Fatal(err)
	}
	if sw := awaitSwap(t, actual); sw.At != 42*time.Second {
		t.Fatalf("explicit failover resumed at %v, want 42s", sw.At)
	}
}

func TestEncoderInputFailoverKeepsEncoderTime(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, 100*time.Second), mkv("B", 100_000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "c1")
	actual, _ := wrapRequested(h, ps.ID, 12*time.Second)
	if err := h.m.FailoverInput(context.Background(), ps.ID, h.m.InputURL(ps.ID), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if sw := awaitSwap(t, actual); sw.At != 30*time.Second {
		t.Fatalf("encoder input failover resumed at %v, want 30s", sw.At)
	}
}

type bufferedPlayerHLS struct{ *bufferedReserveHLS }

func (h *bufferedPlayerHLS) RequestedPosition() time.Duration { return h.buffer.Position }

// A missing future article must not throw away the healthy media the player
// already buffered, even though the failed byte read maps far ahead of it.
func TestLoopbackReadFailurePreservesReadyMedia(t *testing.T) {
	a := mkv("A", 100_000, 100*time.Second)
	h := newHarness(t, []*fakeRelease{a, mkv("B", 100_000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "buffered-read-failure")
	actual := h.tr.addHLS(ps.ID)
	f := &bufferedReserveHLS{repairHLS: &repairHLS{fakeHLS: actual, buffer: core.RecoveryBuffer{Position: 12 * time.Second, ReadyUntil: 40 * time.Second}}}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return &bufferedPlayerHLS{f}, true }
	a.setFail(99_000, 100_000, nil)
	get(t, h.m.InputURL(ps.ID), map[string]string{"Range": "bytes=99000-"})
	awaitSwap(t, actual)
	if !f.preserved {
		t.Fatal("read failure discarded already produced media")
	}
}
