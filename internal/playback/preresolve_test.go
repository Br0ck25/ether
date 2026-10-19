package playback

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestPreResolveReusedWithoutLiveSessionOrHistory(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("r1", 2<<30, 90*time.Minute)}, []string{"r1"})
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

type fakeAdopted struct {
	rel *fakeRelease
	src *fakeSource
}

func (f *fakeAdopted) Preferred(context.Context, core.RatingKey) (core.MediaSource, error) {
	f.src = &fakeSource{rel: f.rel}
	return f.src, nil
}
func (f *fakeAdopted) MediaInfo(context.Context, core.RatingKey) (*core.MediaInfo, error) {
	return &core.MediaInfo{Container: "mkv", Duration: f.rel.Duration, Size: f.rel.Size}, nil
}
func TestAdoptedMediaSkipsResolverAndNNTP(t *testing.T) {
	r := mkv("localfile", 1<<20, 90*time.Minute)
	h := newHarness(t, []*fakeRelease{r}, nil)
	local := &fakeAdopted{rel: r}
	h.m.deps.LocalMedia = local
	ps, err := h.m.Prepare(context.Background(), owner, movieKey, defaultCaps("client"))
	if err != nil {
		t.Fatal(err)
	}
	if h.engine.openCount() != 0 {
		t.Fatal("local media opened Usenet engine")
	}
	h.resolver.mu.Lock()
	calls := h.resolver.calls
	h.resolver.mu.Unlock()
	if calls != 0 {
		t.Fatal("local media used resolver")
	}
	if ps.Info.Size != r.Size || ps.Source == nil {
		t.Fatalf("missing local media %+v", ps)
	}
	h.m.Stop(ps.ID)
	// The source closes once the (cancelled) background index read ends.
	deadline := time.Now().Add(2 * time.Second)
	for !local.src.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !local.src.closed.Load() {
		t.Fatal("local file not closed")
	}
}
