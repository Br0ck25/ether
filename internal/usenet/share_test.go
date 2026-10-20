package usenet

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func TestShares(t *testing.T) {
	cases := []struct {
		name     string
		capacity int
		weights  map[string]int
		want     map[string]int
	}{
		{"equal split", 20, map[string]int{"a": 1, "b": 1}, map[string]int{"a": 10, "b": 10}},
		{"remainder dropped", 10, map[string]int{"a": 1, "b": 1, "c": 1}, map[string]int{"a": 3, "b": 3, "c": 3}},
		{"zero weight counts as one", 10, map[string]int{"a": 0, "b": -3}, map[string]int{"a": 5, "b": 5}},
		{"weighted", 30, map[string]int{"gpu": 2, "b": 1}, map[string]int{"gpu": 20, "b": 10}},
		{"weighted floor", 10, map[string]int{"gpu": 3, "b": 1, "c": 1}, map[string]int{"gpu": 6, "b": 2, "c": 2}},
		// floor gives b and c 0; raising them to 1 overshoots, so the
		// largest gives one back each time.
		{"minimum one", 4, map[string]int{"gpu": 10, "b": 1, "c": 1}, map[string]int{"gpu": 2, "b": 1, "c": 1}},
		{"capacity equals nodes", 3, map[string]int{"a": 5, "b": 1, "c": 1}, map[string]int{"a": 1, "b": 1, "c": 1}},
		{"capacity below nodes", 2, map[string]int{"a": 1, "b": 1, "c": 1}, map[string]int{"a": 1, "b": 1, "c": 1}},
		{"single node", 8, map[string]int{"a": 3}, map[string]int{"a": 8}},
		{"no nodes", 8, nil, map[string]int{}},
	}
	for _, c := range cases {
		got := Shares(c.capacity, c.weights)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: Shares(%d, %v) = %v, want %v", c.name, c.capacity, c.weights, got, c.want)
		}
		if c.capacity >= len(c.weights) && sum(got) > c.capacity {
			t.Errorf("%s: total %d exceeds capacity %d", c.name, sum(got), c.capacity)
		}
	}
}

func TestSharesNeverExceedCapacity(t *testing.T) {
	for capacity := 1; capacity <= 40; capacity++ {
		for nodes := 1; nodes <= capacity; nodes++ {
			w := map[string]int{}
			for i := 0; i < nodes; i++ {
				w[fmt.Sprintf("n%02d", i)] = 1 + (i*7)%5
			}
			got := Shares(capacity, w)
			if sum(got) > capacity {
				t.Fatalf("cap %d nodes %d: total %d (%v)", capacity, nodes, sum(got), got)
			}
			for id, v := range got {
				if v < 1 {
					t.Fatalf("cap %d: node %s got %d", capacity, id, v)
				}
			}
		}
	}
}

func TestShareOfFollowsMembership(t *testing.T) {
	members := map[string]int{"a": 1}
	if got := ShareOf(20, "a", members); got != 20 {
		t.Fatalf("alone: %d", got)
	}
	members["b"] = 1
	if a, b := ShareOf(20, "a", members), ShareOf(20, "b", members); a != 10 || b != 10 {
		t.Fatalf("two nodes: %d %d", a, b)
	}
	members["c"] = 2
	if a, c := ShareOf(20, "a", members), ShareOf(20, "c", members); a != 5 || c != 10 {
		t.Fatalf("three nodes: a=%d c=%d", a, c)
	}
	// c leaves: its share is redistributed.
	delete(members, "c")
	if a := ShareOf(20, "a", members); a != 10 {
		t.Fatalf("after leave: %d", a)
	}
	// Not (yet) a member: keep the whole limit rather than guess.
	if got := ShareOf(20, "z", members); got != 20 {
		t.Fatalf("non-member: %d", got)
	}
}

// runBodies fetches n articles concurrently at mixed priorities.
func runBodies(t *testing.T, f *Fetcher, n int) { runBodiesAt(t, f, n, -1) }

// runBodiesAt fetches n articles concurrently at prio (negative: mixed).
func runBodiesAt(t *testing.T, f *Fetcher, n int, prio core.Priority) {
	t.Helper()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := prio
			if p < 0 {
				p = core.Priority(i % 3)
			}
			if _, err := f.Body(context.Background(), fmt.Sprintf("s%d@x", i%10), nil, p); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}

func TestConnectionShareLimitsPool(t *testing.T) {
	s := startShareServer(t)
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 6)}, Options{})
	f.SetConnectionShare(func(configured int) int { return configured / 3 })
	runBodies(t, f, 30)
	if st := s.Stats(); st.MaxActive > 2 {
		t.Fatalf("server saw %d connections with a share of 2", st.MaxActive)
	}
	ps := statsFor(f, "p")
	if ps.NodeShare != 2 || ps.MaxConnections != 2 || ps.ConfiguredConnections != 6 || f.MaxConnections() != 2 {
		t.Fatalf("stats %+v max=%d", ps, f.MaxConnections())
	}
	// Removing the share restores the configured count.
	f.SetConnectionShare(nil)
	if ps := statsFor(f, "p"); ps.NodeShare != 0 || ps.MaxConnections != 6 {
		t.Fatalf("after nil: %+v", ps)
	}
	// Shares are clamped to 1..configured.
	f.SetConnectionShare(func(int) int { return 0 })
	if ps := statsFor(f, "p"); ps.MaxConnections != 1 {
		t.Fatalf("clamp low: %+v", ps)
	}
	f.SetConnectionShare(func(int) int { return 99 })
	if ps := statsFor(f, "p"); ps.MaxConnections != 6 {
		t.Fatalf("clamp high: %+v", ps)
	}
}

func startShareServer(t *testing.T) *nntptest.Server {
	s := nntptest.Start(t, nntptest.Options{Latency: 5 * time.Millisecond})
	for i := 0; i < 10; i++ {
		addSeg(t, s, fmt.Sprintf("s%d@x", i), randData(int64(i), 5_000))
	}
	return s
}

// Shrinking lets in-flight requests finish, then closes connections above
// the new share; idle connections above it close at once.
func TestConnectionShareShrinksGracefully(t *testing.T) {
	s := startShareServer(t)
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 6)}, Options{MinIdle: -1})
	s.SetLatency(5 * time.Millisecond)
	runBodies(t, f, 30) // opens up to 6 connections, all idle afterwards
	if ps := statsFor(f, "p"); ps.LiveConnections < 3 {
		t.Fatalf("expected several open connections, got %+v", ps)
	}
	f.SetConnectionShare(func(int) int { return 2 })
	if ps := statsFor(f, "p"); ps.LiveConnections > 2 || ps.IdleConnections > 2 {
		t.Fatalf("idle connections above the share stay open: %+v", ps)
	}

	// In flight: six slow requests on six connections, then shrink to 1.
	f.SetConnectionShare(nil)
	s.SetLatency(300 * time.Millisecond)
	done := make(chan struct{})
	go func() { runBodiesAt(t, f, 6, core.PriorityLive); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for statsFor(f, "p").ActiveConnections < 6 {
		if time.Now().After(deadline) {
			t.Fatalf("requests never all started: %+v", statsFor(f, "p"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.SetConnectionShare(func(int) int { return 1 })
	<-done // every in-flight request completed (runBodies reports errors)
	if ps := statsFor(f, "p"); ps.LiveConnections > 1 || ps.MaxConnections != 1 {
		t.Fatalf("after in-flight requests finished: %+v", ps)
	}
}

// Growing hands the new slots to waiting requests at once.
func TestConnectionShareGrowWakesWaiters(t *testing.T) {
	s := startShareServer(t)
	s.SetLatency(100 * time.Millisecond)
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 4)}, Options{})
	f.SetConnectionShare(func(int) int { return 1 })
	done := make(chan struct{})
	go func() { runBodies(t, f, 8); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ps := statsFor(f, "p")
		if ps.ActiveConnections == 1 && ps.WaitingLive+ps.WaitingReadahead+ps.WaitingPrefetch > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no queue formed: %+v", ps)
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.SetConnectionShare(func(int) int { return 4 })
	deadline = time.Now().Add(3 * time.Second)
	for statsFor(f, "p").ActiveConnections < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("growth did not admit waiters: %+v", statsFor(f, "p"))
		}
		time.Sleep(2 * time.Millisecond)
	}
	<-done
}

// A provider added after the share is set gets its share immediately.
func TestConnectionShareAppliesToNewProviders(t *testing.T) {
	s := startShareServer(t)
	f := newFetcher(t, nil, Options{})
	f.SetConnectionShare(func(configured int) int { return configured / 2 })
	f.Update([]config.UsenetProvider{s.Provider("p", 10)})
	if ps := statsFor(f, "p"); ps.MaxConnections != 5 || ps.NodeShare != 5 {
		t.Fatalf("new provider: %+v", ps)
	}
}
