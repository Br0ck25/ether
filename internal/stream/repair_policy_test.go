package stream

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestRepairNotificationPrecedesRecoveryAndVerifiedCompletion(t *testing.T) {
	f, doc, want := repairPost(t)
	f.setMissing(postID("repair.mkv", 4))
	e := newTestEngine(t, f, nil, repairOpts())
	src := openSource(t, e, "notice", doc)
	var mu sync.Mutex
	var events []core.RepairEvent
	src.(core.RepairReporter).SetRepairHandler(func(event core.RepairEvent) core.RepairWindow {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if event.Phase == "missing" && e.Stats().RepairSuccesses != 0 {
			t.Error("late missing notification")
		}
		return core.RepairWindow{Deadline: time.Now().Add(2 * time.Second)}
	})
	got := make([]byte, 5120)
	if _, err := src.ReadAt(got, 3*5120); err != nil || !bytes.Equal(got, want[3*5120:4*5120]) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 || events[0].Phase != "missing" || events[len(events)-1].Phase != "repaired" {
		t.Fatal(events)
	}
	if events[0].Offset != 3*5120 {
		t.Fatal("posted offset was not mapped to media", events[0])
	}
}

func TestRepairPlaybackDeadlineCancelsDependencyWork(t *testing.T) {
	f, doc, _ := repairPost(t)
	f.setMissing(postID("repair.mkv", 4))
	e := newTestEngine(t, f, nil, repairOpts())
	src := openSource(t, e, "deadline", doc)
	f.mu.Lock()
	f.latency = 100 * time.Millisecond
	f.mu.Unlock()
	src.(core.RepairReporter).SetRepairHandler(func(event core.RepairEvent) core.RepairWindow {
		return core.RepairWindow{Deadline: time.Now().Add(20 * time.Millisecond)}
	})
	start := time.Now()
	_, err := src.ReadAt(make([]byte, 5120), 3*5120)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("playback deadline not honored", err, time.Since(start))
	}
}

func TestRepairAwareHealthAdmitsOnlyVerifiedRecovery(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		f, doc, _ := repairPost(t)
		f.setMissing(postID("repair.mkv", 4))
		opts := repairOpts()
		opts.Repair.Enabled = enabled
		e := newTestEngine(t, f, nil, opts)
		ok, err := e.CheckRelease(context.Background(), "health", doc)
		if ok != enabled {
			t.Fatalf("repair=%v healthy=%v err=%v stats=%+v", enabled, ok, err, e.Stats())
		}
		if enabled && e.Stats().RepairSuccesses == 0 {
			t.Fatal("unrepaired sample admitted")
		}
	}
}

func TestAdaptiveSummariesDoNotFetchParityUntilDamage(t *testing.T) {
	f, doc, _ := repairPost(t)
	opts := repairOpts()
	opts.Repair.AdaptiveSummaries = true
	e := newTestEngine(t, f, nil, opts)
	if err := e.InitRepair(context.Background(), "adaptive", doc); err != nil {
		t.Fatal(err)
	}
	if f.calls(postID("stream.vol0+4.par2", 1)) != 0 {
		t.Fatal("healthy source fetched recovery data")
	}
	f.setMissing(postID("repair.mkv", 4))
	if _, err := e.ReadPostedRange(context.Background(), "adaptive", doc, 0, 3*5120, make([]byte, 5120)); err != nil {
		t.Fatal(err)
	}
	r, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(r)
	r.repairMu.Lock()
	defer r.repairMu.Unlock()
	if r.repair.summaries == nil || len(r.repair.summaries.Exponents) > 2 {
		t.Fatal("damage did not activate bounded summaries")
	}
}

func TestRepairProviderStallIncreasesEstimate(t *testing.T) {
	e := newTestEngine(t, newFakeFetcher(), nil, repairOpts())
	e.repairRate.rate = 1 << 20
	e.repairRate.at = time.Now()
	fast := e.repairETA(8 << 20)
	e.repairRate.at = time.Now().Add(-12 * time.Second)
	slow := e.repairETA(8 << 20)
	if fast <= 0 || slow < fast*8 {
		t.Fatalf("stall erased/underestimated plan: %v -> %v", fast, slow)
	}
}

func TestRepairMovingDeadlineExtendsWithinOwnerBound(t *testing.T) {
	e := newTestEngine(t, newFakeFetcher(), nil, repairOpts())
	e.repairRate.rate = 1 << 20
	e.repairRate.at = time.Now()
	ctx, _, _, finish := e.watchRepair(t.Context(), nil, 0, core.PriorityReadahead, fetchOpts{repairProgress: func(_ *postedFile, _ int, event core.RepairEvent) core.RepairWindow {
		if event.Phase == "planning" && event.Estimate != 0 {
			t.Error("unplanned repair reported a cheap estimate")
		}
		return core.RepairWindow{Deadline: time.Now().Add(500 * time.Millisecond)}
	}})
	defer finish(true)
	timer := time.NewTimer(800 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal("growing playable buffer retained old deadline")
	case <-timer.C:
	}
}
