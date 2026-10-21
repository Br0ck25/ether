package playback

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Broken alternates are opened side by side, so the stall is one open,
// not the sum of every broken one.
func TestFailoverRacesAlternates(t *testing.T) {
	slow := func(r *fakeRelease) *fakeRelease { r.ReadDelay = 400 * time.Millisecond; return r }
	b, c := slow(mkv("B", 100_000, time.Minute)), slow(mkv("C", 100_000, time.Minute))
	b.setFail(0, b.Size, nil)
	c.setFail(0, c.Size, nil)
	d := slow(mkv("D", 100_000, time.Minute))
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), b, c, d}, []string{"A", "B", "C", "D"})
	failures := make(chan string, 8)
	h.m.deps.Resolver = &observedFailureResolver{Resolver: h.resolver, failures: failures}
	var mu sync.Mutex
	var fetched []string
	h.m.deps.FetchNZB = func(ctx context.Context, r core.Release) ([]byte, error) {
		mu.Lock()
		fetched = append(fetched, r.ID)
		mu.Unlock()
		if r.ID == "D" {
			// A winning probe cancels its unfinished siblings. Observe both
			// failures before allowing D to win, rather than depending on
			// equal-delay timers firing in a particular order.
			waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			seen := map[string]bool{}
			for !seen["B"] || !seen["C"] {
				select {
				case id := <-failures:
					seen[id] = true
				case <-waitCtx.Done():
					return nil, waitCtx.Err()
				}
			}
		}
		return []byte("nzb-" + r.ID), nil
	}
	ps := h.prepare(owner, movieKey, "race")
	h.m.SetDecision(ps.ID, core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true})
	started := time.Now()
	ps2, err := h.m.Failover(context.Background(), ps.ID, 10*time.Second)
	if err != nil || ps2.Resolution.Release.ID != "D" {
		t.Fatalf("failover: %v %+v", err, ps2)
	}
	// Serially this is at least three slow opens.
	if took := time.Since(started); took > 1100*time.Millisecond {
		t.Fatalf("failover took %v", took)
	}
	for _, id := range []string{"B", "C"} {
		if h.resolver.isBad(id) == false {
			t.Errorf("broken alternate %s was not marked bad", id)
		}
	}
	// The next failover does not retry the broken ones.
	mu.Lock()
	fetched = nil
	mu.Unlock()
	if _, err := h.m.Failover(context.Background(), ps.ID, 20*time.Second); err == nil {
		t.Log("failover after alternates exhausted found a release")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range fetched {
		if id == "B" || id == "C" || id == "D" {
			t.Fatalf("retried %s", id)
		}
	}
}

type observedFailureResolver struct {
	core.Resolver
	failures chan string
}

func (r *observedFailureResolver) MarkBad(ctx context.Context, id, reason string) error {
	if err := r.Resolver.MarkBad(ctx, id, reason); err != nil {
		return err
	}
	select {
	case r.failures <- id:
	default:
	}
	return nil
}
