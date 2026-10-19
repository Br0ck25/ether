package collections_test

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// The Home Designer can place a collection on a page whatever its own
// Home/section targeting and schedule (core.CollectionRowLookup).
func TestCollectionRowsByIDForTheHomeDesigner(t *testing.T) {
	e := newEnv(t)
	var _ core.CollectionRowLookup = e.svc
	// Not promoted anywhere, scheduled for Fridays only, for the owner only.
	quiet := spec("Holiday Classics", "movie", catalogSource(), rl("genre", "is", "Comedy"))
	quiet.Targets.Users = core.UserTargets{IDs: []int64{1}}
	quiet.Targets.Schedule = &core.CollectionSchedule{Days: []int{5}}
	qc, err := e.svc.CreateSpec(e.ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	off := spec("Switched Off", "movie", catalogSource())
	off.Enabled = false
	oc, err := e.svc.CreateSpec(e.ctx, off)
	if err != nil {
		t.Fatal(err)
	}
	monday := core.HubContext{Device: "tv", Now: time.Date(2026, 10, 19, 21, 0, 0, 0, time.Local)}
	if rows, _ := e.svc.HomeRowsFor(e.ctx, owner, monday); len(rows) != 0 {
		t.Fatalf("an untargeted collection is not a Home row by itself: %+v", rows)
	}
	rows, err := e.svc.CollectionRowsByID(e.ctx, owner, monday, []string{qc.ID, oc.ID, "missing"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CollectionID != qc.ID || !rows[0].HomeRow || !rows[0].SectionRow || len(rows[0].Hub.Items) == 0 ||
		rows[0].Hub.ID != "collection."+qc.ID || rows[0].Hub.Title != "Holiday Classics" {
		t.Fatalf("placed rows = %+v", rows)
	}
	// The audience applies to Global placements, not to per-viewer ones.
	if rows, _ := e.svc.CollectionRowsByID(e.ctx, guest, monday, []string{qc.ID}, false); len(rows) != 0 {
		t.Fatalf("audience ignored: %+v", rows)
	}
	if rows, _ := e.svc.CollectionRowsByID(e.ctx, guest, monday, []string{qc.ID}, true); len(rows) != 1 {
		t.Fatalf("per-viewer placement refused: %+v", rows)
	}
	// The content-rating limit always applies to the titles.
	if rows, _ := e.svc.CollectionRowsByID(e.ctx, kid, monday, []string{qc.ID}, true); len(rows) == 1 {
		for _, it := range rows[0].Hub.Items {
			if it.ContentRating == "R" {
				t.Fatalf("R-rated title for a PG viewer: %s", it.Title)
			}
		}
	}
	if rows, err := e.svc.CollectionRowsByID(e.ctx, owner, monday, nil, false); err != nil || rows != nil {
		t.Fatalf("no ids = %+v %v", rows, err)
	}
}
