package playback

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestReserveWarmsFortySecondsWithoutDelayingPlay(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000_000, 100*time.Second)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "reserve")
	h.m.PlaybackProgress(owner.ID, "reserve", movieKey, 10*time.Second, "playing")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.engine.mu.Lock()
		calls := append([]prefetchCall(nil), h.engine.prefetch...)
		h.engine.mu.Unlock()
		for _, c := range calls {
			if c.Length >= 40_000_000 && c.Length <= 256<<20 {
				if h.m.lookup(ps.ID).current().releaseID() != "A" {
					t.Fatal("prefetch changed source")
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no 40-second reserve requested")
}

func TestDetailReserveUsesMediaDuration(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000_000, 100*time.Second)}, []string{"A"})
	if !h.m.PreResolve(t.Context(), owner, movieKey, defaultCaps("detail-reserve")) {
		t.Fatal("warm skipped")
	}
	waitWarm(t, h, owner.ID, "detail-reserve", movieKey)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.engine.mu.Lock()
		calls := append([]prefetchCall(nil), h.engine.prefetch...)
		h.engine.mu.Unlock()
		for _, c := range calls {
			if c.Length == 40_000_000 {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("detail warming did not request 40 seconds")
}

func TestPreparedSpeedAlternateReusesOpenAndProbe(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000_000, 100*time.Second), mkv("B", 20_000_000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "standby")
	s := h.m.lookup(ps.ID)
	g := s.current()
	h.tr.addHLS(ps.ID)
	h.m.prepareSpeedAlternate(g)
	deadline := time.Now().Add(time.Second)
	for {
		g.standby.mu.Lock()
		ready := g.standby.ready != nil
		g.standby.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("alternate not prepared")
		}
		time.Sleep(time.Millisecond)
	}
	if s.current() != g {
		t.Fatal("preparation replaced playing source")
	}
	_, probes := counts(h)
	next, err := h.m.failoverWith(context.Background(), s, g, 10*time.Second, failoverOpts{down: true, preserveBuffer: true})
	if err != nil || next.releaseID() != "B" {
		t.Fatalf("takeover failed %v", err)
	}
	_, after := counts(h)
	if after != probes {
		t.Fatalf("takeover reprobed: %d -> %d", probes, after)
	}
}

func TestBufferRunwayAccountsForOngoingDelivery(t *testing.T) {
	for _, tc := range []struct {
		buffer time.Duration
		ratio  float64
		want   time.Duration
	}{
		{20 * time.Second, .5, 40 * time.Second}, {10 * time.Second, 0, 10 * time.Second}, {0, .8, 0}, {40 * time.Second, 1.2, time.Hour},
	} {
		if got := bufferRunway(tc.buffer, tc.ratio); got != tc.want {
			t.Fatalf("runway(%v,%v)=%v want %v", tc.buffer, tc.ratio, got, tc.want)
		}
	}
}

func TestLowBufferOverridesLegacyGraceButNotSeekSettling(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 80_000_000, 100*time.Second), mkv("B", 10_000_000, 100*time.Second)}, []string{"A", "B"}, func(h *harness, o *Options) { o.SpeedCheckInterval = time.Hour })
	ps := h.prepare(owner, movieKey, "deadline")
	s := h.m.lookup(ps.ID)
	f := &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 10 * time.Second, ReadyUntil: 14 * time.Second}}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
	h.clock.Advance(7 * time.Second)
	feedFetches(s, 10, 64<<10, 200*time.Millisecond, h.clock.Now())
	h.m.checkSpeeds()
	h.m.checkSpeeds()
	select {
	case <-f.swapC:
	case <-time.After(3 * time.Second):
		t.Fatal("emptying buffer waited for 20-second grace")
	}
}

func TestPreparedAlternateIsClosedWhenPlaybackStops(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000_000, 100*time.Second), mkv("B", 20_000_000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "stop-standby")
	g := h.m.lookup(ps.ID).current()
	h.tr.addHLS(ps.ID)
	h.m.prepareSpeedAlternate(g)
	var next *generation
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		g.standby.mu.Lock()
		next = g.standby.ready
		g.standby.mu.Unlock()
		if next != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if next == nil {
		t.Fatal("standby not prepared")
	}
	h.m.Stop(ps.ID)
	select {
	case <-next.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("unused standby survived Stop")
	}
	h.m.mu.Lock()
	_, retained := h.m.tokens[ps.ID+"/"+next.token]
	h.m.mu.Unlock()
	if retained {
		t.Fatal("unused standby loopback grant survived Stop")
	}
}

func TestSeekSettlingAndPausedPlaybackDoNotTriggerRecovery(t *testing.T) {
	for _, paused := range []bool{false, true} {
		h := newHarness(t, []*fakeRelease{mkv("A", 80_000_000, 100*time.Second), mkv("B", 10_000_000, 100*time.Second)}, []string{"A", "B"}, func(h *harness, o *Options) { o.SpeedCheckInterval = time.Hour })
		ps := h.prepare(owner, movieKey, "seek")
		s := h.m.lookup(ps.ID)
		f := &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 10 * time.Second, ReadyUntil: 11 * time.Second}}
		h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
		h.clock.Advance(30 * time.Second)
		if paused {
			s.mu.Lock()
			s.viewerState = "paused"
			s.mu.Unlock()
		} else {
			s.noteReadPos(0, 100, h.clock.Now(), h.m.opts.SpeedGrace)
			s.noteReadPos(9999, 100, h.clock.Now(), h.m.opts.SpeedGrace)
		}
		feedFetches(s, 10, 64<<10, 200*time.Millisecond, h.clock.Now())
		h.m.checkSpeeds()
		h.m.checkSpeeds()
		s.current().standby.mu.Lock()
		started := s.current().standby.started
		s.current().standby.mu.Unlock()
		if started {
			t.Fatal("seek/paused playback started speculative recovery")
		}
		select {
		case <-f.swapC:
			t.Fatal("seek/paused playback switched")
		default:
		}
	}
}

func TestBlockedSourceRecoversWithoutCompletedFetchSamples(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 80_000_000, 100*time.Second), mkv("B", 10_000_000, 100*time.Second)}, []string{"A", "B"}, func(h *harness, o *Options) { o.SpeedCheckInterval = time.Hour })
	ps := h.prepare(owner, movieKey, "blocked")
	s := h.m.lookup(ps.ID)
	f := &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 10 * time.Second, ReadyUntil: 14 * time.Second}}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
	end := s.current().reads.begin(h.clock.Now())
	defer end()
	h.clock.Advance(8 * time.Second)
	h.m.checkSpeeds()
	h.m.checkSpeeds()
	select {
	case <-f.swapC:
	case <-time.After(3 * time.Second):
		t.Fatal("outage waited for a completed read")
	}
}

func TestMeasuredColdAndPreparedPlay(t *testing.T) {
	// Controlled source-selection delay, not an Internet or Apple TV benchmark.
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000_000, 100*time.Second)}, []string{"A"})
	h.resolver.delay = 250 * time.Millisecond
	start := time.Now()
	cold := h.prepare(owner, movieKey, "cold-measure")
	coldTime := time.Since(start)
	h.m.Stop(cold.ID)
	if !h.m.PreResolve(t.Context(), owner, movieKey, defaultCaps("warm-measure")) {
		t.Fatal("prewarm skipped")
	}
	waitWarm(t, h, owner.ID, "warm-measure", movieKey)
	calls, probes := counts(h)
	start = time.Now()
	h.prepare(owner, movieKey, "warm-measure")
	warmTime := time.Since(start)
	afterCalls, afterProbes := counts(h)
	if calls != afterCalls || probes != afterProbes {
		t.Fatal("prepared Play repeated discovery/probe")
	}
	t.Logf("controlled 250ms discovery: cold=%s prepared=%s; preparation paid before Play", coldTime, warmTime)
}

type bufferedReserveHLS struct {
	*repairHLS
	preserved bool
}

func (h *bufferedReserveHLS) SwapInputBuffered(ctx context.Context, url string, info core.MediaInfo, at time.Duration) error {
	h.preserved = true
	return h.SwapInput(ctx, url, info, at)
}
func TestBufferedRecoveryAllowsTrailingDurationButRejectsPALMapping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration time.Duration
		preserve bool
	}{
		{"trailing-credits", 99 * time.Second, true}, {"pal-speed", 96 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []*fakeRelease{mkv("A", 80_000_000, 100*time.Second), mkv("B", 10_000_000, tc.duration)}, []string{"A", "B"})
			ps := h.prepare(owner, movieKey, "buffer-boundary")
			s := h.m.lookup(ps.ID)
			f := &bufferedReserveHLS{repairHLS: &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 0, ReadyUntil: 40 * time.Second}}}
			h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
			if _, err := h.m.failoverWith(t.Context(), s, s.current(), 0, failoverOpts{down: true, preserveBuffer: true}); err != nil {
				t.Fatal(err)
			}
			if f.preserved != tc.preserve {
				t.Fatalf("preserved=%v want %v", f.preserved, tc.preserve)
			}
		})
	}
}
