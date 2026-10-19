package prefetch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/playback"
)

type fakeCatalog struct {
	core.Catalog
	mu   sync.Mutex
	next map[core.RatingKey]*core.Title
}

func (c *fakeCatalog) NextEpisode(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.next[key]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, core.ErrNotFound
}

type fakeResolver struct {
	mu    sync.Mutex
	calls []core.ResolveRequest
	err   error
}

func (r *fakeResolver) Resolve(ctx context.Context, req core.ResolveRequest) (*core.Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	if r.err != nil {
		return nil, r.err
	}
	id := fmt.Sprintf("rel-%d", req.Title.Key)
	return &core.Resolution{Title: req.Title.Key, Release: core.Release{ID: id, Name: id, Size: 1 << 30}, NZB: []byte("nzb-" + id)}, nil
}
func (r *fakeResolver) MarkBad(ctx context.Context, id, reason string) error { return nil }
func (r *fakeResolver) Available(ctx context.Context, key core.RatingKey) (bool, bool) {
	return false, false
}
func (r *fakeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type call struct {
	ID             string
	NZB            string
	Offset, Length int64
}

type fakeEngine struct {
	mu       sync.Mutex
	calls    []call
	block    chan struct{} // when set, Prefetch waits on it
	cur, max atomic.Int32
}

func (e *fakeEngine) Open(ctx context.Context, id string, nzb []byte) (core.MediaSource, error) {
	return nil, errors.New("not used")
}

func (e *fakeEngine) Prefetch(ctx context.Context, id string, nzb []byte, off, n int64) error {
	c := e.cur.Add(1)
	defer e.cur.Add(-1)
	for {
		m := e.max.Load()
		if c <= m || e.max.CompareAndSwap(m, c) {
			break
		}
	}
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call{id, string(nzb), off, n})
	return nil
}

func (e *fakeEngine) got() []call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]call(nil), e.calls...)
}

type fakeHints struct {
	mu         sync.Mutex
	points     map[[2]int64]*playback.ResumePoint
	remembered []core.RatingKey
	caps       core.ClientCaps
	live       map[core.RatingKey]bool
}

func (h *fakeHints) ResumePoint(ctx context.Context, userID int64, key core.RatingKey) (*playback.ResumePoint, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.points[[2]int64{userID, int64(key)}], nil
}

func (h *fakeHints) ResumeFor(ctx context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps) (*playback.ResumePoint, error) {
	return h.ResumePoint(ctx, user.ID, key)
}

func (h *fakeHints) Remember(ctx context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps, res *core.Resolution) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remembered = append(h.remembered, key)
	return nil
}

func (h *fakeHints) ClientCaps(userID int64, key core.RatingKey) (core.ClientCaps, bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.caps, h.live[key], true
}

type fakeState struct {
	core.State
	byUser map[int64][]core.WatchStatus
}

func (s *fakeState) InProgress(ctx context.Context, userID int64, limit int) ([]core.WatchStatus, error) {
	return s.byUser[userID], nil
}

type fakeAuth struct {
	core.Auth
	users []core.User
}

func (a *fakeAuth) Users(ctx context.Context) ([]core.User, error) { return a.users, nil }

var (
	user = &core.User{ID: 7, Username: "u"}
	ep1  = core.EpisodeKey(100, 1, 1)
	ep2  = core.EpisodeKey(100, 1, 2)
	ep3  = core.EpisodeKey(100, 1, 3)
)

type fixture struct {
	p        *Prefetcher
	catalog  *fakeCatalog
	resolver *fakeResolver
	engine   *fakeEngine
	hints    *fakeHints
	state    *fakeState
	auth     *fakeAuth
}

func newFixture(t *testing.T, opts Options, mod func(*fixture)) *fixture {
	t.Helper()
	f := &fixture{
		catalog: &fakeCatalog{next: map[core.RatingKey]*core.Title{
			ep1: {Key: ep2, Type: core.TypeEpisode, Title: "Two"},
			ep2: {Key: ep3, Type: core.TypeEpisode, Title: "Three", Released: time.Now().Add(240 * time.Hour)},
		}},
		resolver: &fakeResolver{},
		engine:   &fakeEngine{},
		hints: &fakeHints{
			points: map[[2]int64]*playback.ResumePoint{},
			live:   map[core.RatingKey]bool{},
			caps:   core.ClientCaps{ClientID: "tv", MaxHeight: 2160},
		},
		state: &fakeState{byUser: map[int64][]core.WatchStatus{}},
		auth:  &fakeAuth{},
	}
	if mod != nil {
		mod(f)
	}
	cfg := config.Defaults(t.TempDir()).Cache
	p, err := New(Deps{Catalog: f.catalog, Resolver: f.resolver, Engine: f.engine, Hints: f.hints, State: f.state, Auth: f.auth}, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	f.p = p
	return f
}

func (f *fixture) wait(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.p.waitIdle(ctx); err != nil {
		t.Fatal("prefetcher never went idle")
	}
}

func TestNextEpisodeTriggerAndDedupe(t *testing.T) {
	f := newFixture(t, Options{}, nil)
	dur := 50 * time.Minute

	f.p.OnTimeline(user, ep1, 10*time.Minute, dur, "playing") // 20%: nothing
	f.wait(t)
	if f.resolver.count() != 0 {
		t.Fatal("prefetched too early")
	}
	for i := 0; i < 5; i++ {
		f.p.OnTimeline(user, ep1, 41*time.Minute+time.Duration(i)*time.Second, dur, "playing")
	}
	f.wait(t)
	if n := f.resolver.count(); n != 1 {
		t.Fatalf("Resolve calls = %d", n)
	}
	req := f.resolver.calls[0]
	if req.Title.Key != ep2 || req.Client.ClientID != "tv" || req.User.ID != user.ID {
		t.Fatalf("resolve req = %+v", req)
	}
	calls := f.engine.got()
	want := call{fmt.Sprintf("rel-%d", ep2), fmt.Sprintf("nzb-rel-%d", ep2), 0, 64 << 20}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("prefetch calls = %+v, want %+v", calls, want)
	}
	if len(f.hints.remembered) != 1 || f.hints.remembered[0] != ep2 {
		t.Fatalf("remembered = %v", f.hints.remembered)
	}
	if s := f.p.Stats(); s.Completed != 1 || s.Failed != 0 {
		t.Fatalf("stats = %+v", s)
	}

	// Unaired next episode: no resolve. Finales and movies: nothing.
	f.p.OnTimeline(user, ep2, 45*time.Minute, dur, "playing")
	f.p.OnTimeline(user, ep3, 45*time.Minute, dur, "playing")
	f.p.OnTimeline(user, core.MovieKey(5), 45*time.Minute, dur, "playing")
	f.wait(t)
	if n := f.resolver.count(); n != 1 {
		t.Fatalf("Resolve calls = %d", n)
	}
}

func TestNextEpisodeUsesStoredRelease(t *testing.T) {
	f := newFixture(t, Options{}, func(f *fixture) {
		f.hints.points[[2]int64{user.ID, int64(ep2)}] = &playback.ResumePoint{
			Release: core.Release{ID: "stored"}, NZB: []byte("n"), Size: 10 << 20,
		}
	})
	f.p.OnTimeline(user, ep1, 49*time.Minute, 50*time.Minute, "playing")
	f.wait(t)
	if f.resolver.count() != 0 {
		t.Fatal("resolved despite a stored release")
	}
	if c := f.engine.got(); len(c) != 1 || c[0].ID != "stored" || c[0].Length != 10<<20 {
		t.Fatalf("calls = %+v", c)
	}
}

func TestNextEpisodeDisabledAndThreshold(t *testing.T) {
	f := newFixture(t, Options{}, nil)
	cfg := config.Defaults(t.TempDir()).Cache
	cfg.NextEpisodeAt = 0.95
	f.p.SetConfig(cfg)
	f.p.OnTimeline(user, ep1, 45*time.Minute, 50*time.Minute, "playing") // 90%
	f.wait(t)
	if f.resolver.count() != 0 {
		t.Fatal("threshold ignored")
	}
	cfg.PrefetchNextEpisode = false
	f.p.SetConfig(cfg)
	f.p.OnTimeline(user, ep1, 49*time.Minute, 50*time.Minute, "playing")
	f.wait(t)
	if f.resolver.count() != 0 {
		t.Fatal("disabled prefetch ran")
	}
}

func TestFailedJobRetriesAfterCooloff(t *testing.T) {
	var (
		mu  sync.Mutex
		now = time.Now()
	)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f := newFixture(t, Options{Now: clock, RetryAfter: time.Minute}, func(f *fixture) {
		f.resolver.err = errors.New("indexer down")
	})
	f.p.OnTimeline(user, ep1, 49*time.Minute, 50*time.Minute, "playing")
	f.wait(t)
	f.p.OnTimeline(user, ep1, 49*time.Minute, 50*time.Minute, "playing")
	f.wait(t)
	if n := f.resolver.count(); n != 1 {
		t.Fatalf("retried within cool-off: %d", n)
	}
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	f.p.OnTimeline(user, ep1, 49*time.Minute, 50*time.Minute, "playing")
	f.wait(t)
	if n := f.resolver.count(); n != 2 {
		t.Fatalf("not retried after cool-off: %d", n)
	}
	if s := f.p.Stats(); s.Failed != 2 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestConcurrencyLimitAndNonBlocking(t *testing.T) {
	f := newFixture(t, Options{Concurrency: 2, QueueSize: 3}, func(f *fixture) {
		f.engine.block = make(chan struct{})
		for i := 1; i <= 10; i++ {
			f.catalog.next[core.EpisodeKey(200, 1, i)] = &core.Title{Key: core.EpisodeKey(200, 1, i+1), Type: core.TypeEpisode}
		}
	})
	start := time.Now()
	for i := 1; i <= 2; i++ {
		f.p.OnTimeline(user, core.EpisodeKey(200, 1, i), 49*time.Minute, 50*time.Minute, "playing")
	}
	// Wait for both workers to be busy, then overfill the queue.
	deadline := time.Now().Add(5 * time.Second)
	for f.engine.cur.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("workers never started")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 3; i <= 10; i++ {
		f.p.OnTimeline(user, core.EpisodeKey(200, 1, i), 49*time.Minute, 50*time.Minute, "playing")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("OnTimeline blocked")
	}
	if s := f.p.Stats(); s.Dropped != 5 || s.Running != 2 || s.Queued != 3 {
		t.Fatalf("stats = %+v", s)
	}
	close(f.engine.block)
	f.wait(t)
	if m := f.engine.max.Load(); m != 2 {
		t.Fatalf("max concurrent prefetches = %d", m)
	}
	if n := len(f.engine.got()); n != 5 { // 2 running + 3 queued; the rest dropped
		t.Fatalf("prefetches = %d", n)
	}
}

func TestKeepWarm(t *testing.T) {
	movie := core.MovieKey(42)
	f := newFixture(t, Options{WarmBehind: 1 << 20}, func(f *fixture) {
		f.hints.points[[2]int64{user.ID, int64(movie)}] = &playback.ResumePoint{
			Release: core.Release{ID: "m"}, NZB: []byte("nzb-m"), Size: 1000 << 20, Duration: 100 * time.Minute,
		}
		f.hints.points[[2]int64{user.ID, int64(ep1)}] = &playback.ResumePoint{
			Release: core.Release{ID: "e"}, NZB: []byte("nzb-e"), Size: 100 << 20, Duration: 50 * time.Minute,
		}
		f.hints.live[ep1] = true // being watched right now
		f.state.byUser[user.ID] = []core.WatchStatus{
			{Key: movie, ViewOffset: 50 * time.Minute},
			{Key: ep1, ViewOffset: 10 * time.Minute},
			{Key: ep2},                                        // on deck, not started
			{Key: core.MovieKey(43), ViewOffset: time.Minute}, // no stored release
		}
		f.auth.users = []core.User{*user}
	})

	if err := f.p.warmPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	calls := f.engine.got()
	want := call{"m", "nzb-m", 500<<20 - 1<<20, 1<<20 + 64<<20}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("warm calls = %+v, want %+v", calls, want)
	}
	// A second pass within the interval is deduplicated.
	if err := f.p.warmPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	if n := len(f.engine.got()); n != 1 {
		t.Fatalf("warm repeated: %d", n)
	}
	// WarmNow runs a pass through the loop (still deduplicated).
	f.p.WarmNow()
	time.Sleep(50 * time.Millisecond)
	f.wait(t)
	if n := len(f.engine.got()); n != 1 {
		t.Fatalf("WarmNow repeated: %d", n)
	}
}

func TestWarmWindow(t *testing.T) {
	start, n := WarmWindow(30*time.Second, time.Minute, 1000, 100, 200)
	if start != 400 || n != 300 {
		t.Fatalf("window = %d+%d", start, n)
	}
	start, n = WarmWindow(time.Second, time.Minute, 6000, 500, 200)
	if start != 0 || n != 300 {
		t.Fatalf("clamped start = %d+%d", start, n)
	}
	start, n = WarmWindow(59*time.Second, time.Minute, 6000, 100, 500)
	if start != 5800 || n != 200 {
		t.Fatalf("clamped end = %d+%d", start, n)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	f := newFixture(t, Options{}, nil)
	f.p.Close()
	f.p.Close()
	f.p.OnTimeline(user, ep1, 49*time.Minute, 50*time.Minute, "playing") // ignored
	if f.resolver.count() != 0 {
		t.Fatal("ran after close")
	}
}
