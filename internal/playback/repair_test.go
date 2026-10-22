package playback

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"sync"
	"testing"
	"time"
)

type repairHLS struct {
	*fakeHLS
	mu     sync.Mutex
	buffer core.RecoveryBuffer
}

func (h *repairHLS) RecoveryBuffer() core.RecoveryBuffer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buffer
}

func TestRepairWarmsAlternateButKeepsRecoverableOriginal(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "repair-plan")
	s := h.m.lookup(ps.ID)
	g := s.current()
	f := &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 10 * time.Second, ReadyUntil: 40 * time.Second}}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
	w := h.m.noteRepair(g, core.RepairEvent{Key: 1, Offset: 60_000, Phase: "missing"})
	if w.Deadline.IsZero() {
		t.Fatal("missing playback deadline")
	}
	waitDamagePrepared(t, g)
	h.m.noteRepair(g, core.RepairEvent{Key: 1, Offset: 60_000, Phase: "repairing", Estimate: time.Second})
	if h.m.repairMustSwitch(g) {
		t.Fatal("cheap repair abandoned")
	}
	h.m.noteRepair(g, core.RepairEvent{Key: 1, Phase: "repaired"})
	if s.current() != g || h.m.repairMustSwitch(g) {
		t.Fatal("repaired source switched")
	}
}

func TestRepairSwitchesBeforeFailedArticleReachesReader(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "repair-switch")
	s := h.m.lookup(ps.ID)
	g := s.current()
	f := &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 10 * time.Second, ReadyUntil: 18 * time.Second}}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
	h.m.noteRepair(g, core.RepairEvent{Key: 1, Offset: 50_000, Phase: "missing"})
	waitDamagePrepared(t, g)
	h.m.noteRepair(g, core.RepairEvent{Key: 1, Offset: 50_000, Phase: "repairing", Estimate: 20 * time.Second})
	deadline := time.Now().Add(3 * time.Second)
	for s.current() == g && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.current() == g {
		t.Fatal("repair missed buffer but no proactive swap")
	}
	if s.current().releaseID() != "B" || g.articleMissing.Load() {
		t.Fatal("switch waited for foreground failure")
	}
}

func TestRepairCompletingDuringCandidateOpenPreventsSwap(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "repair-race")
	s := h.m.lookup(ps.ID)
	g := s.current()
	needed := true
	_, err := h.m.failoverWith(context.Background(), s, g, 0, failoverOpts{quietFailure: true, stillNeeded: func() bool { old := needed; needed = false; return old }})
	if err != nil || s.current() != g || g.ctx.Err() != nil {
		t.Fatal("obsolete proactive swap retired good source", err)
	}
}

func TestRepairTelemetryIsScopedToViewerAndClient(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "client")
	s := h.m.lookup(ps.ID)
	h.m.PlaybackProgress(owner.ID, "other", movieKey, 20*time.Second, "playing")
	s.mu.Lock()
	at := s.viewerAt
	s.mu.Unlock()
	if !at.IsZero() {
		t.Fatal("another client moved repair deadline")
	}
	h.m.PlaybackProgress(owner.ID, "client", movieKey, 20*time.Second, "paused")
	s.mu.Lock()
	at = s.viewerAt
	s.mu.Unlock()
	if at.IsZero() {
		t.Fatal("timeline ignored")
	}
}

func TestRepairSeekInvalidatesOlderTelemetryAndPassedDamage(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "seeking")
	s := h.m.lookup(ps.ID)
	g := s.current()
	h.m.PlaybackProgress(owner.ID, "seeking", movieKey, 10*time.Second, "paused")
	f := &repairHLS{fakeHLS: h.tr.addHLS(ps.ID), buffer: core.RecoveryBuffer{Position: 45 * time.Second, ReadyUntil: 55 * time.Second, SeekAt: time.Now()}}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return f, true }
	event := core.RepairEvent{Key: 1, Offset: 50_000, Phase: "failed"}
	g.recovery.events = map[repairEventKey]core.RepairEvent{{article: 1}: event}
	pos, _, _ := h.m.repairHorizon(g, event)
	if pos != 45*time.Second || h.m.repairMustSwitch(g) {
		t.Fatal("abandoned seek range affected recovery", pos)
	}
}

func TestRepairConcurrentCancellationDoesNotHideActiveAttempt(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "concurrent-repair")
	g := h.m.lookup(ps.ID).current()
	for _, e := range []core.RepairEvent{
		{Key: 1, Attempt: 1, Phase: "missing"},
		{Key: 1, Attempt: 2, Phase: "repairing", Estimate: time.Second},
		{Key: 1, Attempt: 1, Phase: "obsolete"},
	} {
		h.m.noteRepair(g, e)
	}
	g.recovery.mu.Lock()
	count := len(g.recovery.events)
	g.recovery.mu.Unlock()
	if count != 1 {
		t.Fatal("cancelled waiter hid active repair", count)
	}
	h.m.noteRepair(g, core.RepairEvent{Key: 1, Attempt: 2, Phase: "repaired"})
	h.m.noteRepair(g, core.RepairEvent{Key: 1, Attempt: 1, Phase: "failed"})
	g.recovery.mu.Lock()
	defer g.recovery.mu.Unlock()
	if len(g.recovery.events) != 0 {
		t.Fatal("late failure undid verified recovery")
	}
}
