package personalize

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Cluster nodes share one database. Each node's engine must log its serves
// without colliding on serve ids, and a repeat on one node must touch that
// node's serve, not another's.
func TestServeLogTwoEnginesShareDatabase(t *testing.T) {
	h := newHarness(t, nil)
	other, err := New(Options{
		DB: h.db, Catalog: h.cat, State: h.st, Users: h.users,
		Logger: quietLogger(), Now: h.clock.Now, Location: time.UTC,
		RefreshEvery: -1, RebuildDebounce: time.Hour, FlushEvery: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)

	x, y := movieKey(1, 3), movieKey(1, 4)
	logServeAt(h, 1, testNow, "home.top10", "home", x)
	h.flushServes()
	otherH := &harness{t: t, db: h.db, e: other}
	logServeAt(otherH, 2, testNow, "home.trending", "home", y)
	// A repeat of the same page on the second node.
	logServeAt(otherH, 2, testNow, "home.trending", "home", y)
	other.rk.writer.flush(context.Background())

	if n := h.count(`SELECT COUNT(*) FROM personalize_serves`); n != 2 {
		t.Fatalf("serves = %d, want 2 (one per node)", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM personalize_serve_items`); n != 2 {
		t.Fatalf("serve items = %d, want 2", n)
	}
	if n := h.count(`SELECT repeats FROM personalize_serves WHERE user_id = 1`); n != 1 {
		t.Errorf("first node's serve repeats = %d, want 1", n)
	}
	if n := h.count(`SELECT repeats FROM personalize_serves WHERE user_id = 2`); n != 2 {
		t.Errorf("second node's serve repeats = %d, want 2", n)
	}
}

// Duplicate positions within one serve are ignored rather than failing
// the batch (portable ON CONFLICT DO NOTHING), and a touch moves last_at
// forward only.
func TestWriteServesIgnoresDuplicatePositionsAndTouchesForward(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	k := movieKey(1, 3)
	rec := &serveRec{id: 7, userID: 1, at: testNow, surface: "home", hc: hubContext(testNow, core.DeviceTV),
		items: []serveItem{
			{key: k, hubID: "home.top10", kind: KindTop10, rowPos: 1, itemPos: 0},
			{key: k, hubID: "home.top10", kind: KindTop10, rowPos: 1, itemPos: 0},
		}}
	ids := newServeIDs()
	ops := []serveOp{
		{touch: 7, at: testNow.Add(2 * time.Minute)}, // queued just ahead of its serve
		{rec: rec},
		{touch: 7, at: testNow.Add(5 * time.Minute)},
		{touch: 7, at: testNow.Add(time.Minute)}, // late, out of order
		{touch: 99, at: testNow},                 // unknown handle: skipped
	}
	if err := writeServes(ctx, h.db, ops, ids); err != nil {
		t.Fatal(err)
	}
	if _, ok := ids.m[7]; !ok {
		t.Fatal("serve handle not mapped after commit")
	}
	if n := h.count(`SELECT COUNT(*) FROM personalize_serve_items`); n != 1 {
		t.Fatalf("serve items = %d, want 1", n)
	}
	if n := h.count(`SELECT repeats FROM personalize_serves`); n != 4 {
		t.Fatalf("repeats = %d, want 4", n)
	}
	var last int64
	if err := h.db.QueryRow(`SELECT last_at FROM personalize_serves`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if want := ms(testNow.Add(5 * time.Minute)); last != want {
		t.Fatalf("last_at = %d, want %d", last, want)
	}
}

// A full handle map forgets the least recently used handles, never a serve
// that is still being repeated: its touches keep extending last_at, which
// decides whether a later play is attributed to it.
func TestServeIDsEvictLeastRecentlyUsed(t *testing.T) {
	ids := newServeIDs()
	// Handle 1: a screen left on Home, re-served throughout.
	ids.record(map[int64]int64{1: 101}, nil)
	for h := int64(2); h <= maxServeIDs+10; h++ {
		var touched []int64
		if h%100 == 0 {
			touched = []int64{1}
		}
		ids.record(map[int64]int64{h: 100 + h}, touched)
	}
	if len(ids.m) > maxServeIDs {
		t.Fatalf("handles = %d, want at most %d", len(ids.m), maxServeIDs)
	}
	if e, ok := ids.m[1]; !ok || e.db != 101 {
		t.Fatalf("live serve's handle was evicted: %+v %v", e, ok)
	}
	if _, ok := ids.m[maxServeIDs+10]; !ok {
		t.Fatal("newest handle was evicted")
	}
	if _, ok := ids.m[2]; ok {
		t.Fatal("stale handle kept")
	}
}
