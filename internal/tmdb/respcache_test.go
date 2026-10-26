package tmdb

import (
	"fmt"
	"testing"
	"time"
)

func TestRespCacheEvictsLeastRecentlyUsedByCount(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newRespCache(3, 1<<20)
	for _, k := range []string{"a", "b", "c"} {
		c.put(k, cacheEntry{body: []byte(k), expires: now.Add(time.Minute)}, now)
	}
	c.get("a") // a is now most recent; b is the LRU
	c.put("d", cacheEntry{body: []byte("d"), expires: now.Add(time.Minute)}, now)
	if c.len() != 3 {
		t.Fatalf("len = %d, want 3", c.len())
	}
	if _, ok := c.get("b"); ok {
		t.Fatal("LRU entry b survived")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.get(k); !ok {
			t.Fatalf("%s evicted", k)
		}
	}
}

func TestRespCacheEnforcesByteBudget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := make([]byte, 1000)
	per := itemSize("k00", cacheEntry{body: body})
	c := newRespCache(1000, 5*per)
	for i := 0; i < 20; i++ {
		c.put(fmt.Sprintf("k%02d", i), cacheEntry{body: body}, now)
	}
	if c.len() != 5 || c.lru.Cost() != 5*per {
		t.Fatalf("len=%d bytes=%d, want 5 and %d", c.len(), c.lru.Cost(), 5*per)
	}
	// Replacing an entry must not double count it.
	c.put("k19", cacheEntry{body: body}, now)
	if c.lru.Cost() != 5*per {
		t.Fatalf("bytes after replace = %d", c.lru.Cost())
	}
	// An entry larger than the budget is not cached.
	c.put("huge", cacheEntry{body: make([]byte, 6*per)}, now)
	if _, ok := c.get("huge"); ok || c.len() != 5 {
		t.Fatal("oversized entry cached")
	}
}

func TestRespCacheSweepsLongExpiredEntries(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newRespCache(100, 1<<20)
	c.put("ancient", cacheEntry{expires: now.Add(-staleRetention - time.Hour)}, now)
	c.put("stale", cacheEntry{etag: `"x"`, expires: now.Add(-time.Hour)}, now)
	// Within the sweep interval nothing is swept.
	c.put("fresh", cacheEntry{expires: now.Add(time.Hour)}, now.Add(time.Minute))
	if c.len() != 3 {
		t.Fatalf("len = %d, want 3", c.len())
	}
	c.put("fresh2", cacheEntry{expires: now.Add(time.Hour)}, now.Add(sweepInterval+time.Minute))
	if _, ok := c.get("ancient"); ok {
		t.Fatal("entry expired beyond stale retention survived the sweep")
	}
	if _, ok := c.get("stale"); !ok {
		t.Fatal("recently expired entry must be kept for ETag revalidation")
	}
}
