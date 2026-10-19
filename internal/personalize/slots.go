package personalize

import "sort"

// The page-structure solver: given the rows chosen for a page in ranked
// order, with anchored slots and position bounds, it assigns every row a
// slot.
//
//  1. Anchored rows take their slot. Two anchors on one slot (a viewer's
//     override against a Global anchor) go to the stronger layer; the
//     other moves to the nearest free slot. An anchor beyond the end of
//     the page sits at the end.
//  2. The other rows fill the free slots in rank order, except where
//     that would break a bound: a row is never placed above its MinSlot
//     ("never above") and, when possible, never below its MaxSlot ("never
//     below"). Each free slot takes the best-ranked row that may sit
//     there and leaves the remaining rows a feasible arrangement (checked
//     by earliest-deadline-first, which is exact for unit-length jobs
//     with release times and deadlines). When no arrangement honours
//     every bound, the most urgent row goes first and the miss is
//     reported.

// slotReq is one row's placement constraints. Slots are 1-based; 0 means
// unset.
type slotReq struct {
	anchor int
	lo, hi int // never above lo, never below hi
	pri    int // anchor conflicts: higher wins (override 2, Global 1)
}

// slotOutcome is how a row's constraint fared.
type slotOutcome struct {
	slot      int
	displaced bool // anchored, but another anchor holds its slot
	clamped   bool // anchored beyond the end of the page
	missedLo  bool // placed above its MinSlot
	missedHi  bool // placed below its MaxSlot
}

// placeSlots returns, for rows in rank order, each row's slot.
func placeSlots(reqs []slotReq) []slotOutcome {
	n := len(reqs)
	out := make([]slotOutcome, n)
	taken := make([]bool, n+2)
	// 1. Anchors: exact slots first, stronger layer first on a tie, then
	// the displaced ones to the nearest free slot.
	var anchors []int
	for i, r := range reqs {
		if r.anchor > 0 {
			anchors = append(anchors, i)
		}
	}
	sort.SliceStable(anchors, func(a, b int) bool {
		ra, rb := reqs[anchors[a]], reqs[anchors[b]]
		if ra.anchor != rb.anchor {
			return ra.anchor < rb.anchor
		}
		return ra.pri > rb.pri
	})
	var displaced []int
	for _, i := range anchors {
		t := reqs[i].anchor
		if t > n {
			t = n
			out[i].clamped = true
		}
		if taken[t] {
			displaced = append(displaced, i)
			continue
		}
		taken[t] = true
		out[i].slot = t
	}
	for _, i := range displaced {
		t := min(reqs[i].anchor, n)
		s := nearestFree(taken, t, n)
		taken[s] = true
		out[i].slot = s
		out[i].displaced = !out[i].clamped || s != n
	}
	// 2. Everything else in rank order around the anchors.
	var free []int
	for s := 1; s <= n; s++ {
		if !taken[s] {
			free = append(free, s)
		}
	}
	var rest []int
	bounded := false
	for i, r := range reqs {
		if r.anchor == 0 {
			rest = append(rest, i)
			if r.lo > 0 || r.hi > 0 {
				bounded = true
			}
		}
	}
	if !bounded {
		for k, i := range rest {
			out[i].slot = free[k]
		}
		return out
	}
	lo := func(i int) int { return max(1, reqs[i].lo) }
	hi := func(i int) int {
		if reqs[i].hi > 0 {
			return reqs[i].hi
		}
		return n + 1
	}
	used := make([]bool, n)
	for k, s := range free {
		pick := -1
		var avail []int
		for _, i := range rest {
			if !used[i] && lo(i) <= s {
				avail = append(avail, i)
			}
		}
		if len(avail) == 0 {
			// Every remaining row wants to sit lower: the earliest release
			// goes here (a MinSlot miss).
			for _, i := range rest {
				if !used[i] && (pick < 0 || lo(i) < lo(pick)) {
					pick = i
				}
			}
		} else {
			for _, i := range avail {
				used[i] = true
				ok := edfFeasible(rest, used, free[k+1:], lo, hi)
				used[i] = false
				if ok {
					pick = i
					break
				}
			}
			if pick < 0 { // nothing keeps every bound: most urgent first
				for _, i := range avail {
					if pick < 0 || hi(i) < hi(pick) {
						pick = i
					}
				}
			}
		}
		used[pick] = true
		out[pick].slot = s
		out[pick].missedLo = s < lo(pick)
		out[pick].missedHi = s > hi(pick)
	}
	return out
}

// nearestFree is the free slot closest to t, preferring lower on the
// page (later) on a tie, as a displaced anchor yields to the one above.
func nearestFree(taken []bool, t, n int) int {
	for d := 1; d <= n; d++ {
		if s := t + d; s <= n && !taken[s] {
			return s
		}
		if s := t - d; s >= 1 && !taken[s] {
			return s
		}
	}
	return t
}

// edfFeasible reports whether the unused rows can fill slots (ascending)
// with every row at or below its release (lo) and at or above its
// deadline (hi).
func edfFeasible(rows []int, used []bool, slots []int, lo, hi func(int) int) bool {
	done := make(map[int]bool, len(slots))
	for _, s := range slots {
		best := -1
		for _, i := range rows {
			if used[i] || done[i] || lo(i) > s {
				continue
			}
			if best < 0 || hi(i) < hi(best) {
				best = i
			}
		}
		if best < 0 || hi(best) < s {
			return false
		}
		done[best] = true
	}
	return true
}
