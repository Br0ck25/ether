package stream

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type laneFetcher struct {
	*fakeFetcher
	gates             [3]chan struct{}
	started           chan core.Priority
	cancelled         chan core.Priority
	exitGate          chan struct{}
	mu                sync.Mutex
	calls             [3]int
	active, maxActive int
}

func newLaneFetcher() *laneFetcher {
	f := &laneFetcher{fakeFetcher: newFakeFetcher(), started: make(chan core.Priority, 32), cancelled: make(chan core.Priority, 32)}
	for i := range f.gates {
		f.gates[i] = make(chan struct{})
	}
	return f
}
func (f *laneFetcher) BodyPart(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	f.mu.Lock()
	f.calls[prio]++
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	f.started <- prio
	select {
	case <-f.gates[prio]:
	case <-ctx.Done():
		f.cancelled <- prio
		if f.exitGate != nil {
			<-f.exitGate
		}
		return nil, core.ArticlePart{}, ctx.Err()
	}
	return f.fakeFetcher.BodyPart(ctx, id, groups, prio)
}
func flightFixture(t *testing.T, f *laneFetcher) (*Engine, *release, *postedFile) {
	t.Helper()
	doc := post(t, f.fakeFetcher, 1024, postFile{name: "priority.mkv", data: mkvBytes(9, 1024)})
	e := newTestEngine(t, f, nil, Options{Retries: -1})
	rel, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.release(rel) })
	return e, rel, rel.files[0]
}
func waitLane(t *testing.T, ch <-chan core.Priority, want core.Priority) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("lane=%d want %d", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lane did not start/stop")
	}
}
func startLane(e *Engine, rel *release, pf *postedFile, ctx context.Context, p core.Priority) <-chan error {
	done := make(chan error, 1)
	go func() {
		seg, err := e.segment(ctx, rel, pf, 0, p, fetchOpts{})
		if err == nil && !bytes.Equal(seg.data, mkvBytes(9, 1024)) {
			err = errors.New("wrong decoded bytes")
		}
		done <- err
	}()
	return done
}
func waitLaneResult(t *testing.T, ch <-chan error, want error) {
	t.Helper()
	select {
	case err := <-ch:
		if !errors.Is(err, want) {
			t.Fatalf("result=%v want=%v", err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("article request did not finish")
	}
}

func TestLiveArticleDoesNotJoinQueuedSpeculation(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	preCtx, preCancel := context.WithCancel(context.Background())
	defer preCancel()
	pre := startLane(e, rel, pf, preCtx, core.PriorityPrefetch)
	waitLane(t, f.started, core.PriorityPrefetch)
	ahead := startLane(e, rel, pf, context.Background(), core.PriorityReadahead)
	waitLane(t, f.started, core.PriorityReadahead)
	live := startLane(e, rel, pf, context.Background(), core.PriorityLive)
	waitLane(t, f.started, core.PriorityLive)
	close(f.gates[core.PriorityLive])
	waitLaneResult(t, live, nil)
	// Lower-priority owners remain independent when a live fetch completes.
	select {
	case lane := <-f.cancelled:
		t.Fatalf("live completion cancelled independent lane %d", lane)
	default:
	}
	preCancel()
	waitLane(t, f.cancelled, core.PriorityPrefetch)
	waitLaneResult(t, pre, context.Canceled)
	close(f.gates[core.PriorityReadahead])
	waitLaneResult(t, ahead, nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxActive != 3 || f.calls != [3]int{1, 1, 1} {
		t.Fatalf("max=%d calls=%v", f.maxActive, f.calls)
	}
}

func TestSamePriorityWaitersCoalesceAndCancelIndependently(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := startLane(e, rel, pf, ctx, core.PriorityLive)
	waitLane(t, f.started, core.PriorityLive)
	second := startLane(e, rel, pf, context.Background(), core.PriorityLive)
	deadline := time.Now().Add(3 * time.Second)
	for e.Stats().Coalesced == 0 {
		if time.Now().After(deadline) {
			t.Fatal("same lane did not coalesce")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	waitLaneResult(t, first, context.Canceled)
	select {
	case lane := <-f.cancelled:
		t.Fatalf("one waiter cancelled shared lane %d", lane)
	default:
	}
	close(f.gates[core.PriorityLive])
	waitLaneResult(t, second, nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls[0] != 1 {
		t.Fatalf("same lane fetched %d times", f.calls[0])
	}
}

func TestCancelledLaneRemainsBoundedUntilWorkerExits(t *testing.T) {
	f := newLaneFetcher()
	f.exitGate = make(chan struct{})
	e, rel, pf := flightFixture(t, f)
	var releaseExit sync.Once
	t.Cleanup(func() { releaseExit.Do(func() { close(f.exitGate) }) })
	ctx, cancel := context.WithCancel(context.Background())
	first := startLane(e, rel, pf, ctx, core.PriorityLive)
	waitLane(t, f.started, core.PriorityLive)
	cancel()
	waitLaneResult(t, first, context.Canceled)
	waitLane(t, f.cancelled, core.PriorityLive)
	second := startLane(e, rel, pf, context.Background(), core.PriorityLive)
	// The tombstone remains visible until the canceled fetch acknowledges exit.
	e.mu.Lock()
	fl := e.flights[flightKey{segKey: segKey{rel.digest, pf.segBase}, priority: core.PriorityLive}]
	e.mu.Unlock()
	if fl == nil {
		t.Fatal("canceled lane disappeared before its worker exited")
	}
	releaseExit.Do(func() { close(f.exitGate) })
	waitLane(t, f.started, core.PriorityLive)
	close(f.gates[core.PriorityLive])
	waitLaneResult(t, second, nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxActive != 1 || f.calls[0] != 2 {
		t.Fatalf("overlapping canceled workers max=%d calls=%v", f.maxActive, f.calls)
	}
}

func TestEngineCloseJoinsEveryPriorityLane(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	var results []<-chan error
	for p := core.PriorityPrefetch; p >= core.PriorityLive; p-- {
		results = append(results, startLane(e, rel, pf, context.Background(), p))
		waitLane(t, f.started, p)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		waitLaneResult(t, result, context.Canceled)
	}
	f.mu.Lock()
	active := f.active
	f.mu.Unlock()
	e.mu.Lock()
	remaining := len(e.flights)
	e.mu.Unlock()
	if active != 0 || remaining != 0 {
		t.Fatalf("Close left active=%d flights=%d", active, remaining)
	}
}

func waitFlightWaiters(t *testing.T, e *Engine, rel *release, pf *postedFile, p core.Priority, want int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		e.mu.Lock()
		fl := e.flights[flightKey{segKey{rel.digest, pf.segBase}, p}]
		got := 0
		if fl != nil {
			got = fl.waiters
		}
		e.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("priority%d waiters=%d want%d", p, got, want)
		case <-poll.C:
		}
	}
}

func TestLiveHedgeReusesTimelySpeculativeDecode(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	e.hedgeDelay = time.Second // Synchronization test; production bound tested separately.
	ahead := startLane(e, rel, pf, context.Background(), core.PriorityReadahead)
	waitLane(t, f.started, core.PriorityReadahead)
	live := startLane(e, rel, pf, context.Background(), core.PriorityLive)
	waitFlightWaiters(t, e, rel, pf, core.PriorityReadahead, 2)
	close(f.gates[core.PriorityReadahead])
	waitLaneResult(t, ahead, nil)
	waitLaneResult(t, live, nil)
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != [3]int{0, 1, 0} {
		t.Fatalf("timely decode duplicated: calls=%v", calls)
	}
}

func TestLiveHedgeTimeoutUsesReservedLiveLane(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	aheadCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ahead := startLane(e, rel, pf, aheadCtx, core.PriorityReadahead)
	waitLane(t, f.started, core.PriorityReadahead)
	close(f.gates[core.PriorityLive])
	started := time.Now()
	live := startLane(e, rel, pf, context.Background(), core.PriorityLive)
	waitLane(t, f.started, core.PriorityLive)
	waitLaneResult(t, live, nil)
	if elapsed := time.Since(started); elapsed < liveHedgeDelay/2 || elapsed > 500*time.Millisecond {
		t.Fatalf("live hedge duration %v, target<=%v plus scheduling", elapsed, liveHedgeDelay)
	}
	select {
	case lane := <-f.cancelled:
		t.Fatalf("hedge timeout canceled independent lane%d", lane)
	default:
	}
	cancel()
	waitLaneResult(t, ahead, context.Canceled)
}

type blockedFlightCache struct {
	core.SegmentCache
	started, release chan struct{}
	once             sync.Once
}

func (c *blockedFlightCache) Get(string, int) ([]byte, bool) { return nil, false }
func (c *blockedFlightCache) Put(string, int, []byte) error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}
func TestLiveHedgeConsumesNoHotDecodeBeforeDiskPersistence(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	cache := &blockedFlightCache{started: make(chan struct{}), release: make(chan struct{})}
	e.cache = cache
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(cache.release) }) })
	close(f.gates[core.PriorityPrefetch])
	pre := make(chan error, 1)
	go func() {
		_, err := e.segment(context.Background(), rel, pf, 0, core.PriorityPrefetch, fetchOpts{cacheID: "disk", noHot: true})
		pre <- err
	}()
	waitLane(t, f.started, core.PriorityPrefetch)
	select {
	case <-cache.started:
	case <-time.After(3 * time.Second):
		t.Fatal("prefetch did not reach disk")
	}
	if e.hot.has(segKey{rel.digest, pf.segBase}) {
		t.Fatal("noHot prefetch populated hot before live use")
	}
	live := startLane(e, rel, pf, context.Background(), core.PriorityLive)
	waitLaneResult(t, live, nil)
	if !e.hot.has(segKey{rel.digest, pf.segBase}) {
		t.Fatal("live consumption failed to promote decoded bytes")
	}
	select {
	case err := <-pre:
		t.Fatalf("prefetch completed before durable write: %v", err)
	default:
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != [3]int{0, 0, 1} {
		t.Fatalf("disk wait caused duplicate BODY: %v", calls)
	}
	unblock.Do(func() { close(cache.release) })
	waitLaneResult(t, pre, nil)
}

func TestLiveHedgeCancellationReleasesOnlyItsReference(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	e.hedgeDelay = time.Second
	aheadCtx, aheadCancel := context.WithCancel(context.Background())
	defer aheadCancel()
	ahead := startLane(e, rel, pf, aheadCtx, core.PriorityReadahead)
	waitLane(t, f.started, core.PriorityReadahead)
	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	live := startLane(e, rel, pf, liveCtx, core.PriorityLive)
	waitFlightWaiters(t, e, rel, pf, core.PriorityReadahead, 2)
	liveCancel()
	waitLaneResult(t, live, context.Canceled)
	waitFlightWaiters(t, e, rel, pf, core.PriorityReadahead, 1)
	select {
	case lane := <-f.cancelled:
		t.Fatalf("canceled live hedge killed owner%d", lane)
	default:
	}
	aheadCancel()
	waitLaneResult(t, ahead, context.Canceled)
	waitLane(t, f.cancelled, core.PriorityReadahead)
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != [3]int{0, 1, 0} {
		t.Fatalf("cancellation started another lane: %v", calls)
	}
}

func TestSpeculativeLanesJoinExistingHigherPriority(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second core.Priority
	}{
		{"readahead joins live", core.PriorityLive, core.PriorityReadahead},
		{"prefetch joins live", core.PriorityLive, core.PriorityPrefetch},
		{"prefetch joins readahead", core.PriorityReadahead, core.PriorityPrefetch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaneFetcher()
			e, rel, pf := flightFixture(t, f)
			first := startLane(e, rel, pf, context.Background(), tc.first)
			waitLane(t, f.started, tc.first)
			second := startLane(e, rel, pf, context.Background(), tc.second)
			waitFlightWaiters(t, e, rel, pf, tc.first, 2)
			close(f.gates[tc.first])
			waitLaneResult(t, first, nil)
			waitLaneResult(t, second, nil)
			f.mu.Lock()
			calls := f.calls
			f.mu.Unlock()
			var expected [3]int
			expected[tc.first] = 1
			if calls != expected {
				t.Fatalf("lower lane duplicated higher BODY: got%v want%v", calls, expected)
			}
		})
	}
}

func TestDurablePrefetchJoinsLiveButWaitsForItsDiskWrite(t *testing.T) {
	f := newLaneFetcher()
	e, rel, pf := flightFixture(t, f)
	cache := &blockedFlightCache{started: make(chan struct{}), release: make(chan struct{})}
	e.cache = cache
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(cache.release) }) })
	live := make(chan error, 1)
	go func() {
		_, err := e.segment(context.Background(), rel, pf, 0, core.PriorityLive, fetchOpts{cacheID: "disk"})
		live <- err
	}()
	waitLane(t, f.started, core.PriorityLive)
	pre := make(chan error, 1)
	go func() {
		_, err := e.segment(context.Background(), rel, pf, 0, core.PriorityPrefetch, fetchOpts{cacheID: "disk", noHot: true})
		pre <- err
	}()
	waitFlightWaiters(t, e, rel, pf, core.PriorityLive, 2)
	close(f.gates[core.PriorityLive])
	waitLaneResult(t, live, nil)
	select {
	case <-cache.started:
	case <-time.After(3 * time.Second):
		t.Fatal("live did not persist")
	}
	select {
	case err := <-pre:
		t.Fatalf("durable prefetch completed before persistence: %v", err)
	default:
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != [3]int{1, 0, 0} {
		t.Fatalf("prefetch duplicated BODY: %v", calls)
	}
	unblock.Do(func() { close(cache.release) })
	waitLaneResult(t, pre, nil)
}
