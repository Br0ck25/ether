package playback

import (
	"context"
	"testing"
	"time"
)

func TestConstrainedClientDoesNotReuseLargePreparedSource(t *testing.T) {
	for _, warm := range []bool{false, true} {
		h := newHarness(t, []*fakeRelease{mkv("large", 10<<30, 90*time.Minute), mkv("small", 1<<30, 90*time.Minute)}, []string{"large"})
		caps := defaultCaps("client")
		ctx := context.Background()
		if warm {
			if !h.m.PreResolve(ctx, owner, movieKey, caps) {
				t.Fatal("warm skipped")
			}
			waitWarm(t, h, owner.ID, caps.ClientID, movieKey)
		} else {
			ps := h.prepare(owner, movieKey, caps.ClientID)
			h.m.Stop(ps.ID)
		}
		h.resolver.mu.Lock()
		h.resolver.candidates[0].ID = "small"
		h.resolver.mu.Unlock()
		caps.MaxBitrateKbps = 2000 // Slow Wi-Fi is constrained even inside the LAN.
		ps, err := h.m.Prepare(ctx, owner, movieKey, caps)
		if err != nil || ps.Resolution.Release.ID != "small" {
			t.Fatalf("warm=%v reused large source: %v %v", warm, ps, err)
		}
	}
}
