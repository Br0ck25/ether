package flight

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoSharesOneCall(t *testing.T) {
	var g Group[string, int]
	var calls atomic.Int32
	release := make(chan struct{})
	fn := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 7, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if v, err := g.Do(t.Context(), "k", fn); v != 7 || err != nil {
				t.Errorf("Do = %d, %v", v, err)
			}
		})
	}
	for g.Len() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 || g.Len() != 0 {
		t.Fatalf("calls=%d len=%d", calls.Load(), g.Len())
	}
}

func TestDoWaiterRetriesWhenLeaderGivesUp(t *testing.T) {
	var g Group[string, string]
	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	started := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, err := g.Do(leaderCtx, "k", func(ctx context.Context) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		})
		leaderDone <- err
	}()
	<-started
	got := make(chan string, 1)
	go func() {
		v, err := g.Do(t.Context(), "k", func(context.Context) (string, error) { return "mine", nil })
		if err != nil {
			t.Error(err)
		}
		got <- v
	}()
	time.Sleep(10 * time.Millisecond)
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v", err)
	}
	if v := <-got; v != "mine" {
		t.Fatalf("waiter got %q, want its own result", v)
	}
}

func TestDoSharesLeaderErrors(t *testing.T) {
	var g Group[int, int]
	boom := errors.New("boom")
	if _, err := g.Do(t.Context(), 1, func(context.Context) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestDoDetachedOutlivesCallers(t *testing.T) {
	var g Group[string, int]
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), struct{}{}, "v"))
	release := make(chan struct{})
	finished := make(chan int, 1)
	fn := func(fctx context.Context) (int, error) {
		<-release
		if fctx.Err() != nil || fctx.Value(struct{}{}) != "v" {
			finished <- -1
			return 0, fctx.Err()
		}
		finished <- 1
		return 1, nil
	}
	errc := make(chan error, 1)
	go func() {
		_, err := g.DoDetached(ctx, "k", fn)
		errc <- err
	}()
	for g.Len() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller err = %v", err)
	}
	waiter := make(chan int, 1)
	go func() {
		v, _ := g.DoDetached(t.Context(), "k", fn)
		waiter <- v
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	if v := <-finished; v != 1 {
		t.Fatal("detached call was canceled or lost the caller's values")
	}
	if v := <-waiter; v != 1 {
		t.Fatalf("waiter got %d", v)
	}
}
