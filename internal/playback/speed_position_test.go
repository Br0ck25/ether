package playback

import (
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
	"time"
)

func TestSpeedPositionFollowsMediaAfterTailProbeAndBackwardSeek(t *testing.T) {
	s := &session{expectPos: -1}
	now := time.Now()
	s.noteReadPos(999000, 1000, now, time.Second)
	s.noteReadPos(10000, 1000, now, time.Second)
	if s.lastSrcPos != 11000 {
		t.Fatalf("tail probe pinned failover at EOF: %d", s.lastSrcPos)
	}
	s.noteReadPos(500000, 1000, now, time.Second)
	s.noteReadPos(20000, 1000, now, time.Second)
	if s.lastSrcPos != 21000 {
		t.Fatalf("backward seek kept old position: %d", s.lastSrcPos)
	}
}

type requestedHLS struct {
	*fakeHLS
	at time.Duration
}

func (h *requestedHLS) RequestedPosition() time.Duration { return h.at }
func TestDownFailoverUsesRequestedSegmentInsteadOfTailRead(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 80_000_000, 100*time.Second), mkv("B", 10_000_000, 100*time.Second)}, []string{"A", "B"}, func(h *harness, o *Options) { speedOpts(o) })
	ps := h.prepare(owner, movieKey, "c1")
	actual := h.tr.addHLS(ps.ID)
	wrapped := &requestedHLS{fakeHLS: actual, at: 12 * time.Second}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return wrapped, true }
	s := h.m.lookup(ps.ID)
	h.clock.Advance(2 * time.Second)
	s.noteReadPos(79_999_000, 1000, h.clock.Now(), 0)
	feedFetches(s, 10, 64<<10, 200*time.Millisecond, h.clock.Now())
	h.m.checkSpeeds()
	h.m.checkSpeeds()
	select {
	case swap := <-actual.swapC:
		if swap.At != 12*time.Second {
			t.Fatalf("down-failover resumed at %v instead of requested segment", swap.At)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no down-failover")
	}
}
