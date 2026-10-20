package auth

import (
	"context"
	"testing"
	"time"
)

func TestFlushSeenRetainsObservationsAfterFailure(t *testing.T) {
	s, _, clock := setup(t)
	user, err := s.Authenticate(context.Background(), "alice-access")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.FlushSeen(ctx); err == nil {
		t.Fatal("expected canceled database write")
	}
	if err := s.FlushSeen(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := s.db.QueryRow(`SELECT last_seen_at FROM auth_users WHERE id=?`, user.ID).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != clock.now().Unix() {
		t.Fatalf("observation lost: %d", seen)
	}
}

func TestSeenObservationsCannotRegress(t *testing.T) {
	s, _, clock := setup(t)
	user, err := s.Owner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	latest := clock.now().Add(time.Minute)
	s.touch(user.ID, latest)
	s.touch(user.ID, clock.now()) // an older authentication finishes later
	if err := s.FlushSeen(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := s.db.QueryRow(`SELECT last_seen_at FROM auth_users WHERE id=?`, user.ID).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != latest.Unix() {
		t.Fatalf("pending observation regressed: %d", seen)
	}
	s.touch(user.ID, clock.now())
	if err := s.FlushSeen(context.Background()); err != nil {
		t.Fatal(err)
	}
	users, err := s.AllUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.ID == user.ID && !u.LastSeen.Equal(latest) {
			t.Fatalf("in-memory observation regressed: %v", u.LastSeen)
		}
	}
}
