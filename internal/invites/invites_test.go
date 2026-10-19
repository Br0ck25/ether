package invites

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInviteLimitsAndIdempotency(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	calls := 0
	now := time.Now()
	s := New(db, Deps{Now: func() time.Time { return now }, Share: func(context.Context, Account, Invite) error { calls++; return nil }})
	in, token, err := s.Create(ctx, Invite{Label: "Family", Expires: now.Add(time.Hour), MaxUses: 1, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.lookup(ctx, token); err != nil {
		t.Fatal(err)
	}
	a := Account{1, "one@example.test"}
	if err = s.Redeem(ctx, in, a); err != nil {
		t.Fatal(err)
	}
	if err = s.Redeem(ctx, in, a); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("duplicate external share")
	}
	if err = s.Redeem(ctx, in, Account{2, "two@example.test"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("over quota %v", err)
	}
	var stored string
	if err = db.QueryRow(`SELECT token_hash FROM invites_links`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || strings.Contains(stored, "example") {
		t.Fatal("raw credential persisted")
	}
}
func TestFailedShareReleasesReservation(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, Deps{Share: func(context.Context, Account, Invite) error { return errors.New("offline") }})
	in, _, err := s.Create(ctx, Invite{Expires: time.Now().Add(time.Hour), MaxUses: 1, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Redeem(ctx, in, Account{1, "one@example.test"}) == nil {
		t.Fatal("want failure")
	}
	var uses int
	_ = db.QueryRow(`SELECT uses FROM invites_links`).Scan(&uses)
	if uses != 0 {
		t.Fatal("reservation leaked")
	}
}
func TestPublicLandingEscapesAndRateLimits(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, Deps{})
	_, token, err := s.Create(ctx, Invite{Label: "<script>secret()</script>", Expires: time.Now().Add(time.Hour), MaxUses: 1, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	for i := 0; i < 31; i++ {
		r := httptest.NewRequest(http.MethodGet, "/"+token, nil)
		r.RemoteAddr = "192.0.2.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i == 0 {
			if w.Code != 200 || strings.Contains(w.Body.String(), "<script>secret()") {
				t.Fatalf("unsafe landing %s", w.Body.String())
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("missing CSP")
			}
		}
		if i == 30 && w.Code != 429 {
			t.Fatal("rate limit missing")
		}
	}
}

func TestPollRetriesPolicyWithoutRepeatingAccountOrShare(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	polls, shares, applies := 0, 0, 0
	s := New(db, Deps{Start: func(context.Context) (string, string, string, error) {
		return "pin", "code", "https://example.test", nil
	}, Poll: func(context.Context, string) (*Account, error) {
		polls++
		if polls > 1 {
			return nil, errors.New("PIN consumed")
		}
		return &Account{42, "person@example.test"}, nil
	}, Share: func(context.Context, Account, Invite) error { shares++; return nil }, Apply: func(context.Context, Account, Invite) error {
		applies++
		if applies == 1 {
			return errors.New("temporarily offline")
		}
		return nil
	}})
	_, token, err := s.Create(ctx, Invite{Expires: time.Now().Add(time.Hour), MaxUses: 1, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/"+token+"/pin", nil))
	var started struct {
		Handle string `json:"handle"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{409, 200} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/"+token+"/poll", strings.NewReader(`{"handle":"`+started.Handle+`"}`)))
		if w.Code != want {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if polls != 1 || shares != 1 || applies != 2 {
		t.Fatalf("poll=%d share=%d apply=%d", polls, shares, applies)
	}
	var uses int
	_ = db.QueryRow(`SELECT uses FROM invites_links`).Scan(&uses)
	if uses != 1 {
		t.Fatalf("uses=%d", uses)
	}
}
