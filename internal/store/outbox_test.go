package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUnavailableAndPermanent(t *testing.T) {
	cases := []struct {
		err         error
		unavailable bool
		permanent   bool
	}{
		{nil, false, false},
		{errors.New("syntax error"), false, false},
		{context.DeadlineExceeded, true, false},
		{fmt.Errorf("exec: %w", syscall.ECONNREFUSED), true, false},
		{&net.OpError{Op: "dial", Err: errors.New("no route")}, true, false},
		{io.ErrUnexpectedEOF, true, false},
		{&pgconn.PgError{Code: "08006"}, true, false},
		{&pgconn.PgError{Code: "57P01"}, true, false},
		{&pgconn.PgError{Code: "23505"}, false, false}, // unique violation
		{&pgconn.PgError{Code: "28P01"}, false, true},  // bad password
		{fmt.Errorf("connect: %w", &pgconn.PgError{Code: "3D000"}), false, true},
	}
	for i, c := range cases {
		if got := IsUnavailable(c.err); got != c.unavailable {
			t.Errorf("%d: IsUnavailable(%v) = %v", i, c.err, got)
		}
		if got := IsPermanent(c.err); got != c.permanent {
			t.Errorf("%d: IsPermanent(%v) = %v", i, c.err, got)
		}
	}
}

func TestAvailabilityTransitions(t *testing.T) {
	a := NewAvailability(nil)
	var seen []bool
	unsub := a.Subscribe(func(up bool) { seen = append(seen, up) })
	defer unsub()
	select {
	case <-a.Up():
	default:
		t.Fatal("new tracker should be available")
	}
	if a.Report(errors.New("bad sql")) || a.Down() {
		t.Fatal("a statement error is not an outage")
	}
	if !a.Report(syscall.ECONNREFUSED) || !a.Down() {
		t.Fatal("connection refused should mark the database down")
	}
	up := a.Up()
	select {
	case <-up:
		t.Fatal("Up closed while down")
	default:
	}
	st := a.State()
	if st.Reachable || st.DownSince.IsZero() || st.LastError == "" {
		t.Fatalf("state %+v", st)
	}
	a.Report(nil)
	select {
	case <-up:
	default:
		t.Fatal("Up not closed on recovery")
	}
	if len(seen) != 2 || seen[0] || !seen[1] {
		t.Fatalf("transitions %v", seen)
	}
	var nilA *Availability
	nilA.Fail(syscall.ECONNREFUSED)
	if nilA.Down() || !nilA.State().Reachable {
		t.Fatal("nil tracker must be always available")
	}
}

// fakeDB is a write target the test can take down.
type fakeDB struct {
	mu     sync.Mutex
	down   bool
	hang   bool // block until ctx ends (a black-holed connection)
	writes []string
	reject map[string]bool
}

func (f *fakeDB) apply(ctx context.Context, key string, v int) error {
	f.mu.Lock()
	down, hang := f.down, f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	if down {
		return fmt.Errorf("dial: %w", syscall.ECONNREFUSED)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reject[key] {
		return errors.New("constraint violated")
	}
	f.writes = append(f.writes, fmt.Sprintf("%s=%d", key, v))
	return nil
}

func (f *fakeDB) set(down, hang bool) {
	f.mu.Lock()
	f.down, f.hang = down, hang
	f.mu.Unlock()
}

func (f *fakeDB) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOutboxHoldsWritesThroughAnOutage(t *testing.T) {
	db := &fakeDB{}
	avail := NewAvailability(nil)
	ob := NewOutbox(OutboxOptions[string, int]{
		Name: "test", Apply: db.apply, Availability: avail, Logger: quietLog(),
		Timeout: 100 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	})
	defer ob.Close(context.Background())
	ctx := context.Background()

	if q, err := ob.Write(ctx, "a", 1); q || err != nil {
		t.Fatalf("healthy write: queued=%v err=%v", q, err)
	}
	// A black-holed database costs the first caller at most Timeout.
	db.set(false, true)
	start := time.Now()
	if q, err := ob.Write(ctx, "a", 2); !q || err != nil {
		t.Fatalf("outage write: queued=%v err=%v", q, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("write blocked %v", d)
	}
	if !avail.Down() {
		t.Fatal("timeout should mark the database down")
	}
	// Later writes queue without trying; newest value per key wins.
	start = time.Now()
	for i := 3; i <= 5; i++ {
		if q, _ := ob.Write(ctx, "a", i); !q {
			t.Fatal("write while down was not queued")
		}
	}
	_, _ = ob.Write(ctx, "b", 1)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("queued writes blocked %v", d)
	}
	if ob.Depth() != 2 {
		t.Fatalf("depth %d, want 2", ob.Depth())
	}
	st := ob.Status()
	if st.Depth != 2 || st.Oldest.IsZero() || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
	db.set(true, false) // refused: still down, retried with backoff
	time.Sleep(80 * time.Millisecond)
	if len(db.got()) != 1 {
		t.Fatalf("wrote while down: %v", db.got())
	}
	db.set(false, false)
	waitFor(t, "replay", func() bool { return ob.Depth() == 0 })
	got := db.got()
	if len(got) != 3 || got[1] != "a=5" || got[2] != "b=1" {
		t.Fatalf("writes %v, want [a=1 a=5 b=1]", got)
	}
	if avail.Down() {
		t.Fatal("replay should mark the database reachable")
	}
}

func TestOutboxMergeCapAndRejects(t *testing.T) {
	db := &fakeDB{reject: map[string]bool{"bad": true}}
	avail := NewAvailability(nil)
	ob := NewOutbox(OutboxOptions[string, int]{
		Name: "test", Apply: db.apply, Availability: avail, Logger: quietLog(), Cap: 3,
		Merge:   func(old, new int) int { return old + new },
		Timeout: 100 * time.Millisecond, MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
	})
	ctx := context.Background()
	if _, err := ob.Write(ctx, "bad", 1); err == nil {
		t.Fatal("a rejected healthy write must return its error")
	}
	db.set(true, false)
	_, _ = ob.Write(ctx, "a", 1)
	_, _ = ob.Write(ctx, "a", 2) // merged: 3
	_, _ = ob.Write(ctx, "bad", 1)
	_, _ = ob.Write(ctx, "c", 1)
	_, _ = ob.Write(ctx, "d", 1) // full: drops "a", the oldest
	if st := ob.Status(); st.Depth != 3 || st.Dropped != 1 {
		t.Fatalf("status %+v", st)
	}
	db.set(false, false)
	waitFor(t, "replay", func() bool { return ob.Depth() == 0 })
	got := db.got()
	if len(got) != 2 || got[0] != "c=1" || got[1] != "d=1" {
		t.Fatalf("writes %v (rejected write must be dropped, oldest evicted)", got)
	}
	// Merge with a pending value.
	db.set(true, false)
	_, _ = ob.Write(ctx, "m", 1)
	_, _ = ob.Write(ctx, "m", 2)
	db.set(false, false)
	// Close replays what is left even if the loop has not yet.
	if err := ob.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.got(); got[len(got)-1] != "m=3" {
		t.Fatalf("merged write %v", got)
	}
}

func TestOutboxCloseReportsLostWrites(t *testing.T) {
	db := &fakeDB{}
	ob := NewOutbox(OutboxOptions[string, int]{Name: "test", Apply: db.apply, Logger: quietLog(),
		Timeout: 50 * time.Millisecond, MinBackoff: time.Hour, MaxBackoff: time.Hour})
	db.set(true, false)
	_, _ = ob.Write(context.Background(), "a", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ob.Close(ctx); err == nil {
		t.Fatal("Close should report writes it could not make")
	}
	if _, err := ob.Write(context.Background(), "b", 1); err == nil {
		t.Fatal("a write after Close cannot be queued")
	}
}
