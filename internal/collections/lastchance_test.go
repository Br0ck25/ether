package collections_test

import (
	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
	"time"
)

func TestLastChanceMatchesActualNextRotationAndViewerTargets(t *testing.T) {
	for _, every := range []string{"daily", "weekly"} {
		t.Run(every, func(t *testing.T) {
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			e := newEnv(t, withOpts(collections.WithNow(func() time.Time { return now })))
			sp := spec("Rotating picks", "movie", catalogSource(), rl("votes", "gte", 1000))
			sp.Rotate = &core.CollectionRotate{Size: 5, Every: every}
			sp.Targets.Users = core.UserTargets{IDs: []int64{owner.ID}}
			created, err := e.svc.CreateSpec(e.ctx, sp)
			if err != nil {
				t.Fatal(err)
			}
			key := core.SmartKey(mustInt(t, created.ID))
			current, err := e.svc.Items(e.ctx, owner, key, 0, 100)
			if err != nil || len(current.Items) != 5 {
				t.Fatalf("current %v %v", current, err)
			}
			before := now
			boundary := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
			if every == "weekly" {
				boundary = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			}
			deadlines := map[core.RatingKey]time.Time{}
			for _, title := range current.Items {
				at, err := e.svc.LastChance(e.ctx, owner, title.Key)
				if err != nil {
					t.Fatal(err)
				}
				deadlines[title.Key] = at
				other, err := e.svc.LastChance(e.ctx, guest, title.Key)
				if err != nil || !other.IsZero() {
					t.Fatal("targeted deadline leaked to another viewer")
				}
			}
			now = boundary
			next, err := e.svc.Items(e.ctx, owner, key, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			nextKeys := map[core.RatingKey]bool{}
			for _, title := range next.Items {
				nextKeys[title.Key] = true
			}
			leaving := 0
			for title, at := range deadlines {
				if nextKeys[title] {
					if !at.IsZero() {
						t.Fatal("title remains next rotation but advertised leaving")
					}
				} else {
					leaving++
					if !at.Equal(boundary) {
						t.Fatalf("deadline %s expected %s (at %s)", at, boundary, before)
					}
					after, err := e.svc.LastChance(e.ctx, owner, title)
					if err != nil || !after.IsZero() {
						t.Fatal("departed title still has last-chance badge")
					}
				}
			}
			if leaving == 0 {
				t.Fatal("fixture had no actual transition")
			}
		})
	}
}
func TestLastChanceNeverInventsExpiryForStableOrHiddenCollection(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, withOpts(collections.WithNow(func() time.Time { return now })))
	sp := spec("Stable picks", "movie", catalogSource(), rl("votes", "gte", 1000))
	sp.Rotate = &core.CollectionRotate{Size: 500, Every: "daily"}
	created, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	key := core.SmartKey(mustInt(t, created.ID))
	page, err := e.svc.Items(e.ctx, owner, key, 0, 50)
	if err != nil || len(page.Items) == 0 {
		t.Fatal("empty fixture")
	}
	for _, title := range page.Items {
		at, err := e.svc.LastChance(e.ctx, owner, title.Key)
		if err != nil || !at.IsZero() {
			t.Fatal("stable collection got invented deadline")
		}
	}
	sp.Rotate.Size = 3
	sp.Enabled = false
	if _, err = e.svc.UpdateSpec(e.ctx, created.ID, sp); err != nil {
		t.Fatal(err)
	}
	at, err := e.svc.LastChance(e.ctx, owner, page.Items[0].Key)
	if err != nil || !at.IsZero() {
		t.Fatal("disabled collection advertised deadline")
	}
}
