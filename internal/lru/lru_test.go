package lru

import (
	"slices"
	"testing"
)

func TestEntryBoundEvictsLeastRecentlyUsed(t *testing.T) {
	var evicted []string
	c := New(Options[string, int]{MaxEntries: 2, OnEvict: func(k string, _ int) { evicted = append(evicted, k) }})
	c.Add("a", 1)
	c.Add("b", 2)
	c.Get("a")
	c.Add("c", 3)
	if c.Contains("b") || !c.Contains("a") || !c.Contains("c") || c.Len() != 2 {
		t.Fatalf("len=%d evicted=%v", c.Len(), evicted)
	}
	if !slices.Equal(evicted, []string{"b"}) {
		t.Fatalf("evicted=%v", evicted)
	}
	c.Peek("a") // does not promote
	c.Add("d", 4)
	if c.Contains("a") {
		t.Fatal("Peek promoted the entry")
	}
}

func TestCostBound(t *testing.T) {
	c := New(Options[string, []byte]{MaxCost: 10, Cost: func(_ string, v []byte) int64 { return int64(len(v)) }})
	c.Add("a", make([]byte, 4))
	c.Add("b", make([]byte, 4))
	c.Add("a", make([]byte, 6)) // replace: cost 10, still fits
	if c.Cost() != 10 || c.Len() != 2 {
		t.Fatalf("cost=%d len=%d", c.Cost(), c.Len())
	}
	c.Add("c", make([]byte, 1))
	if c.Contains("b") || c.Cost() != 7 {
		t.Fatalf("cost=%d", c.Cost())
	}
	if c.Add("huge", make([]byte, 11)) || c.Contains("huge") {
		t.Fatal("oversize value stored")
	}
	if c.Add("a", make([]byte, 11)) || c.Contains("a") || c.Cost() != 1 {
		t.Fatalf("oversize replacement kept the old value: cost=%d", c.Cost())
	}
}

func TestRemoveFuncAndClear(t *testing.T) {
	c := New(Options[int, int]{Cost: func(_, v int) int64 { return int64(v) }})
	for i := range 6 {
		c.Add(i, i)
	}
	var order []int
	if n := c.RemoveFunc(func(k, _ int) bool { order = append(order, k); return k%2 == 0 }); n != 3 {
		t.Fatalf("removed %d", n)
	}
	if !slices.Equal(order, []int{0, 1, 2, 3, 4, 5}) || c.Cost() != 9 {
		t.Fatalf("order=%v cost=%d", order, c.Cost())
	}
	var keys []int
	for k := range c.All() {
		keys = append(keys, k)
	}
	if !slices.Equal(keys, []int{5, 3, 1}) {
		t.Fatalf("All = %v", keys)
	}
	if k, _, ok := c.Oldest(); !ok || k != 1 {
		t.Fatalf("oldest=%d", k)
	}
	if !c.Remove(1) || c.Remove(1) {
		t.Fatal("Remove")
	}
	c.Clear()
	if c.Len() != 0 || c.Cost() != 0 {
		t.Fatal("Clear")
	}
}
