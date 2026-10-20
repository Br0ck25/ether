package playback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// TestDrainingRefusesOnlyNewSessions: a draining node keeps serving the
// sessions it has (a repeated Prepare returns the same session) but sends
// new viewers elsewhere with the stream-limit error clients retry on.
func TestDrainingRefusesOnlyNewSessions(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute), mkv("B", 10000, time.Minute)}, []string{"A", "B"})
	ctx := context.Background()
	ps, err := h.m.Prepare(ctx, owner, movieKey, defaultCaps("c1"))
	if err != nil {
		t.Fatal(err)
	}
	h.m.SetDraining(true)
	if !h.m.Draining() {
		t.Fatal("Draining() false after SetDraining(true)")
	}
	again, err := h.m.Prepare(ctx, owner, movieKey, defaultCaps("c1"))
	if err != nil || again.ID != ps.ID {
		t.Fatalf("existing session while draining: %v %v", again, err)
	}
	_, err = h.m.Prepare(ctx, owner, movie2, defaultCaps("c2"))
	if !errors.Is(err, core.ErrStreamLimit) || !strings.Contains(err.Error(), "restarting") {
		t.Fatalf("new session while draining: %v", err)
	}
	group := strings.Repeat("g", 20)
	if err := h.m.BeginChannel(owner, defaultCaps("c3"), group); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("live TV tune while draining: %v", err)
	}
	h.m.SetDraining(false)
	if _, err := h.m.Prepare(ctx, owner, movie2, defaultCaps("c2")); err != nil {
		t.Fatalf("after draining: %v", err)
	}
}
