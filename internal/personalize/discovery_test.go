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
