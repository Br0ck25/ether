package usenet

import (
	"container/list"
	"slices"
	"sync"
	"time"
)

// missCache briefly remembers which providers answered 430 for an article,
// so retries of a missing segment (the stream engine retrying, a health
// check followed by playback) do not ask the same servers again.
type missCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	now   func() time.Time         // injected clock, as in breaker (tests)
	items map[string]*list.Element // message-id -> *missEntry
	order list.List                // oldest first
}

type missEntry struct {
	id        string
	providers []string // pool keys that said 430
	expires   time.Time
}

func newMissCache(ttl time.Duration, max int, now func() time.Time) *missCache {
	if now == nil {
		now = time.Now
	}
	return &missCache{ttl: ttl, max: max, now: now, items: make(map[string]*list.Element)}
}

// get returns the pool keys known to be missing id.
func (m *missCache) get(id string) []string {
	if m.ttl <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[id]
	if !ok {
		return nil
	}
	e := el.Value.(*missEntry)
	if m.now().After(e.expires) {
		m.order.Remove(el)
		delete(m.items, id)
		return nil
	}
	return slices.Clone(e.providers)
}

// add records that pool key has no article id.
func (m *missCache) add(id, key string) {
	if m.ttl <= 0 {
		return
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[id]; ok {
		e := el.Value.(*missEntry)
		if now.After(e.expires) {
			e.providers = e.providers[:0]
			e.expires = now.Add(m.ttl)
			m.order.MoveToBack(el)
		}
		if !slices.Contains(e.providers, key) {
			e.providers = append(e.providers, key)
		}
		return
	}
	m.items[id] = m.order.PushBack(&missEntry{id: id, providers: []string{key}, expires: now.Add(m.ttl)})
	// Drop expired entries from the front, then enforce the size bound.
	for m.order.Len() > 0 {
		front := m.order.Front()
		e := front.Value.(*missEntry)
		if m.order.Len() <= m.max && now.Before(e.expires) {
			break
		}
		m.order.Remove(front)
		delete(m.items, e.id)
	}
}

func (m *missCache) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.order.Len()
}
