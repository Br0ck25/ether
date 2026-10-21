package segcache

import (
	"testing"
	"time"
)

func TestPolicyExpiryHonoursPinsAndLiveProtection(t *testing.T) {
	c := openT(t, t.TempDir(), 1000)
	now := time.Now()
	c.now = func() time.Time { return now }
	for _, id := range []string{"idle", "pinned", "live"} {
		mustPut(t, c, id, 0, []byte("payload"))
	}
	if err := c.Pin("pinned", "owner"); err != nil {
		t.Fatal(err)
	}
	c.Protect(func() map[string]bool { return map[string]bool{"live": true} })
	now = now.Add(48 * time.Hour)
	c.SetPolicy(Policy{Retention: 24 * time.Hour})
	if c.Has("idle", 0) || !c.Has("pinned", 0) || !c.Has("live", 0) {
		t.Fatal("expiry did not preserve only protected bytes")
	}
	if c.Metrics().EvictedBytes != 7 {
		t.Fatal(c.Metrics())
	}
	checkInvariants(t, c)
}

func TestOffBypassesWritesAndZeroBudgetStillMeansUnlimited(t *testing.T) {
	c := openT(t, t.TempDir(), 0)
	mustPut(t, c, "old", 0, []byte("old"))
	c.SetPolicy(Policy{Mode: "off"})
	mustPut(t, c, "new", 0, []byte("new"))
	if used, _ := c.Usage(); used != 0 {
		t.Fatal("off retained bytes", used)
	}
	c.SetPolicy(Policy{Mode: "demand"})
	mustPut(t, c, "new", 0, []byte("new"))
	if !c.Has("new", 0) {
		t.Fatal("zero budget unexpectedly disabled cache")
	}
}

func TestAdaptiveKeepsReplayButExpiryWins(t *testing.T) {
	c := openT(t, t.TempDir(), 20)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.SetPolicy(Policy{Mode: "adaptive", ReplayBoost: 24 * time.Hour, Retention: 48 * time.Hour})
	mustPut(t, c, "favourite", 0, make([]byte, 10))
	c.RecordPlay("favourite", "Favourite", 100)
	c.RecordPlay("favourite", "Favourite", 100)
	now = now.Add(time.Hour)
	mustPut(t, c, "once", 0, make([]byte, 10))
	now = now.Add(time.Hour)
	mustPut(t, c, "new", 0, make([]byte, 10))
	if !c.Has("favourite", 0) || c.Has("once", 0) || !c.Has("new", 0) {
		t.Fatal("replay credit ignored")
	}
	now = now.Add(72 * time.Hour)
	c.Sweep()
	if c.Has("favourite", 0) {
		t.Fatal("replay credit overrode expiry")
	}
	checkInvariants(t, c)
}

func TestPlaybackMetadataAndCounters(t *testing.T) {
	dir := t.TempDir()
	c := openT(t, dir, 100)
	mustPut(t, c, "r", 0, []byte("cached"))
	c.RecordPlay("r", "A movie", 40)
	c.RecordPlay("r", "A movie", 40)
	if _, ok := c.Get("r", 0); !ok {
		t.Fatal("missing bytes")
	}
	c.Get("r", 1)
	c.Get("absent", 0)
	if m := c.Metrics(); m.Hits != 1 || m.Misses != 2 || m.ServedBytes != 6 {
		t.Fatal(m)
	}
	c.Close()
	again := openT(t, dir, 100)
	r := again.Releases()[0]
	if r.Title != "A movie" || r.Plays != 2 || r.Size != 40 || r.LastPlayed.IsZero() {
		t.Fatalf("lost metadata: %+v", r)
	}
	if again.Metrics().Hits != 0 {
		t.Fatal("metrics should reset")
	}
}

func TestAdaptiveProtectsLargeFavouriteAcrossManySegments(t *testing.T) {
	c := openT(t, t.TempDir(), 1001)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.SetPolicy(Policy{Mode: "adaptive", ReplayBoost: 24 * time.Hour})
	for i := 0; i < 1000; i++ {
		mustPut(t, c, "large", i, []byte{1})
	}
	c.RecordPlay("large", "Large", 1000)
	c.RecordPlay("large", "Large", 1000)
	now = now.Add(time.Hour)
	mustPut(t, c, "one-off", 0, []byte{1})
	now = now.Add(time.Hour)
	mustPut(t, c, "new", 0, []byte{1})
	if c.ReleaseBytes("large") != 1000 || c.Has("one-off", 0) {
		t.Fatal("large replay lost to sampling bias")
	}
	checkInvariants(t, c)
}
