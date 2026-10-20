package usenet

import (
	"cmp"
	"slices"
)

// MaxShareWeight bounds a node's weight in Shares.
const MaxShareWeight = 100

// Shares divides capacity (an account-wide connection limit) among live
// cluster nodes in proportion to their weights (0 or less counts as 1):
// node i gets floor(capacity*w_i/Σw), raised to at least 1. When raising
// pushes the total over capacity, the largest shares give one back each
// (ties: the higher node ID first) until the total fits, so the cluster
// never opens more than capacity connections while capacity ≥ nodes.
//
// When capacity is smaller than the number of nodes every node still gets
// one connection: a node without connections could not stream at all, and
// a provider that refuses the extra logins is handled by the pool's
// server-limit adaptation. The result is deterministic for a membership
// list, so every node computes the same division independently.
func Shares(capacity int, weights map[string]int) map[string]int {
	if len(weights) == 0 || capacity <= 0 {
		return map[string]int{}
	}
	ids := make([]string, 0, len(weights))
	total := 0
	w := make(map[string]int, len(weights))
	for id, v := range weights {
		v = min(max(v, 1), MaxShareWeight)
		w[id] = v
		total += v
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make(map[string]int, len(ids))
	if capacity < len(ids) {
		for _, id := range ids {
			out[id] = 1
		}
		return out
	}
	sum := 0
	for _, id := range ids {
		out[id] = max(capacity*w[id]/total, 1)
		sum += out[id]
	}
	for sum > capacity {
		// Take one back from the largest share (capacity ≥ nodes, so the
		// largest is always above 1 here).
		big := slices.MaxFunc(ids, func(a, b string) int {
			if c := cmp.Compare(out[a], out[b]); c != 0 {
				return c
			}
			return cmp.Compare(a, b)
		})
		out[big]--
		sum--
	}
	return out
}

// ShareOf is self's part of capacity under Shares; capacity itself when
// self is the only node or not a member.
func ShareOf(capacity int, self string, weights map[string]int) int {
	if _, ok := weights[self]; !ok || len(weights) <= 1 {
		return capacity
	}
	return Shares(capacity, weights)[self]
}
