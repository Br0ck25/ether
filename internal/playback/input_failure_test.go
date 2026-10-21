package playback

import (
	"testing"
	"time"
)

func TestEncoderFailureRecoversOnlyObservedSource(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute), mkv("B", 10000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "encoder-failure")
	hls := h.tr.addHLS(ps.ID)
	old := h.m.InputURL(ps.ID)
	if err := h.m.FailoverInput(t.Context(), ps.ID, old, 12*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hls.swapC:
	default:
		t.Fatal("encoder was not moved to replacement")
	}
	if err := h.m.FailoverInput(t.Context(), ps.ID, old, 12*time.Second); err != nil {
		t.Fatal(err)
	}
	current, ok := h.m.Session(ps.ID)
	if !ok || current.Resolution.Release.ID != "B" || current.Failovers != 1 {
		t.Fatalf("stale failure displaced new source: %+v", current)
	}
	if h.resolver.isBad("A") {
		t.Fatal("encoder failure permanently blacklisted a source")
	}
}

func TestExhaustedEncoderRecoveryClosesPlayback(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "encoder-exhausted")
	h.tr.addHLS(ps.ID)
	if err := h.m.FailoverInput(t.Context(), ps.ID, h.m.InputURL(ps.ID), 0); err == nil {
		t.Fatal("exhausted recovery succeeded")
	}
	if _, ok := h.m.Session(ps.ID); ok {
		t.Fatal("exhausted session retained phantom playback")
	}
	// Retirement cancels background index/read work, but the source closes
	// only after those readers release their generation references.
	eventually(t, "exhausted source closed", func() bool { return h.engine.sources[0].closed.Load() })
}
