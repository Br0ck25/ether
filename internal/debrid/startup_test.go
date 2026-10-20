package debrid

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestPersistedPayloadSkipsTrackerLookupWithoutHash(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	h.cacheEverywhere(testHash)
	inner := &resolvingInner{}
	x := NewIndexer(inner, h.svc)
	rel := core.Release{ID: "detail-only", Protocol: core.ProtocolTorrent}
	first, err := x.FetchNZB(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	calls := h.provs["torbox-0"].totalCalls()
	// A fresh wrapper models a later request, without an in-memory flight.
	x = NewIndexer(inner, h.svc)
	second, err := x.FetchNZB(context.Background(), rel)
	if err != nil || string(second) != string(first) || inner.resolved != 1 || h.provs["torbox-0"].totalCalls() != calls {
		t.Fatalf("payload reuse fetched tracker/provider again: resolves=%d err=%v", inner.resolved, err)
	}
	// The same season pack's other episode must not reuse the wrong file.
	rel.Target = &core.ReleaseTarget{Season: 1, Episode: 2}
	_, _ = x.FetchNZB(context.Background(), rel)
	if inner.resolved != 2 {
		t.Fatal("distinct episode reused old descriptor")
	}
	// A changed hash must invalidate the persisted descriptor.
	rel.Target = nil
	rel.InfoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err = x.FetchNZB(context.Background(), rel); err == nil {
		t.Fatal("changed hash reused old payload")
	}
}

type gatedTorrentInner struct {
	fakeInner
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (r *gatedTorrentInner) ResolveTorrent(ctx context.Context, rel core.Release) (core.Release, error) {
	r.calls.Add(1)
	select {
	case r.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return rel, ctx.Err()
	case <-r.release:
	}
	rel.InfoHash = testHash
	return rel, nil
}

func TestTorrentFlightWaiterCancellationDoesNotCancelLeader(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	h.cacheEverywhere(testHash)
	inner := &gatedTorrentInner{entered: make(chan struct{}, 10), release: make(chan struct{})}
	x := NewIndexer(inner, h.svc)
	rel := core.Release{ID: "shared", Protocol: core.ProtocolTorrent}
	leader := make(chan error, 1)
	go func() { _, err := x.FetchNZB(context.Background(), rel); leader <- err }()
	<-inner.entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := x.FetchNZB(ctx, rel); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter cancellation: %v", err)
	}
	close(inner.release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 1 || h.provs["torbox-0"].callCount("acquire") != 1 {
		t.Fatal("concurrent request duplicated acquisition")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.flights) != 0 {
		t.Fatal("completed flight leaked")
	}
}

func TestTorrentFlightRetriesAfterCancelledLeader(t *testing.T) {
	h := newHarness(t, 1<<20, "torbox")
	h.cacheEverywhere(testHash)
	inner := &gatedTorrentInner{entered: make(chan struct{}, 10), release: make(chan struct{})}
	x := NewIndexer(inner, h.svc)
	rel := core.Release{ID: "retry", Protocol: core.ProtocolTorrent}
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() { _, err := x.FetchNZB(ctx, rel); leader <- err }()
	<-inner.entered
	follower := make(chan error, 1)
	go func() { _, err := x.FetchNZB(context.Background(), rel); follower <- err }()
	cancel()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error %v", err)
	}
	<-inner.entered
	close(inner.release)
	if err := <-follower; err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 2 {
		t.Fatal("waiter did not retry cancelled acquisition")
	}
}
