package state

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// TestProgressHeldWhileDatabaseDown marks the database unreachable (as the
// cluster heartbeat or another writer would), reports progress, and checks
// it is written with its original time once the database is back.
func TestProgressHeldWhileDatabaseDown(t *testing.T) {
	db := store.OpenTest(t)
	clock := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	avail := store.NewAvailability(nil)
	s := New(db, Options{Now: func() time.Time { return clock }, Availability: avail})
	defer s.Close(context.Background())
	var events []Event
	s.Subscribe(func(ev Event) { events = append(events, ev) })
	key, dur := core.MovieKey(7), 100*time.Minute

	avail.Fail(syscall.ECONNREFUSED)
	start := time.Now()
	for _, off := range []time.Duration{10, 20, 30} {
		if err := s.Progress(ctx, 1, key, off*time.Minute, dur, "playing"); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("progress blocked while the database was down")
	}
	if st, ok := s.OutboxStatus(); !ok || st.Depth != 1 {
		t.Fatalf("outbox %+v %v, want one held write (newest wins)", st, ok)
	}
	if ws, _ := s.Get(ctx, 1, key); ws.ViewOffset != 0 {
		t.Fatalf("written while down: %+v", ws)
	}
	reported := clock
	clock = clock.Add(time.Hour)
	avail.OK()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := s.OutboxStatus(); st.Depth == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("held progress never written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ws, _ := s.Get(ctx, 1, key)
	if ws.ViewOffset != 30*time.Minute || !ws.LastViewedAt.Equal(reported) {
		t.Fatalf("after recovery %+v (want 30m at %v)", ws, reported)
	}
	if len(events) == 0 || events[0].Kind != EventProgress || !events[0].At.Equal(reported) {
		t.Fatalf("events %+v", events)
	}
}

func TestMergeProgressKeepsCompletion(t *testing.T) {
	dur := 100 * time.Minute
	done := progressWrite{offset: 95 * time.Minute, duration: dur, state: "stopped"}
	rewind := progressWrite{offset: 5 * time.Minute, duration: dur, state: "playing"}
	m := mergeProgress(done, rewind)
	if m.offset != rewind.offset || m.before == nil || m.before.offset != done.offset {
		t.Fatalf("merge %+v", m)
	}
	m2 := mergeProgress(m, progressWrite{offset: 6 * time.Minute, duration: dur})
	if m2.before == nil || m2.before.offset != done.offset {
		t.Fatal("completion lost on a further merge")
	}
	if m3 := mergeProgress(m2, progressWrite{offset: 99 * time.Minute, duration: dur}); m3.before != nil {
		t.Fatal("a completing write needs no carried completion")
	}

	// Applying the merged write counts the viewing and keeps the rewind.
	db := store.OpenTest(t)
	avail := store.NewAvailability(nil)
	s := New(db, Options{Availability: avail})
	defer s.Close(context.Background())
	key := core.MovieKey(8)
	avail.Fail(syscall.ECONNREFUSED)
	_ = s.Progress(ctx, 1, key, 50*time.Minute, dur, "playing")
	_ = s.Progress(ctx, 1, key, 95*time.Minute, dur, "playing")
	_ = s.Progress(ctx, 1, key, 5*time.Minute+30*time.Second, dur, "playing")
	avail.OK()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ws, _ := s.Get(ctx, 1, key)
	if ws.ViewCount != 1 || ws.ViewOffset != 5*time.Minute+30*time.Second {
		t.Fatalf("status %+v", ws)
	}
}
