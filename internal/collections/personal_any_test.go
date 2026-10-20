package collections_test

import (
	"database/sql"
	"testing"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

// With match "any", a per-viewer rule is one alternative among the others:
// "Horror OR not watched" keeps unwatched non-horror titles and watched
// horror titles, and drops only watched titles that are not horror.
func TestMatchAnyWithPersonalRuleIsAnOr(t *testing.T) {
	st := &fakeState{watched: map[int64]map[core.RatingKey]bool{1: {core.MovieKey(102): true, core.MovieKey(121): true}}}
	e := newEnv(t, withOpts(collections.WithState(st), collections.WithAuth(fakeAuth{})))
	sp := spec("Horror or unwatched", "movie", catalogSource(), rl("genre", "is", "Horror"), rl("watched", "is", false))
	sp.Match = "any"
	created, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	key := core.SmartKey(mustInt(t, created.ID))
	check := func(label string, p core.Page) {
		t.Helper()
		got := map[core.RatingKey]bool{}
		for _, it := range p.Items {
			got[it.Key] = true
		}
		if got[core.MovieKey(102)] {
			t.Fatalf("%s: watched non-horror title kept", label)
		}
		if !got[core.MovieKey(121)] || !got[core.MovieKey(128)] {
			t.Fatalf("%s: horror titles missing: %v", label, p.Items)
		}
		if !got[core.MovieKey(105)] || !got[core.MovieKey(101)] {
			t.Fatalf("%s: unwatched non-horror titles missing: %v", label, p.Items)
		}
	}
	page, err := e.svc.Items(e.ctx, owner, key, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	check("built", page)
	// A guest has watched nothing: everything matches.
	guestPage, _ := e.svc.Items(e.ctx, guest, key, 0, 200)
	if guestPage.Total <= page.Total {
		t.Fatalf("guest total %d, owner total %d", guestPage.Total, page.Total)
	}
	// After a restart the members come from the database.
	e2, err := collections.New(e.db.(*sql.DB), e.cat, nil, collections.WithIndexTTL(0), collections.WithoutDefaults(), collections.WithState(st))
	if err != nil {
		t.Fatal(err)
	}
	page2, err := e2.Items(e.ctx, owner, key, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	check("reloaded", page2)
}
