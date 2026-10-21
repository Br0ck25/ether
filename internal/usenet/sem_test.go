package usenet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// acquireAsync starts an Acquire and returns a channel that receives its
// result.
func acquireAsync(s *prioSem, ctx context.Context, p core.Priority) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- s.Acquire(ctx, p) }()
	return ch
}

func waitQueued(t *testing.T, s *prioSem, p core.Priority, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.stats().Waiting[p] != n {
		if time.Now().After(deadline) {
			t.Fatalf("waiting[%d] = %d, want %d", p, s.stats().Waiting[p], n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSemPriorityOrder(t *testing.T) {
	s := newPrioSem(1, 1)
	ctx := context.Background()
	if err := s.Acquire(ctx, core.PriorityLive); err != nil {
		t.Fatal(err)
	}
	pre := acquireAsync(s, ctx, core.PriorityPrefetch)
	waitQueued(t, s, core.PriorityPrefetch, 1)
	ra := acquireAsync(s, ctx, core.PriorityReadahead)
	waitQueued(t, s, core.PriorityReadahead, 1)
	live := acquireAsync(s, ctx, core.PriorityLive)
	waitQueued(t, s, core.PriorityLive, 1)

	s.Release(core.PriorityLive)
	if err := <-live; err != nil {
		t.Fatal(err)
	}
	select {
	case <-ra:
		t.Fatal("readahead granted while slot held")
	case <-pre:
		t.Fatal("prefetch granted while slot held")
	default:
	}
	s.Release(core.PriorityLive)
	if err := <-ra; err != nil {
		t.Fatal(err)
	}
	s.Release(core.PriorityReadahead)
	if err := <-pre; err != nil {
		t.Fatal(err)
	}
	s.Release(core.PriorityPrefetch)
	if st := s.stats(); st.InUse != 0 {
		t.Fatalf("in use %d", st.InUse)
	}
}

func TestSemPrefetchCap(t *testing.T) {
	s := newPrioSem(4, 1)
	ctx := context.Background()
	if !s.TryAcquire(core.PriorityPrefetch) {
		t.Fatal("first prefetch should succeed")
	}
	if s.TryAcquire(core.PriorityPrefetch) {
		t.Fatal("prefetch above its cap")
	}
	// Live and readahead can still use the rest.
	for i := 0; i < 3; i++ {
		if !s.TryAcquire(core.PriorityLive) {
			t.Fatal("live should fit")
		}
	}
	if s.TryAcquire(core.PriorityLive) {
		t.Fatal("over capacity")
	}
	pre := acquireAsync(s, ctx, core.PriorityPrefetch)
	waitQueued(t, s, core.PriorityPrefetch, 1)
	// Freeing a live slot must not admit a second prefetch.
	s.Release(core.PriorityLive)
	select {
	case <-pre:
		t.Fatal("second prefetch admitted")
	case <-time.After(20 * time.Millisecond):
	}
	s.Release(core.PriorityPrefetch)
	if err := <-pre; err != nil {
		t.Fatal(err)
	}
}

func TestSemPrefetchYieldsToWaitingLive(t *testing.T) {
	s := newPrioSem(2, 2)
	ctx := context.Background()
	if !s.TryAcquire(core.PriorityPrefetch) || !s.TryAcquire(core.PriorityPrefetch) {
		t.Fatal("prefetch should take both slots")
	}
	live := acquireAsync(s, ctx, core.PriorityLive)
	waitQueued(t, s, core.PriorityLive, 1)
	pre := acquireAsync(s, ctx, core.PriorityPrefetch)
	waitQueued(t, s, core.PriorityPrefetch, 1)
	if s.TryAcquire(core.PriorityPrefetch) {
		t.Fatal("TryAcquire jumped the queue")
	}
	s.Release(core.PriorityPrefetch)
	if err := <-live; err != nil {
		t.Fatal(err)
	}
	select {
	case <-pre:
		t.Fatal("prefetch should still wait")
	default:
	}
	s.Release(core.PriorityPrefetch)
	if err := <-pre; err != nil {
		t.Fatal(err)
	}
}

func TestSemCancelReleases(t *testing.T) {
	s := newPrioSem(1, 1)
	if err := s.Acquire(context.Background(), core.PriorityLive); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := acquireAsync(s, ctx, core.PriorityLive)
	waitQueued(t, s, core.PriorityLive, 1)
	cancel()
	if err := <-ch; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if st := s.stats(); st.Waiting[core.PriorityLive] != 0 || st.InUse != 1 {
		t.Fatalf("stats %+v", st)
	}
	s.Release(core.PriorityLive)
	if !s.TryAcquire(core.PriorityLive) {
		t.Fatal("slot leaked")
	}
	// Already-cancelled context with a free slot still gets the slot
	// (the fast path), but with no free slot fails immediately.
	if err := s.Acquire(ctx, core.PriorityLive); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSemCancelUnblocksPrefetchBehind(t *testing.T) {
	// A cancelled live waiter must not leave a prefetch waiter stranded.
	s := newPrioSem(2, 1)
	if !s.TryAcquire(core.PriorityLive) || !s.TryAcquire(core.PriorityLive) {
		t.Fatal("setup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	live := acquireAsync(s, ctx, core.PriorityLive)
	waitQueued(t, s, core.PriorityLive, 1)
	pre := acquireAsync(s, context.Background(), core.PriorityPrefetch)
	waitQueued(t, s, core.PriorityPrefetch, 1)
	cancel()
	<-live
	s.Release(core.PriorityLive)
	select {
	case err := <-pre:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("prefetch stranded")
	}
}

func TestSemClose(t *testing.T) {
	s := newPrioSem(1, 1)
	_ = s.Acquire(context.Background(), core.PriorityLive)
	ch := acquireAsync(s, context.Background(), core.PriorityReadahead)
	waitQueued(t, s, core.PriorityReadahead, 1)
	s.Close()
	if err := <-ch; !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("err = %v", err)
	}
	if err := s.Acquire(context.Background(), core.PriorityLive); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("err = %v", err)
	}
	s.Release(core.PriorityLive) // holders may still release
}

func TestSemStress(t *testing.T) {
	s := newPrioSem(3, 1)
	var mu sync.Mutex
	inUse, prefetching, maxIn, maxPre := 0, 0, 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := core.Priority(i % 3)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%7)*time.Millisecond+time.Millisecond)
			defer cancel()
			if i%5 == 0 {
				ctx = context.Background()
			}
			if err := s.Acquire(ctx, p); err != nil {
				return
			}
			mu.Lock()
			inUse++
			if p == core.PriorityPrefetch {
				prefetching++
			}
			maxIn, maxPre = max(maxIn, inUse), max(maxPre, prefetching)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inUse--
			if p == core.PriorityPrefetch {
				prefetching--
			}
			mu.Unlock()
			s.Release(p)
		}(i)
	}
	wg.Wait()
	if maxIn > 3 || maxPre > 1 {
		t.Fatalf("max in use %d, max prefetch %d", maxIn, maxPre)
	}
	if st := s.stats(); st.InUse != 0 || st.Waiting != [3]int{} {
		t.Fatalf("leak: %+v", st)
	}
}

func TestSemReservedLiveCombinedBackgroundAndResize(t *testing.T) {
	s := newPrioSem(4, 4)
	s.reserveLive = true
	if !s.TryAcquire(core.PriorityPrefetch) || !s.TryAcquire(core.PriorityReadahead) || !s.TryAcquire(core.PriorityReadahead) {
		t.Fatal("background capacity unavailable")
	}
	if s.TryAcquire(core.PriorityReadahead) || s.TryAcquire(core.PriorityPrefetch) {
		t.Fatal("background consumed live reservation")
	}
	if !s.TryAcquire(core.PriorityLive) {
		t.Fatal("live reservation unavailable")
	}
	s.setSize(2, 1)
	s.Release(core.PriorityReadahead)
	s.Release(core.PriorityReadahead)
	if s.TryAcquire(core.PriorityReadahead) {
		t.Fatal("shrink ignored combined background limit")
	}
	s.Release(core.PriorityLive)
	if !s.TryAcquire(core.PriorityLive) {
		t.Fatal("shrink lost reserved slot")
	}
	s.Release(core.PriorityLive)
	s.Release(core.PriorityPrefetch)
	s.setSize(1, 1)
	if !s.TryAcquire(core.PriorityReadahead) {
		t.Fatal("single connection cannot make progress")
	}
	queued := acquireAsync(s, context.Background(), core.PriorityReadahead)
	waitQueued(t, s, core.PriorityReadahead, 1)
	s.setSize(3, 1)
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
	if !s.TryAcquire(core.PriorityLive) {
		t.Fatal("growth failed to retain reservation")
	}
	s.Release(core.PriorityLive)
	s.Release(core.PriorityReadahead)
	s.Release(core.PriorityReadahead)
}
