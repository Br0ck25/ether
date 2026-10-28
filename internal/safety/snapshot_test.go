package safety

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestPutStoresIndependentPolicySnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	p := Policy{BlockedGenres: []string{"Horror"}, BlockedKeywords: []string{"violence"}, AllowedHours: []int{9}}
	if err := svc.Put(ctx, 1, p); err != nil {
		t.Fatal(err)
	}
	p.BlockedGenres[0] = "Comedy"
	p.BlockedKeywords[0] = "romance"
	p.AllowedHours[0] = 22
	u := &core.User{ID: 1}
	if svc.Allowed(u, &core.Title{Genres: []string{"Horror"}}) || svc.Allowed(u, &core.Title{Keywords: []string{"violence"}}) {
		t.Fatal("caller changed active content restrictions without saving")
	}
	if !svc.CanPlay(u, false, time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)) || svc.CanPlay(u, false, time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC)) {
		t.Fatal("caller changed active playback hours without saving")
	}
}
