// Package lru provides a least-recently-used cache bounded by entry count
// and total cost.
package lru

import (
	"container/list"
	"iter"
)

// Options bound a Cache. A zero bound is unlimited.
type Options[K comparable, V any] struct {
	MaxEntries int
	MaxCost    int64
	// Cost weighs an entry against MaxCost; nil weighs every entry 0.
	Cost func(K, V) int64
	// OnEvict is called for each entry removed to satisfy a bound, never
	// for Remove, RemoveFunc, Clear or a replaced value.
	OnEvict func(K, V)
}

// Cache is an LRU map. It is not safe for concurrent use; callers guard it
// with the lock that protects their related state.
type Cache[K comparable, V any] struct {
	o     Options[K, V]
	ll    list.List // front = most recently used
	items map[K]*list.Element
	cost  int64
}

type entry[K comparable, V any] struct {
	key  K
	val  V
	cost int64
}

// New returns an empty cache.
func New[K comparable, V any](o Options[K, V]) *Cache[K, V] {
	return &Cache[K, V]{o: o, items: map[K]*list.Element{}}
}

// Get returns key's value and marks it most recently used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry[K, V]).val, true
}

// Peek returns key's value without changing its recency.
func (c *Cache[K, V]) Peek(key K) (V, bool) {
	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	return el.Value.(*entry[K, V]).val, true
}

// Contains reports whether key is cached, without changing its recency.
func (c *Cache[K, V]) Contains(key K) bool {
	_, ok := c.items[key]
	return ok
}

// Add stores val as the most recently used entry for key, then evicts
// least-recently-used entries until both bounds hold. A value whose cost
// alone exceeds MaxCost is not stored, and any previous value is removed;
// Add reports whether val was stored.
func (c *Cache[K, V]) Add(key K, val V) bool {
	var cost int64
	if c.o.Cost != nil {
		cost = c.o.Cost(key, val)
	}
	if el, ok := c.items[key]; ok {
		if c.o.MaxCost > 0 && cost > c.o.MaxCost {
			c.removeElement(el)
			return false
		}
		e := el.Value.(*entry[K, V])
		c.cost += cost - e.cost
		e.val, e.cost = val, cost
		c.ll.MoveToFront(el)
	} else {
		if c.o.MaxCost > 0 && cost > c.o.MaxCost {
			return false
		}
		c.items[key] = c.ll.PushFront(&entry[K, V]{key: key, val: val, cost: cost})
		c.cost += cost
	}
	for (c.o.MaxEntries > 0 && c.ll.Len() > c.o.MaxEntries) || (c.o.MaxCost > 0 && c.cost > c.o.MaxCost) {
		e := c.removeElement(c.ll.Back())
		if c.o.OnEvict != nil {
			c.o.OnEvict(e.key, e.val)
		}
	}
	return true
}

// Remove deletes key and reports whether it was cached.
func (c *Cache[K, V]) Remove(key K) bool {
	el, ok := c.items[key]
	if ok {
		c.removeElement(el)
	}
	return ok
}

// RemoveFunc deletes every entry for which drop returns true, visiting from
// least to most recently used, and returns how many it deleted.
func (c *Cache[K, V]) RemoveFunc(drop func(K, V) bool) int {
	n := 0
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		if e := el.Value.(*entry[K, V]); drop(e.key, e.val) {
			c.removeElement(el)
			n++
		}
		el = prev
	}
	return n
}

// All yields the entries from most to least recently used without changing
// their recency. The cache must not be modified during iteration.
func (c *Cache[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for el := c.ll.Front(); el != nil; el = el.Next() {
			if e := el.Value.(*entry[K, V]); !yield(e.key, e.val) {
				return
			}
		}
	}
}

// Oldest returns the least recently used entry.
func (c *Cache[K, V]) Oldest() (K, V, bool) {
	el := c.ll.Back()
	if el == nil {
		var k K
		var v V
		return k, v, false
	}
	e := el.Value.(*entry[K, V])
	return e.key, e.val, true
}

// Clear deletes every entry.
func (c *Cache[K, V]) Clear() {
	c.ll.Init()
	clear(c.items)
	c.cost = 0
}

// Len returns the number of entries.
func (c *Cache[K, V]) Len() int { return c.ll.Len() }

// Cost returns the total cost of the entries.
func (c *Cache[K, V]) Cost() int64 { return c.cost }

func (c *Cache[K, V]) removeElement(el *list.Element) *entry[K, V] {
	e := c.ll.Remove(el).(*entry[K, V])
	delete(c.items, e.key)
	c.cost -= e.cost
	return e
}
