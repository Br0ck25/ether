package safety

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
	"time"
)

func TestPolicyPersistsAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	p := KidsPreset("preschool")
	p.RemoteDisabled = true
	p.AllowedHours = []int{9}
	if err := s.Put(ctx, 1, p); err != nil {
		t.Fatal(err)
	}
	s, err = New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		title core.Title
		want  bool
	}{{core.Title{ContentRating: "TV-Y"}, true}, {core.Title{}, false}, {core.Title{ContentRating: "R"}, false}, {core.Title{ContentRating: "G", Adult: true}, false}, {core.Title{ContentRating: "G", Keywords: []string{"pornography"}}, false}} {
		if got := s.Allowed(u, &tt.title); got != tt.want {
			t.Errorf("%+v got %v", tt.title, got)
		}
	}
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if s.CanPlay(u, true, now) || !s.CanPlay(u, false, now) || s.CanPlay(u, false, now.Add(time.Hour)) {
		t.Fatal("play policy failed")
	}
}
