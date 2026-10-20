package invites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestRedeemOnTwoNodesHonoursUseLimit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	var shares atomic.Int32
	share := func(context.Context, Account, Invite) error { shares.Add(1); return nil }
	var nodes []*Service
	for i := 0; i < 2; i++ {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		nodes = append(nodes, New(db, Deps{Share: share}))
	}
	in, _, err := nodes[0].Create(ctx, Invite{Expires: time.Now().Add(time.Hour), MaxUses: 3, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 10; i++ {
		for _, s := range nodes {
			wg.Add(1)
			// The same account redeems on both nodes at once.
			go func(s *Service, id int64) {
				defer wg.Done()
				if s.Redeem(ctx, in, Account{id, "x@example.test"}) == nil {
					ok.Add(1)
				}
			}(s, int64(100+i))
		}
	}
	wg.Wait()
	var uses, claims int
	if err := nodes[1].db.QueryRow(`SELECT uses FROM invites_links WHERE id=?`, in.ID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := nodes[1].db.QueryRow(`SELECT COUNT(*) FROM invites_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if uses != 3 || claims != 3 || shares.Load() != 3 {
		t.Fatalf("uses=%d claims=%d shares=%d", uses, claims, shares.Load())
	}
	if ok.Load() < 3 {
		t.Fatalf("successful redemptions %d", ok.Load())
	}
}

// A sign-in started on one node can be polled on another, and the plex.tv
// account it resolved to survives a retry on a third request elsewhere.
func TestPINSignInPollsFromAnyNode(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	now := time.Now()
	var polls, shares atomic.Int32
	deps := Deps{
		Now: func() time.Time { return now },
		Start: func(context.Context) (string, string, string, error) {
			return "pin-1", "ABCD", "https://example.test/link", nil
		},
		Poll: func(_ context.Context, id string) (*Account, error) {
			if id != "pin-1" {
				t.Errorf("polled %q", id)
			}
			if polls.Add(1) == 1 {
				return nil, nil // not signed in yet
			}
			return &Account{7, "seven@example.test"}, nil
		},
		Share: func(context.Context, Account, Invite) error {
			if shares.Add(1) == 1 {
				return errors.New("plex.tv offline")
			}
			return nil
		},
	}
	a, b := New(db, deps), New(db, deps)
	_, token, err := a.Create(ctx, Invite{Expires: now.Add(time.Hour), MaxUses: 2, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	do := func(s *Service, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.PublicHandler().ServeHTTP(w, httptest.NewRequest("POST", "/"+token+path, strings.NewReader(body)))
		return w
	}
	w := do(a, "/pin", "")
	var started struct{ Handle, Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || started.Handle == "" || started.Code != "ABCD" {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	var stored string
	if err := db.QueryRow(`SELECT handle_hash FROM invites_pins`).Scan(&stored); err != nil || stored == started.Handle {
		t.Fatalf("handle stored in clear or missing: %v", err)
	}
	poll := `{"handle":"` + started.Handle + `"}`
	if w := do(b, "/poll", poll); w.Code != 200 || !strings.Contains(w.Body.String(), `"done":false`) {
		t.Fatalf("first poll on b: %d %s", w.Code, w.Body.String())
	}
	if w := do(b, "/poll", poll); w.Code != 409 {
		t.Fatalf("share failure: %d %s", w.Code, w.Body.String())
	}
	// The retry on a's side reuses the account b already got from plex.tv.
	if w := do(a, "/poll", poll); w.Code != 200 || !strings.Contains(w.Body.String(), `"done":true`) {
		t.Fatalf("retry on a: %d %s", w.Code, w.Body.String())
	}
	if polls.Load() != 2 || shares.Load() != 2 {
		t.Fatalf("polls=%d shares=%d", polls.Load(), shares.Load())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM invites_pins`).Scan(&n)
	if n != 0 {
		t.Fatal("finished sign-in left behind")
	}
	// An expired sign-in is refused on every node and swept on the next start.
	old := started.Handle
	if w = do(a, "/pin", ""); w.Code != 200 {
		t.Fatalf("second start %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &started)
	if started.Handle == old {
		t.Fatal("handle reused")
	}
	now = now.Add(pinTTL + time.Second)
	if w := do(b, "/poll", `{"handle":"`+started.Handle+`"}`); w.Code != 404 {
		t.Fatalf("expired poll: %d", w.Code)
	}
	do(b, "/pin", "")
	_ = db.QueryRow(`SELECT COUNT(*) FROM invites_pins`).Scan(&n)
	if n != 1 {
		t.Fatalf("expired sign-ins not swept: %d rows", n)
	}
}
