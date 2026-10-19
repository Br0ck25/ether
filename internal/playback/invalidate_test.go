package playback

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestInvalidateTitleDropsWarmResolution(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("r1", 2<<30, 90*time.Minute)}, []string{"r1"})
	caps := defaultCaps("client")
	if !h.m.PreResolve(context.Background(), owner, movieKey, caps) {
		t.Fatal("warm skipped")
	}
	h.m.mu.Lock()
	e := h.m.warm[identity{owner.ID, caps.ClientID, movieKey}]
	h.m.mu.Unlock()
	select {
	case <-e.done:
	case <-time.After(time.Second):
		t.Fatal("warm did not complete")
	}
	if n := h.m.InvalidateTitle(movie2); n != 0 {
		t.Fatalf("other title dropped %d entries", n)
	}
	if n := h.m.InvalidateTitle(movieKey); n != 1 {
		t.Fatalf("dropped %d entries, want 1", n)
	}
	if _, err := h.m.Prepare(context.Background(), owner, movieKey, caps); err != nil {
		t.Fatal(err)
	}
	h.resolver.mu.Lock()
	calls := h.resolver.calls
	h.resolver.mu.Unlock()
	if calls != 2 {
		t.Fatalf("play reused an invalidated warm resolution: %d resolver calls", calls)
	}
}

func TestInvalidateTitleShowAndSeasonKeys(t *testing.T) {
	h := newHarness(t, nil, nil)
	ep := func(show int64, season, episode int) identity {
		return identity{userID: 1, clientID: "c", key: core.EpisodeKey(show, season, episode)}
	}
	reset := func() {
		h.m.mu.Lock()
		h.m.warm = map[identity]*warmEntry{
			ep(10, 1, 1): {}, ep(10, 2, 1): {}, ep(11, 1, 1): {},
			{userID: 1, clientID: "c", key: core.MovieKey(10)}: {},
		}
		h.m.mu.Unlock()
	}
	reset()
	if n := h.m.InvalidateTitle(core.SeasonKey(10, 1)); n != 1 {
		t.Fatalf("season dropped %d, want 1", n)
	}
	reset()
	if n := h.m.InvalidateTitle(core.ShowKey(10)); n != 2 {
		t.Fatalf("show dropped %d, want 2", n)
	}
	reset()
	if n := h.m.InvalidateTitle(core.EpisodeKey(11, 1, 1)); n != 1 {
		t.Fatalf("episode dropped %d, want 1", n)
	}
}
