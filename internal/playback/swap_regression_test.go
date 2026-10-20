package playback

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// closeHookEngine calls onClose when a source of release hookID closes.
type closeHookEngine struct {
	core.Engine
	hookID  string
	onClose func()
}

type closeHookSource struct {
	core.MediaSource
	once    sync.Once
	onClose func()
}

func (s *closeHookSource) Close() error {
	err := s.MediaSource.Close()
	s.once.Do(s.onClose)
	return err
}

func (e *closeHookEngine) Open(ctx context.Context, releaseID string, nzb []byte) (core.MediaSource, error) {
	src, err := e.Engine.Open(ctx, releaseID, nzb)
	if err != nil || releaseID != e.hookID {
		return src, err
	}
	return &closeHookSource{MediaSource: src, onClose: e.onClose}, nil
}

// A Stop landing right after a failover published the new release must
// release that release's live pin; it used to be taken after the swap and
// so outlived the session.
func TestStopDuringFailoverSwapDropsLivePin(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, 100*time.Second), mkv("B", 100_000, 100*time.Second)}, []string{"A", "B"})
	var sessionID string
	h.m.deps.Engine = &closeHookEngine{Engine: h.engine, hookID: "A", onClose: func() { h.m.Stop(sessionID) }}
	ps := h.prepare(owner, movieKey, "c1")
	sessionID = ps.ID
	g := h.m.lookup(ps.ID).current()
	<-g.indexReady
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.refs == 0 })
	if !h.cache.pinned("A", PinReasonLive) {
		t.Fatal("A not pinned while live")
	}

	// Retiring A (inside the swap) stops the session before the failover
	// returns.
	_, _ = h.m.Failover(context.Background(), ps.ID, 10*time.Second)
	if _, ok := h.m.Session(ps.ID); ok {
		t.Fatal("session still live")
	}
	for _, id := range []string{"A", "B"} {
		if h.cache.pinned(id, PinReasonLive) {
			t.Fatalf("%s still pinned live after the session stopped", id)
		}
	}
	h.m.pinMu.Lock()
	defer h.m.pinMu.Unlock()
	if len(h.m.livePins) != 0 {
		t.Fatalf("live pin counts leaked: %v", h.m.livePins)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// After a TS splice onto a lower-bitrate release, the remaining virtual
// range can extend past the new file's end. That is the end of a healthy
// source, not a read failure: it must end the response, not fail over
// again and discard the release just swapped in.
func TestSplicedShortSourceEndsResponseWithoutFailover(t *testing.T) {
	const sizeA, sizeB = 188 * 3000, 188 * 1000
	a := &fakeRelease{ID: "A", Name: "A.ts", Size: sizeA, Duration: 50 * time.Second, FailFrom: 188*2000 + 17, FailTo: 188 * 2100}
	b := &fakeRelease{ID: "B", Name: "B.ts", Size: sizeB, Duration: 50 * time.Second}
	c := &fakeRelease{ID: "C", Name: "C.ts", Size: sizeA, Duration: 50 * time.Second}
	h := newHarness(t, []*fakeRelease{a, b, c}, []string{"A", "B", "C"})
	ps := h.prepare(owner, movieKey, "c1")
	srv := directPlayServer(t, h.m, ps.ID)

	resp, body, _ := get(t, srv.URL, nil)
	if resp.StatusCode != 200 || int64(len(body)) >= sizeA {
		t.Fatalf("status %d, %d bytes; want a short body", resp.StatusCode, len(body))
	}
	st, ok := h.m.Session(ps.ID)
	if !ok {
		t.Fatal("session ended")
	}
	if st.Resolution.Release.ID != "B" || st.Failovers != 1 {
		t.Fatalf("release %s after %d failovers; want B after 1", st.Resolution.Release.ID, st.Failovers)
	}
}
