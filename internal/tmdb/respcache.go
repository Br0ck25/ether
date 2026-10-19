package tmdb

import (
	"container/list"
	"time"
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
	maxEntries int
	maxBytes   int64
	bytes      int64
	ll         *list.List // front = most recently used
	items      map[string]*list.Element
	lastSweep  time.Time
}

type respItem struct {
	key   string
	entry cacheEntry
}

func newRespCache(maxEntries int, maxBytes int64) *respCache {
	if maxEntries <= 0 {
		maxEntries = defaultCacheMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	return &respCache{maxEntries: maxEntries, maxBytes: maxBytes, ll: list.New(), items: map[string]*list.Element{}}
}

func itemSize(key string, e cacheEntry) int64 {
	return int64(len(key) + len(e.body) + len(e.etag) + entryOverhead)
}

// get returns the entry (fresh or stale) and marks it recently used.
func (c *respCache) get(key string) (cacheEntry, bool) {
	el, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*respItem).entry, true
}

// put inserts or replaces an entry, then evicts least-recently-used entries
// until both bounds hold. An entry larger than the whole byte budget is not
// cached at all.
func (c *respCache) put(key string, e cacheEntry, now time.Time) {
	if now.Sub(c.lastSweep) >= sweepInterval {
		c.sweep(now)
	}
	size := itemSize(key, e)
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
	if size > c.maxBytes {
		return
	}
	c.items[key] = c.ll.PushFront(&respItem{key: key, entry: e})
	c.bytes += size
	for c.ll.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.remove(c.ll.Back())
	}
}

// sweep drops entries that expired more than staleRetention ago.
func (c *respCache) sweep(now time.Time) {
	c.lastSweep = now
	cutoff := now.Add(-staleRetention)
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		if el.Value.(*respItem).entry.expires.Before(cutoff) {
			c.remove(el)
		}
		el = prev
	}
}

func (c *respCache) remove(el *list.Element) {
	it := c.ll.Remove(el).(*respItem)
	delete(c.items, it.key)
	c.bytes -= itemSize(it.key, it.entry)
}

func (c *respCache) len() int { return c.ll.Len() }
