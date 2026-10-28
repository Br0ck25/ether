package tmdb

import (
	"time"

	"github.com/braedonsaunders/ether/internal/lru"
)

// Bounds for the in-memory response cache. Every distinct GET URL is
// cached and kept after expiry so its ETag can be revalidated cheaply, so
// without a bound a long-running server that browses and refreshes the
// catalogue accumulates every response it ever fetched.
const (
	defaultCacheMaxEntries = 20_000
	defaultCacheMaxBytes   = 256 << 20
	// staleRetention is how long past expiry an entry is kept for ETag
	// revalidation; older ones are swept even when the cache is not full.
	staleRetention = 7 * 24 * time.Hour
	sweepInterval  = 10 * time.Minute
	// entryOverhead approximates per-entry bookkeeping in the byte budget.
	entryOverhead = 128
)

// respCache is a size-bounded LRU of TMDB responses. It is not safe for
// concurrent use; Client.mu guards it.
type respCache struct {
	lru       *lru.Cache[string, cacheEntry]
	lastSweep time.Time
}

func newRespCache(maxEntries int, maxBytes int64) *respCache {
	if maxEntries <= 0 {
		maxEntries = defaultCacheMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	return &respCache{lru: lru.New(lru.Options[string, cacheEntry]{MaxEntries: maxEntries, MaxCost: maxBytes, Cost: itemSize})}
}

func itemSize(key string, e cacheEntry) int64 {
	return int64(len(key) + len(e.body) + len(e.etag) + entryOverhead)
}

// get returns the entry (fresh or stale) and marks it recently used.
func (c *respCache) get(key string) (cacheEntry, bool) { return c.lru.Get(key) }

// put inserts or replaces an entry, then evicts least-recently-used entries
// until both bounds hold. An entry larger than the whole byte budget is not
// cached at all.
func (c *respCache) put(key string, e cacheEntry, now time.Time) {
	if now.Sub(c.lastSweep) >= sweepInterval {
		c.sweep(now)
	}
	c.lru.Add(key, e)
}

// sweep drops entries that expired more than staleRetention ago.
func (c *respCache) sweep(now time.Time) {
	c.lastSweep = now
	cutoff := now.Add(-staleRetention)
	c.lru.RemoveFunc(func(_ string, e cacheEntry) bool { return e.expires.Before(cutoff) })
}

func (c *respCache) len() int { return c.lru.Len() }
