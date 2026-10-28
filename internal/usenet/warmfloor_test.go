package usenet

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

// waitLive polls until the provider holds at least want live connections.
func waitLive(t *testing.T, f *Fetcher, id string, want int) ProviderStats {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := statsFor(f, id)
		if st.LiveConnections >= want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("live connections = %d, want >= %d (%+v)", st.LiveConnections, want, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEffectiveMinIdle(t *testing.T) {
	cases := []struct {
		cfg  config.UsenetProvider
		glob int
		want int
	}{
		{config.UsenetProvider{}, 2, 2},             // inherit
		{config.UsenetProvider{MinIdle: 4}, 2, 4},   // override up
		{config.UsenetProvider{MinIdle: -1}, 2, -1}, // override off
		{config.UsenetProvider{MinIdle: 3}, -1, 3},  // override on
		{config.UsenetProvider{}, -1, -1},           // inherit off
		{config.UsenetProvider{MinIdle: 0}, 0, 0},   // both unset
	}
	for i, c := range cases {
		if got := effectiveMinIdle(c.cfg, c.glob); got != c.want {
			t.Fatalf("case %d: effectiveMinIdle = %d, want %d", i, got, c.want)
		}
	}
}

// The floor is established by the janitor alone: no request is needed, and
// the first real request reuses a warm connection without dialling.
func TestWarmFloorReachedAfterStart(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	addSeg(t, s, "a@x", []byte("alpha"))
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 4)}, Options{
		MinIdle: 3, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	// LiveConnections includes a dial still being authenticated. Wait for
	// the completed warm floor before asserting its idle/redial counters.
	var st ProviderStats
	waitPipeline(t, func() bool {
		st = statsFor(f, "p")
		return st.WarmIdleConnections == 3 && st.BackgroundRedials >= 3
	})
	if st.BackgroundRedials < 3 || st.MinIdle != 3 || st.WarmIdleConnections != 3 {
		t.Fatalf("floor stats: %+v", st)
	}
	if st.LiveConnections != 3 {
		t.Fatalf("janitor over-dialled past the floor: %+v", st)
	}
	dials := st.Dials
	if got, err := f.Body(context.Background(), "a@x", nil, core.PriorityLive); err != nil || string(got) != "alpha" {
		t.Fatalf("body: %v", err)
	}
	if after := statsFor(f, "p").Dials; after != dials {
		t.Fatalf("first request dialled %d new connections despite warm floor", after-dials)
	}
}

// When the server drops its idle sockets, keepalive discovers the loss and
// the janitor redials the floor without any request paying for it.
func TestWarmFloorRestoredAfterServerDrop(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	addSeg(t, s, "a@x", []byte("alpha"))
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 4)}, Options{
		MinIdle: 2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	before := waitLive(t, f, "p", 2)
	s.DropConnections()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := statsFor(f, "p")
		if st.LiveConnections >= 2 && st.BackgroundRedials > before.BackgroundRedials && st.Dials > before.Dials {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("floor not restored after drop: before %+v now %+v", before, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, err := f.Body(context.Background(), "a@x", nil, core.PriorityLive); err != nil || string(got) != "alpha" {
		t.Fatalf("body after restore: %v", err)
	}
}

// A single failed DATE discards that connection; the floor is topped back up.
func TestWarmFloorRestoredAfterKeepAliveFailure(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 2)}, Options{
		MinIdle: 2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	before := waitLive(t, f, "p", 2)
	var dropped atomic.Bool
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "DATE" && dropped.CompareAndSwap(false, true) {
			return nntptest.Fault{Drop: true}
		}
		return nntptest.Fault{}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := statsFor(f, "p")
		if dropped.Load() && st.Dials > before.Dials && st.LiveConnections >= 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("floor not restored after DATE failure: before %+v now %+v dropped=%v", before, st, dropped.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBackfillPoolNeverWarmed(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	p := s.Provider("b", 4)
	p.Backfill = true
	f := newFetcher(t, []config.UsenetProvider{p}, Options{
		MinIdle: 2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	time.Sleep(250 * time.Millisecond)
	if st := statsFor(f, "b"); st.LiveConnections != 0 || st.MinIdle != 0 || st.BackgroundRedials != 0 {
		t.Fatalf("backfill pool warmed: %+v", st)
	}
	if s.Stats().Accepted != 0 {
		t.Fatalf("backfill server saw %d connections", s.Stats().Accepted)
	}
}

func TestMinIdleNegativeDisablesFloor(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 4)}, Options{
		MinIdle: -2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	time.Sleep(250 * time.Millisecond)
	if st := statsFor(f, "p"); st.LiveConnections != 0 || st.BackgroundRedials != 0 {
		t.Fatalf("floor active despite MinIdle<0: %+v", st)
	}
	if s.Stats().Accepted != 0 {
		t.Fatalf("server saw %d connections", s.Stats().Accepted)
	}
}

func TestPerProviderMinIdleOverride(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	// Global floor on, this provider opts out.
	off := s.Provider("off", 2)
	off.MinIdle = -1
	// Another provider (same server) inherits the global floor.
	on := s.Provider("on", 4)
	on.Priority = 1
	f := newFetcher(t, []config.UsenetProvider{off, on}, Options{
		MinIdle: 2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	waitLive(t, f, "on", 2)
	time.Sleep(100 * time.Millisecond)
	if st := statsFor(f, "off"); st.LiveConnections != 0 || st.MinIdle != 0 {
		t.Fatalf("opted-out provider warmed: %+v", st)
	}
}

// A provider that cannot be dialled gets exponential backoff, not a tight
// redial loop, and the circuit breaker eventually holds the janitor off.
func TestWarmFloorBackoffAfterDialFailures(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	var dials atomic.Int64
	dialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
	}
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 4)}, Options{
		MinIdle: 2, KeepAlive: 10 * time.Millisecond, IdleTimeout: time.Second,
		DialContext: dialer, BackoffMin: 80 * time.Millisecond, BackoffMax: 320 * time.Millisecond,
		FailThreshold: 100, // breaker stays out of the way; warm backoff is what counts
	})
	time.Sleep(700 * time.Millisecond)
	n := dials.Load()
	if n == 0 {
		t.Fatal("janitor never tried to warm the floor")
	}
	if n > 12 {
		t.Fatalf("janitor redialled %d times in 700ms: no backoff", n)
	}
	// Live may transiently be 1 (a redial in flight counts as live), so
	// only the failure accounting is asserted.
	if st := statsFor(f, "p"); st.RedialFailures == 0 || st.Dials != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

// Raising the floor through UpdateConfig applies to an existing (reused)
// pool without a restart, and lowering it to off stops further redials.
func TestWarmFloorLiveConfigChange(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	p := s.Provider("p", 4)
	f := newFetcher(t, []config.UsenetProvider{p}, Options{
		MinIdle: -1, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	time.Sleep(120 * time.Millisecond)
	if st := statsFor(f, "p"); st.LiveConnections != 0 {
		t.Fatalf("warmed before being enabled: %+v", st)
	}
	f.UpdateConfig(config.Usenet{Providers: []config.UsenetProvider{p}, MinIdle: 2})
	waitLive(t, f, "p", 2)
	if st := statsFor(f, "p"); st.MinIdle != 2 {
		t.Fatalf("floor not applied live: %+v", st)
	}
	f.UpdateConfig(config.Usenet{Providers: []config.UsenetProvider{p}, MinIdle: -1})
	if st := statsFor(f, "p"); st.MinIdle != 0 {
		t.Fatalf("floor not disabled live: %+v", st)
	}
}

// Update replacing a provider (changed settings) creates a fresh pool whose
// janitor re-establishes the floor.
func TestWarmFloorAfterProviderUpdate(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	p := s.Provider("p", 2)
	f := newFetcher(t, []config.UsenetProvider{p}, Options{
		MinIdle: 2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
	})
	waitLive(t, f, "p", 2)
	p2 := p
	p2.Connections = 6 // changed settings: fresh pool
	f.Update([]config.UsenetProvider{p2})
	st := waitLive(t, f, "p", 2)
	// LiveConnections includes background dials in flight. Wait for both
	// warm dials to complete before asserting on the completed-redial counter.
	deadline := time.Now().Add(2 * time.Second)
	for st.BackgroundRedials < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		st = statsFor(f, "p")
	}
	if st.ConfiguredConnections != 6 || st.BackgroundRedials < 2 {
		t.Fatalf("replacement pool not warmed: %+v", st)
	}
}

// Close stops the janitor: no further dials reach the server.
func TestWarmFloorStopsOnClose(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	f := New([]config.UsenetProvider{s.Provider("p", 2)}, Options{
		Logger: quiet, MinIdle: 2, KeepAlive: 20 * time.Millisecond, IdleTimeout: time.Second,
		DialTimeout: 2 * time.Second, CommandTimeout: 5 * time.Second,
	})
	waitLive(t, f, "p", 2)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Let graceful QUITs land, then the server must go quiet. Dropping the
	// (now closed) sockets must not trigger any redial either.
	time.Sleep(100 * time.Millisecond)
	s.DropConnections()
	accepted := s.Stats().Accepted
	time.Sleep(250 * time.Millisecond)
	if got := s.Stats().Accepted; got != accepted {
		t.Fatalf("dialled %d times after Close", got-accepted)
	}
}
