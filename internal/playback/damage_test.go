package playback

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestFirstDamagePreparesOneAlternateWithoutSwap(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "minor-damage")
	s := h.m.lookup(ps.ID)
	g := s.current()
	var calls atomic.Int32
	ready, release := make(chan struct{}), make(chan struct{})
	h.m.deps.FetchNZB = func(ctx context.Context, r core.Release) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(ready)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []byte("nzb-" + r.ID), nil
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); h.m.noteDamage(g, 100<<20, 0) }()
	}
	wg.Wait()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("first damage did not prepare")
	}
	if !h.m.noteDamage(g, 100<<20, 256<<10) {
		t.Fatal("isolated small gap rejected")
	}
	if s.current() != g || s.snapshot(time.Now()).Failovers != 0 || h.resolver.isBad("A") {
		t.Fatal("speculation changed source/release health")
	}
	close(release)
	waitDamagePrepared(t, g)
	if calls.Load() != 1 {
		t.Fatalf("duplicate preparations: %d", calls.Load())
	}
	// An actual escalation consumes the prepared NZB, avoiding another fetch.
	_, err := h.m.failover(context.Background(), s, g, 10*time.Second, "persistent damage")
	if err != nil {
		t.Fatal(err)
	}
	if s.current().releaseID() != "B" || calls.Load() != 1 {
		t.Fatalf("prepared alternate not reused: %s fetches=%d", s.current().releaseID(), calls.Load())
	}
}

func waitDamagePrepared(t *testing.T, g *generation) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		g.damage.mu.Lock()
		ready := g.damage.prepared != nil
		g.damage.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("alternate not prepared")
		case <-tick.C:
		}
	}
}

func TestDamageBudgetsAndRecoveryHysteresis(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "budgets")
	g := h.m.lookup(ps.ID).current()
	if !h.m.noteDamage(g, 10<<20, 256<<10) || !h.m.noteDamage(g, 12<<20, 256<<10) {
		t.Fatal("small sparse gaps rejected")
	}
	if h.m.noteDamage(g, 13<<20, 256<<10) {
		t.Fatal("third persistent gap accepted")
	}
	if !h.m.noteDamage(g, 60<<20, 256<<10) {
		t.Fatal("clean progress did not reset burst")
	}
	// Repeated isolated damage cannot evade a generation's cumulative budget.
	rejected := false
	for i := 1; i <= 20; i++ {
		if !h.m.noteDamage(g, int64(60+i*40)<<20, 512<<10) {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("unbounded lifetime concealment")
	}
}

func TestStopCancelsDamagePreparation(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute), mkv("B", 100_000, time.Minute)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "damage-cancel")
	g := h.m.lookup(ps.ID).current()
	started, cancelled := make(chan struct{}), make(chan struct{})
	h.m.deps.FetchNZB = func(ctx context.Context, _ core.Release) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	h.m.noteDamage(g, 100<<20, 0)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no preparation")
	}
	h.m.Stop(ps.ID)
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("stopped session kept speculative work")
	}
}

// Exercise the real copy loop's optional source capability, rather than only
// calling the policy directly. Probing must not enable payload concealment.
type damageHookEngine struct {
	core.Engine
	source *damageHookSource
}
type damageHookSource struct {
	core.MediaSource
	mu        sync.Mutex
	handler   func(int64, int64) bool
	installed int
}

func (e *damageHookEngine) Open(ctx context.Context, id string, nzb []byte) (core.MediaSource, error) {
	src, err := e.Engine.Open(ctx, id, nzb)
	if err != nil {
		return nil, err
	}
	e.source = &damageHookSource{MediaSource: src}
	return e.source, nil
}
func (s *damageHookSource) SetDamageHandler(f func(int64, int64) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = f
	s.installed++
}
func TestDamageHandlerEnabledOnlyForForegroundPlayback(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	engine := &damageHookEngine{Engine: h.engine}
	h.m.deps.Engine = engine
	ps := h.prepare(owner, movieKey, "damage-hook")
	g := h.m.lookup(ps.ID).current()
	src := engine.source
	src.mu.Lock()
	installed := src.installed
	src.mu.Unlock()
	if installed != 0 {
		t.Fatal("probe enabled concealment")
	}
	if _, rerr, werr := h.m.copyFrom(context.Background(), io.Discard, g, 0, 4096, func(int) {}, true); rerr != nil || werr != nil {
		t.Fatalf("background copy: %v %v", rerr, werr)
	}
	src.mu.Lock()
	installed = src.installed
	src.mu.Unlock()
	if installed != 0 {
		t.Fatal("prefetch enabled concealment")
	}
	for range 2 {
		if _, rerr, werr := h.m.copyFrom(context.Background(), io.Discard, g, 0, 4096, func(int) {}, false); rerr != nil || werr != nil {
			t.Fatalf("foreground copy: %v %v", rerr, werr)
		}
	}
	src.mu.Lock()
	installed = src.installed
	src.mu.Unlock()
	if installed != 1 {
		t.Fatalf("foreground handler installed %d times", installed)
	}
}
