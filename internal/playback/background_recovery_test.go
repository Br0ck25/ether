package playback

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestConcurrentSourceErrorsShareOneBackgroundRecovery(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "recovery-storm")
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	h.m.deps.FetchNZB = func(ctx context.Context, r core.Release) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, core.ErrSourceUnavailable
	}
	s := h.m.lookup(ps.ID)
	g := s.current()
	h.m.backgroundFailover(s, g, 10*time.Second)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not start")
	}
	for range 100 {
		h.m.backgroundFailover(s, g, 10*time.Second)
	}
	close(release)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for g.backgroundFailover.Load() {
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("background recovery did not finish")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("one failed source queued %d recovery attempts", calls.Load())
	}
}

func TestStopCancelsRecoveryProviderWorkImmediately(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "cancel-recovery")
	started, cancelled := make(chan struct{}), make(chan struct{})
	h.m.deps.FetchNZB = func(ctx context.Context, r core.Release) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	s := h.m.lookup(ps.ID)
	h.m.backgroundFailover(s, s.current(), 10*time.Second)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not start")
	}
	h.m.Stop(ps.ID)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped playback kept provider work alive")
	}
	// Manager.Close also joins the worker; its normal janitor, speed and
	// pin loops remain live until then.

}

type recoveryContextHLS struct {
	*fakeHLS
	contextErr chan error
}

func (h *recoveryContextHLS) SwapInput(ctx context.Context, url string, info core.MediaInfo, at time.Duration) error {
	h.contextErr <- ctx.Err()
	return h.fakeHLS.SwapInput(ctx, url, info, at)
}

func TestBackgroundRecoveryKeepsSwapContextAliveAfterRetiringOldInput(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "recovery-swap")
	actual := h.tr.addHLS(ps.ID)
	wrapped := &recoveryContextHLS{fakeHLS: actual, contextErr: make(chan error, 1)}
	h.m.deps.HLSLookup = func(string) (core.HLSSession, bool) { return wrapped, true }
	s := h.m.lookup(ps.ID)
	old := s.current()
	h.m.backgroundFailover(s, old, 10*time.Second)
	select {
	case err := <-wrapped.contextErr:
		if err != nil {
			t.Fatalf("retiring the old input cancelled the replacement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input replacement did not run")
	}
	if old.ctx.Err() == nil {
		t.Fatal("old source was not retired before swapping the encoder")
	}
}

// A speed-triggered down-failover is background recovery too: stopping the
// session must cancel its resolver/provider work at once instead of letting
// it run out FailoverTimeout on a playback nobody watches.
func TestStopCancelsDownFailoverProviderWork(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "cancel-down")
	started, cancelled := make(chan struct{}), make(chan struct{})
	h.m.deps.FetchNZB = func(ctx context.Context, r core.Release) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	s := h.m.lookup(ps.ID)
	h.m.backgroundDownFailover(s, s.current(), 10*time.Second)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("down-failover did not start")
	}
	h.m.Stop(ps.ID)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped playback kept down-failover provider work alive")
	}
}
