package availability

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type movingFeed struct {
	sources []core.ReleaseFeed
	items   []core.Release
	offsets []int
	failID  string
}

func (f *movingFeed) FeedSources() []core.ReleaseFeed { return f.sources }
func (f *movingFeed) Recent(ctx context.Context, id string, offset, limit int) ([]core.Release, error) {
	if id == f.failID {
		return nil, core.ErrBudgetExhausted
	}
	if err := core.SpendBackground(ctx, "hit", id, 1); err != nil {
		return nil, err
	}
	f.offsets = append(f.offsets, offset)
	if offset >= len(f.items) {
		return nil, nil
	}
	return append([]core.Release(nil), f.items[offset:min(len(f.items), offset+limit)]...), nil
}
func feedItems(start, n int) []core.Release {
	out := make([]core.Release, n)
	for i := range out {
		out[i] = core.Release{ID: fmt.Sprintf("r%d", start+i), Name: "Film", Indexer: "Fake"}
	}
	return out
}
func TestFeedCatchupSurvivesRestartAndNewArrivals(t *testing.T) {
	x := setup(t)
	x.c.Availability.Verify = false
	x.c.Availability.MaxPages = 2
	f := &movingFeed{sources: x.f.sources, items: feedItems(0, 650)}
	x.s.d.Feeds = f
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := x.s.checkpoint(t.Context(), f.sources[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.target == "" || c.offset != 199 || c.tail != "r199" {
		t.Fatalf("checkpoint %+v", c)
	}
	// Prepending ten releases shifts the next page. Resume at the exact saved
	// tail, not the stale numerical offset, and retain each new arrival.
	f.items = append(feedItems(1000, 10), f.items...)
	restarted, err := New(x.s.d)
	if err != nil {
		t.Fatal(err)
	}
	x.s = restarted
	for i := 0; i < 8; i++ {
		x.now = x.now.Add(15 * time.Minute)
		if err = x.s.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	c, err = x.s.checkpoint(t.Context(), f.sources[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.target != "" || c.head != "r1000" {
		t.Fatalf("catch-up incomplete %+v offsets %v", c, f.offsets)
	}
	var n int
	if err = x.s.db.QueryRow(`SELECT COUNT(*) FROM availability_feed_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 660 {
		t.Fatalf("lost feed items: %d offsets %v", n, f.offsets)
	}
	found := false
	for _, o := range f.offsets {
		if o == 209 {
			found = true
		}
	}
	if !found {
		t.Fatalf("did not adjust continuation for new head: %v", f.offsets)
	}
}
func TestFeedCatchupDoesNotSkipDeletedBoundary(t *testing.T) {
	x := setup(t)
	x.c.Availability.Verify = false
	x.c.Availability.MaxPages = 3
	f := &movingFeed{sources: x.f.sources, items: feedItems(0, 550)}
	x.s.d.Feeds = f
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Delete a saved boundary and insert an unseen release inside the older
	// pages. A failed boundary check must rescan, not declare coverage.
	f.items = append(f.items[:299], f.items[300:]...)
	f.items = append(f.items[:150], append(feedItems(2000, 1), f.items[150:]...)...)
	for i := 0; i < 8; i++ {
		x.now = x.now.Add(15 * time.Minute)
		if err := x.s.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := x.s.db.QueryRow(`SELECT COUNT(*) FROM availability_feed_items WHERE id='r2000'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("missed item after pagination changed")
	}
	c, _ := x.s.checkpoint(t.Context(), f.sources[0])
	if c.target != "" {
		t.Fatalf("did not recover %+v", c)
	}
}
func TestFeedCatchupReservesFreshPollsAndResumesNextDay(t *testing.T) {
	x := setup(t)
	x.c.Availability.Verify = false
	x.c.Availability.MaxPages = 5
	x.now = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	x.c.Availability.FeedRequests = 96 // One source, exactly one head every 15m.
	f := &movingFeed{sources: x.f.sources, items: feedItems(0, 500)}
	x.s.d.Feeds = f
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.offsets) != 1 {
		t.Fatalf("catch-up consumed head reserve: %v", f.offsets)
	}
	c, _ := x.s.checkpoint(t.Context(), f.sources[0])
	if c.tail != "r99" || c.target == "" {
		t.Fatalf("lost denied continuation %+v", c)
	}
	for i := 0; i < 3; i++ {
		x.now = x.now.Add(15 * time.Minute)
		if err := x.s.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.offsets) != 4 {
		t.Fatalf("fresh polling starved: %v", f.offsets)
	}
	x.now = x.now.Add(24 * time.Hour)
	x.c.Availability.FeedRequests = 400
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.offsets) <= 4 {
		t.Fatal("did not resume after budget reset")
	}
}
func TestPausedFeedDoesNotStarveNextIndexer(t *testing.T) {
	x := setup(t)
	x.c.Availability.Verify = false
	fs := append([]core.ReleaseFeed(nil), x.f.sources...)
	fs = append(fs, core.ReleaseFeed{ID: "second", Name: "Second", Generation: "one", Supported: true})
	f := &movingFeed{sources: fs, items: feedItems(0, 1), failID: "f"}
	x.s.d.Feeds = f
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.offsets) != 1 {
		t.Fatal("a paused source prevented another feed from polling")
	}
}
func TestFeedCatchupGenerationResetAndRecentHorizon(t *testing.T) {
	x := setup(t)
	x.c.Availability.Verify = false
	f := &movingFeed{sources: x.f.sources, items: feedItems(0, 400)}
	x.s.d.Feeds = f
	for i := range f.items {
		f.items[i].PubDate = x.now.Add(-62 * 24 * time.Hour)
	}
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.offsets) != 1 {
		t.Fatal("walked beyond recent horizon")
	}
	f.sources[0].Generation = "changed"
	c, err := x.s.checkpoint(t.Context(), f.sources[0])
	if err != nil || c.head != "" {
		t.Fatalf("reused different source checkpoint %+v %v", c, err)
	}
	if err := x.s.spend(t.Context(), "feedRequests", 400, 400); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatal("budget cap was not enforced")
	}
}

func TestLargeFreshBurstDoesNotDiscardOlderGap(t *testing.T) {
	x := setup(t)
	x.c.Availability.Verify = false
	x.c.Availability.MaxPages = 2
	f := &movingFeed{sources: x.f.sources, items: feedItems(0, 650)}
	x.s.d.Feeds = f
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := x.s.checkpoint(t.Context(), f.sources[0])
	f.items = append(feedItems(1000, 250), f.items...)
	x.now = x.now.Add(15 * time.Minute)
	if err := x.s.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, _ := x.s.checkpoint(t.Context(), f.sources[0])
	if before != after {
		t.Fatalf("lost gap while fresh burst exceeded page cap: %+v -> %+v", before, after)
	}
	x.c.Availability.MaxPages = 5
	for i := 0; i < 5; i++ {
		x.now = x.now.Add(15 * time.Minute)
		if err := x.s.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := x.s.db.QueryRow(`SELECT COUNT(*) FROM availability_feed_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 900 {
		t.Fatalf("lost observations: %d", n)
	}
	after, _ = x.s.checkpoint(t.Context(), f.sources[0])
	if after.target != "" {
		t.Fatalf("gap still pending %+v", after)
	}
}
