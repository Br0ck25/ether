package playback

import (
	"context"
	"testing"
	"time"
)

func TestPreResolveReusedWithoutLiveSessionOrHistory(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("r1", 2<<30, 90*time.Minute)}, []string{"r1"})
	// Keep the first warm in flight while the second asks: the per-user
	// budget counts warms that are still running.
	h.resolver.mu.Lock()
	h.resolver.delay = 200 * time.Millisecond
	h.resolver.mu.Unlock()
	caps := defaultCaps("client")
	if !h.m.PreResolve(context.Background(), owner, movieKey, caps) {
		t.Fatal("first warm skipped")
	}
	if h.m.PreResolve(context.Background(), owner, movie2, caps) {
		t.Fatal("per-user budget ignored")
	}
	h.m.mu.Lock()
	e := h.m.warm[identity{owner.ID, caps.ClientID, movieKey}]
	h.m.mu.Unlock()
	select {
	case <-e.done:
	case <-time.After(time.Second):
		t.Fatal("warm did not complete")
	}
	if len(h.m.Sessions()) != 0 {
		t.Fatal("speculation created live session")
	}
	// Metadata omits decision-only codec and bandwidth hints. The same profile
	// must reuse its release and probe even several minutes later.
	h.clock.Advance(5 * time.Minute)
	caps.MaxBitrateKbps = 20000
	caps.VideoCodecs = []string{"h264"}
	ps, err := h.m.Prepare(context.Background(), owner, movieKey, caps)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Resolution.Release.ID != "r1" {
		t.Fatalf("wrong warm release %+v", ps)
	}
	h.resolver.mu.Lock()
	calls := h.resolver.calls
	h.resolver.mu.Unlock()
	h.tr.mu.Lock()
	probes := h.tr.probes
	h.tr.mu.Unlock()
	if probes != 1 {
		t.Fatalf("warm probe was repeated: %d", probes)
	}
	if calls != 1 {
		t.Fatalf("resolved again: %d calls", calls)
	}
}

func TestCachePolicyCanDisableSpeculation(t *testing.T) {
	for _, mode := range []string{"off", "detail", "all"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, []*fakeRelease{mkv("r1", 2<<30, 90*time.Minute)}, []string{"r1"})
			cfg := h.m.config()
			switch mode {
			case "off":
				cfg.Cache.Mode = "off"
			case "detail":
				cfg.Cache.DetailWarmup = "off"
			case "all":
				cfg.Cache.DisableSpeculation = true
			}
			h.m.SetConfig(cfg)
			if h.m.PreResolve(context.Background(), owner, movieKey, defaultCaps("client")) {
				t.Fatal("disabled warm was admitted")
			}
			h.resolver.mu.Lock()
			defer h.resolver.mu.Unlock()
			if h.resolver.calls != 0 {
				t.Fatal("disabled warming reached resolver")
			}
		})
	}
}
