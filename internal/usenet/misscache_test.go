package usenet

import (
	"slices"
	"testing"
	"time"
)

// TTL expiry must follow the injected clock (the breaker pattern), so the
// test advances time by assignment instead of sleeping.
func TestMissCacheExpiryWithInjectedClock(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := newMissCache(time.Minute, 10, func() time.Time { return now })

	m.add("a@x", "p1")
	m.add("a@x", "p2")
	if got := m.get("a@x"); !slices.Equal(got, []string{"p1", "p2"}) {
		t.Fatalf("get before expiry = %v", got)
	}
	now = now.Add(59 * time.Second)
	if got := m.get("a@x"); len(got) != 2 {
		t.Fatalf("get at 59s = %v, want still cached", got)
	}
	now = now.Add(2 * time.Second) // 61s: past the TTL
	if got := m.get("a@x"); got != nil {
		t.Fatalf("get after TTL = %v, want expired", got)
	}
	if m.len() != 0 {
		t.Fatalf("expired entry not evicted, len = %d", m.len())
	}
	// Re-adding after expiry starts a fresh TTL window.
	m.add("a@x", "p3")
	if got := m.get("a@x"); !slices.Equal(got, []string{"p3"}) {
		t.Fatalf("get after re-add = %v", got)
	}
	now = now.Add(time.Minute + time.Second)
	if got := m.get("a@x"); got != nil {
		t.Fatalf("get after second TTL = %v, want expired", got)
	}
}

func TestMissCacheNilClockDefaultsToWallClock(t *testing.T) {
	m := newMissCache(time.Minute, 10, nil)
	m.add("b@x", "p1")
	if got := m.get("b@x"); len(got) != 1 {
		t.Fatalf("get = %v", got)
	}
}
