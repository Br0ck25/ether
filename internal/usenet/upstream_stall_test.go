package usenet

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

// waitServerActive polls until the fake server holds exactly want open
// connections, proving stalled handlers exited instead of leaking.
func waitServerActive(t *testing.T, s *nntptest.Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.Stats().Active; got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("server active connections = %d, want %d (leaked connection?)", s.Stats().Active, want)
}

// A provider that sends the 222 line and the first yEnc line and then
// stops sending — connection open, no bytes — must fail on the idle-read
// deadline, discard that connection, and let the same fetch recover with
// exact bytes from the backfill provider. The partial body the primary
// already sent must not leak into the result. Unlike
// TestStalledBodyBackfillsAndTripsBreaker (which stalls before the status
// line via Fault.Delay), the stall here is mid-body.
func TestMidBodyStallBackfillsExactBytesAndDropsConn(t *testing.T) {
	primary := nntptest.Start(t, nntptest.Options{})
	backfill := nntptest.Start(t, nntptest.Options{})
	want := randData(42, 300_000)
	addSeg(t, primary, "a@x", want)
	addSeg(t, backfill, "a@x", want)
	solo := randData(43, 50_000)
	addSeg(t, primary, "solo@x", solo)

	primary.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "BODY" {
			return nntptest.Fault{BodyDelay: 30 * time.Second}
		}
		return nntptest.Fault{}
	})
	p, b := primary.Provider("p", 1), backfill.Provider("b", 1)
	b.Backfill = true
	f := newFetcher(t, []config.UsenetProvider{p, b}, Options{
		IdleReadTimeout: 50 * time.Millisecond, CommandTimeout: 5 * time.Second,
	})

	start := time.Now()
	got, err := f.Body(context.Background(), "a@x", nil, core.PriorityLive)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("mid-body stall recovery: len=%d err=%v", len(got), err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("recovery not bounded by the idle deadline: %v", d)
	}
	// A timeout breaks the attempt loop for that provider: exactly one
	// stalled BODY on the primary, no retry burn against it.
	if n := primary.BodyRequests("a@x"); n != 1 {
		t.Fatalf("primary BODY requests = %d, want 1", n)
	}
	// The stalled connection is discarded, never returned to the pool,
	// and its server handler exits once the client closes it.
	waitServerActive(t, primary, 0)
	if st := statsFor(f, "p"); st.LiveConnections != 0 {
		t.Fatalf("stalled connection pooled: %+v", st)
	}
	// One stall is below the fail threshold: the provider survives and
	// serves the next article itself once it answers again.
	if st := statsFor(f, "p"); st.State != StateOK {
		t.Fatalf("one mid-body stall sidelines the provider: %+v", st)
	}
	primary.SetFault(nil)
	got, err = f.Body(context.Background(), "solo@x", nil, core.PriorityLive)
	if err != nil || !bytes.Equal(got, solo) {
		t.Fatalf("provider did not survive a mid-body stall: len=%d err=%v", len(got), err)
	}
	waitServerActive(t, primary, 1)
}

// Cancelling the caller context while an article body is stalled
// mid-stream must interrupt the blocked read promptly — with the
// idle-read deadline disabled and a long command timeout, only the
// cancellation can end it. The cancelled connection must be discarded,
// the cancellation must not count against the provider, and the next
// fetch of the same article must return exact bytes on a fresh dial.
func TestMidBodyStallContextCancelIsBoundedAndProviderSurvives(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	want := randData(7, 200_000)
	addSeg(t, s, "stall@x", want)

	sawBody := make(chan struct{}, 1)
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "BODY" {
			select {
			case sawBody <- struct{}{}:
			default:
			}
			return nntptest.Fault{BodyDelay: time.Minute}
		}
		return nntptest.Fault{}
	})
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 1)}, Options{
		IdleReadTimeout: -1, CommandTimeout: time.Minute,
	})

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := f.Body(ctx, "stall@x", nil, core.PriorityLive)
		done <- result{data, err}
	}()
	select {
	case <-sawBody:
	case <-time.After(2 * time.Second):
		t.Fatal("BODY never reached the server")
	}
	// Let the first body line arrive so the cancel lands mid-body, not
	// on the status line.
	time.Sleep(30 * time.Millisecond)
	cancelAt := time.Now()
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) || r.data != nil {
			t.Fatalf("cancelled fetch: data=%d bytes err=%v", len(r.data), r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt a stalled body read")
	}
	if d := time.Since(cancelAt); d > time.Second {
		t.Fatalf("cancellation took %v to interrupt the read", d)
	}
	// The interrupted connection is closed on both ends, not pooled.
	waitServerActive(t, s, 0)
	if st := statsFor(f, "p"); st.LiveConnections != 0 || st.State != StateOK {
		t.Fatalf("after cancel: %+v", st)
	}
	dials := statsFor(f, "p").Dials

	s.SetFault(nil)
	got, err := f.Body(context.Background(), "stall@x", nil, core.PriorityLive)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("fetch after cancel: len=%d err=%v", len(got), err)
	}
	if st := statsFor(f, "p"); st.Dials != dials+1 || st.LiveConnections != 1 {
		t.Fatalf("recovery did not redial exactly once: %+v", st)
	}
}

// When every attempt stalls mid-body, the fetcher must stop within its
// existing budgets — one timed-out attempt per fetch against the
// provider, then the circuit breaker after FailThreshold consecutive
// failures — report a transient error (never "article missing"), leak
// no connections, and serve the article again once the provider
// recovers and the short backoff lapses: a stall is not a blacklist.
func TestPersistentMidBodyStallIsBoundedTransientAndRecovers(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	want := randData(9, 100_000)
	addSeg(t, s, "a@x", want)
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "BODY" {
			return nntptest.Fault{BodyDelay: 30 * time.Second}
		}
		return nntptest.Fault{}
	})
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 1)}, Options{
		IdleReadTimeout: 40 * time.Millisecond, CommandTimeout: 5 * time.Second,
		FailThreshold: 2, BackoffMin: 120 * time.Millisecond, BackoffMax: 250 * time.Millisecond,
	})

	start := time.Now()
	var lastErr error
	for i := 0; i < 3; i++ {
		_, lastErr = f.Body(context.Background(), "a@x", nil, core.PriorityLive)
		if lastErr == nil {
			t.Fatal("persistently stalling provider served a body")
		}
		if errors.Is(lastErr, core.ErrArticleMissing) {
			t.Fatalf("stall misclassified as missing: %v", lastErr)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("three stalled fetches took %v", d)
	}
	// Two timed-out attempts trip the breaker; the third fetch is
	// refused locally instead of issuing another doomed BODY.
	if n := s.BodyRequests("a@x"); n != 2 {
		t.Fatalf("BODY requests = %d, want 2 (fail threshold)", n)
	}
	if st := statsFor(f, "p"); st.LiveConnections != 0 {
		t.Fatalf("stalled connections leaked into the pool: %+v", st)
	}
	waitServerActive(t, s, 0)

	// The provider answers again: after the backoff lapses the same
	// article fetches with exact bytes. Poll, because a fetch during
	// backoff is refused without touching the server.
	s.SetFault(nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := f.Body(context.Background(), "a@x", nil, core.PriorityLive)
		if err == nil {
			if !bytes.Equal(got, want) {
				t.Fatal("recovered fetch returned wrong bytes")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider never recovered from stalls: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if st := statsFor(f, "p"); st.State != StateOK {
		t.Fatalf("provider state after recovery: %+v", st)
	}
}
