package personalize

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
	"time"
)

func TestDiscoveryStickyShowDismissalAndViewerIsolation(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cat := newFakeCatalog(testNow)
	show := core.ShowKey(99)
	a := core.Title{Key: core.EpisodeKey(99, 1, 1), Type: core.TypeEpisode, ShowKey: show, ContentRating: "PG"}
	b := a
	b.Key = core.EpisodeKey(99, 1, 2)
	cat.set(a)
	cat.set(b)
	d := NewDiscovery(db, cat, nil, nil)
	if e = d.RemoveContinue(ctx, 1, a.Key); e != nil {
		t.Fatal(e)
	}
	if d.ContinueAllowed(ctx, 1, &b) {
		t.Fatal("new episode bypassed sticky show dismissal")
	}
	if !d.ContinueAllowed(ctx, 2, &b) {
		t.Fatal("dismissal leaked across viewers")
	}
	if e = d.RestoreContinue(ctx, 1, a.Key); e != nil {
		t.Fatal(e)
	}
	filtered := d.FilterContinue(ctx, &core.User{ID: 1}, []core.Title{a, b})
	if len(filtered) != 1 {
		t.Fatalf("show duplicated %v", filtered)
	}
}
func TestDiscoveryReviewRespectsYearCompletionAndSafety(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cat := newFakeCatalog(testNow)
	st := state.New(db, state.Options{Now: func() time.Time { return time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC) }})
	key := movieKey(0, 1)
	if e = st.Scrobble(ctx, 1, key); e != nil {
		t.Fatal(e)
	}
	d := NewDiscovery(db, cat, st, nil)
	d.now = func() time.Time { return testNow }
	h, e := d.YearInReview(ctx, &core.User{ID: 1}, 2025, 20)
	if e != nil || len(h.Items) != 1 {
		t.Fatalf("review %v %v", h, e)
	}
	other, e := d.YearInReview(ctx, &core.User{ID: 2}, 2025, 20)
	if e != nil || len(other.Items) != 0 {
		t.Fatal("review leaked viewer history")
	}
	current, e := d.YearInReview(ctx, &core.User{ID: 1}, 2026, 20)
	if e != nil || len(current.Items) != 0 {
		t.Fatal("review included another year")
	}
	cat.set(core.Title{Key: key, Type: core.TypeMovie, Adult: true})
	h, e = d.YearInReview(ctx, &core.User{ID: 1}, 2025, 20)
	if e != nil || len(h.Items) != 0 {
		t.Fatal("adult title leaked into review")
	}
}

func TestDiscoveryVirtualReviewCollectionIsViewerScoped(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cat := newFakeCatalog(testNow)
	st := state.New(db, state.Options{Now: func() time.Time { return time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC) }})
	if e = st.Scrobble(ctx, 1, movieKey(0, 1)); e != nil {
		t.Fatal(e)
	}
	d := NewDiscovery(db, cat, st, nil)
	d.now = func() time.Time { return testNow }
	collections := d.ReviewCollections(nil)
	list, e := collections.List(ctx, &core.User{ID: 1}, core.TypeMovie)
	if e != nil || len(list) != 1 || list[0].ChildCount != 1 || list[0].OwnerUserID != 1 {
		t.Fatalf("collections %v %v", list, e)
	}
	key := list[0].Key
	year, typ, ok := reviewCollectionParts(key)
	if !ok || year != 2025 || typ != core.TypeMovie {
		t.Fatal("unstable review identity")
	}
	page, e := collections.Items(ctx, &core.User{ID: 2}, key, 0, 20)
	if e != nil || page.Total != 0 {
		t.Fatalf("collection leaked viewer history %v %v", page, e)
	}
	if _, e = collections.Get(ctx, nil, key); e != core.ErrUnauthorized {
		t.Fatal("anonymous review allowed")
	}
	if e = collections.DeleteSmart(ctx, key); e != core.ErrNotFound {
		t.Fatal("virtual collection deletion accepted")
	}
}

func TestYearInReviewSkipsAlreadyPresentedShowBeforeLookup(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat := newFakeCatalog(testNow)
	show := core.ShowKey(991)
	cat.set(core.Title{Key: show, Type: core.TypeShow, Title: "Long running show"})
	at := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	st := state.New(db, state.Options{Now: func() time.Time { return at }})
	for i := 1; i <= 100; i++ {
		key := core.EpisodeKey(991, 1, i)
		cat.set(core.Title{Key: key, Type: core.TypeEpisode, ShowKey: show})
		if err := st.Scrobble(ctx, 1, key); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDiscovery(db, cat, st, nil)
	d.now = func() time.Time { return testNow }
	before := cat.callCount("get")
	row, err := d.YearInReview(ctx, &core.User{ID: 1}, 2025, 25)
	if err != nil || len(row.Items) != 1 || row.Items[0].Key != show {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if calls := cat.callCount("get") - before; calls != 2 {
		t.Fatalf("got %d title loads for one show, want first episode + show only", calls)
	}
}

type batchedHistoryCatalog struct {
	*fakeCatalog
	batches int
	keys    int
}

func (c *batchedHistoryCatalog) CachedTitles(_ context.Context, keys []core.RatingKey) ([]core.Title, error) {
	c.batches++
	c.keys = len(keys)
	c.dataMu.RLock()
	defer c.dataMu.RUnlock()
	return c.resolve(keys), nil
}
func TestHistoryTitleBatchAvoidsPerTitleReads(t *testing.T) {
	ctx := context.Background()
	cat := &batchedHistoryCatalog{fakeCatalog: newFakeCatalog(testNow)}
	a, b := movieKey(0, 1), movieKey(0, 2)
	cache := mirroredTitles(ctx, cat, []core.RatingKey{a, b, a})
	for _, key := range []core.RatingKey{a, b} {
		title, err := historyTitle(ctx, cat, cache, key)
		if err != nil || title.Key != key {
			t.Fatalf("title=%+v err=%v", title, err)
		}
	}
	if cat.batches != 1 || cat.keys != 2 || cat.callCount("get") != 0 {
		t.Fatalf("batch=%d keys=%d gets=%d", cat.batches, cat.keys, cat.callCount("get"))
	}
	missing := core.MovieKey(999999)
	if _, err := historyTitle(ctx, cat, cache, missing); err != core.ErrNotFound || cat.callCount("get") != 1 {
		t.Fatal("missing mirror entry did not use ordinary lookup")
	}
}
