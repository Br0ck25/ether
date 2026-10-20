package watchlist

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// runPasses runs Run until stop and counts its passes (Users calls).
func runPasses(t *testing.T, s *Service, passes *atomic.Int32) func() {
	t.Helper()
	s.o.Users = func(context.Context) ([]core.User, error) {
		passes.Add(1)
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

func waitPasses(t *testing.T, passes *atomic.Int32, n int32, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for passes.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("passes %d, want %d", passes.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunResumesRecordedSchedule(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	opts := Options{SyncInterval: time.Second, ResumeSchedule: true}

	// First leader, nothing recorded: passes at once and records it.
	var first atomic.Int32
	stop := runPasses(t, New(db, opts), &first)
	waitPasses(t, &first, 1, 2*time.Second)
	stop()

	// The next leader waits for the rest of the interval...
	var next atomic.Int32
	stop = runPasses(t, New(db, opts), &next)
	defer stop()
	time.Sleep(300 * time.Millisecond)
	if next.Load() != 0 {
		t.Fatal("new leader passed immediately after a recent pass")
	}
	// ...then passes.
	waitPasses(t, &next, 1, 3*time.Second)
}

func TestRunPassesAtStartByDefault(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db, Options{SyncInterval: time.Hour})
	s.recordPass(context.Background()) // a recent pass is ignored without ResumeSchedule
	var n atomic.Int32
	stop := runPasses(t, s, &n)
	defer stop()
	waitPasses(t, &n, 1, 2*time.Second)
}
