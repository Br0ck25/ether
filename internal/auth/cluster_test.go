package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plextv"
)

// twoNodes opens two services on separate handles to one database file, as
// two cluster nodes would.
func twoNodes(t *testing.T, v Validator) (*Service, *Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ether.db")
	c := newClock()
	o := Options{Validator: v, Now: c.now, CacheTTL: 10 * time.Minute, NegativeTTL: time.Minute, StaleTTL: time.Hour}
	a, err := New(context.Background(), openDBAt(t, path), o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(context.Background(), openDBAt(t, path), o)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestSetOwnerConcurrentNodesKeepsOneOwner(t *testing.T) {
	a, b := twoNodes(t, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			errs <- a.SetOwner(ctx, plextv.Account{ID: int64(1000 + i), Username: "a"}, "tok-a")
		}(i)
		go func(i int) {
			defer wg.Done()
			errs <- b.SetOwner(ctx, plextv.Account{ID: int64(2000 + i), Username: "b"}, "tok-b")
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM auth_users WHERE is_owner=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("owners=%d err=%v", n, err)
	}
	// The database refuses a second owner row outright.
	if _, err := a.db.Exec(`INSERT INTO auth_users(plex_id,is_owner,created_at,updated_at) VALUES(5,1,0,0)`); !isUniqueViolation(err) {
		t.Fatalf("second owner accepted: %v", err)
	}
}

func TestValidationOnOtherNodeDoesNotReactivateRevokedUser(t *testing.T) {
	v := &fakeValidator{
		accounts: map[string]plextv.Account{"bob-device": {ID: 101, Username: "bob"}},
		access:   map[string]bool{"bob-device": true},
	}
	a, b := twoNodes(t, v)
	ctx := context.Background()
	// Node A learns about bob through validation; node B never has.
	if _, err := a.Authenticate(ctx, "bob-device"); err != nil {
		t.Fatal(err)
	}
	// Node A's authoritative sync drops bob.
	if err := a.SyncFriends(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Node B's validation must not resurrect the revoked row.
	if _, err := b.Authenticate(ctx, "bob-device"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("revoked user accepted on other node: %v", err)
	}
	var active int
	if err := a.db.QueryRow(`SELECT active FROM auth_users WHERE plex_id=101`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active=%d err=%v", active, err)
	}
}

// busNodes builds two services on one database (one shared handle: on
// PostgreSQL one schema, as two cluster nodes would see it) joined by an
// in-memory cluster bus.
func busNodes(t *testing.T, v Validator) (*Service, *Service, *clusterbus.Hub) {
	t.Helper()
	db := openDB(t)
	c := newClock()
	o := Options{Validator: v, Now: c.now, CacheTTL: 10 * time.Minute, NegativeTTL: time.Minute, StaleTTL: time.Hour}
	a, err := New(context.Background(), db, o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(context.Background(), db, o)
	if err != nil {
		t.Fatal(err)
	}
	hub := clusterbus.NewHub()
	a.SetBus(hub.Node("a"))
	b.SetBus(hub.Node("b"))
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b, hub
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTokenGrantAndRevokeReachOtherNode(t *testing.T) {
	a, b, _ := busNodes(t, nil)
	ctx := context.Background()
	if err := a.SetOwner(ctx, plextv.Account{ID: 1, Username: "owner"}, "server-token"); err != nil {
		t.Fatal(err)
	}
	// Node A's friend sync grants bob a token; node B learns it.
	if err := a.SyncFriends(ctx, []plextv.Friend{{PlexID: 101, Username: "bob", Token: "bob-token", AllLibs: true}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B accepts bob", func() bool { _, err := b.Authenticate(ctx, "bob-token"); return err == nil })
	// Node A's sync revokes bob; node B rejects him after the invalidation.
	if err := a.SyncFriends(ctx, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B rejects bob", func() bool {
		_, err := b.Authenticate(ctx, "bob-token")
		return errors.Is(err, core.ErrUnauthorized)
	})
	if _, err := b.Authenticate(ctx, "server-token"); err != nil {
		t.Fatalf("owner token rejected on B: %v", err)
	}
}

func TestPerUserSettingsAndCachedValidationsFollowOtherNode(t *testing.T) {
	v := &fakeValidator{
		accounts: map[string]plextv.Account{"carol-device": {ID: 202, Username: "carol"}},
		access:   map[string]bool{"carol-device": true},
	}
	a, b, _ := busNodes(t, v)
	ctx := context.Background()
	u, err := b.Authenticate(ctx, "carol-device")
	if err != nil {
		t.Fatal(err)
	}
	// A changes carol's stream limit; B serves the new value.
	eventually(t, "A knows carol", func() bool { _, err := a.User(ctx, u.ID); return err == nil })
	cu, _ := a.User(ctx, u.ID)
	cu.StreamLimit = 3
	if err := a.UpdateUser(ctx, *cu); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B sees limit", func() bool { got, _ := b.User(ctx, u.ID); return got != nil && got.StreamLimit == 3 })
	// A forgets carol's token: B's cached validation goes too, so B asks
	// plex.tv again.
	before := v.calls.Load()
	a.Invalidate("carol-device")
	eventually(t, "B revalidates", func() bool {
		if _, err := b.Authenticate(ctx, "carol-device"); err != nil {
			t.Fatal(err)
		}
		return v.calls.Load() > before
	})
}

// last_seen_at only moves forward, whichever node flushes last.
func TestFlushSeenNeverMovesBackwards(t *testing.T) {
	a, b, _ := busNodes(t, nil)
	ctx := context.Background()
	if err := a.SetOwner(ctx, plextv.Account{ID: 1, Username: "owner"}, "tok"); err != nil {
		t.Fatal(err)
	}
	owner, _ := a.Owner(ctx)
	a.touch(owner.ID, time.Unix(2_000_000_000, 0))
	b.touch(owner.ID, time.Unix(1_900_000_000, 0))
	if err := a.FlushSeen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.FlushSeen(ctx); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := a.db.QueryRow(`SELECT last_seen_at FROM auth_users WHERE id=?`, owner.ID).Scan(&seen); err != nil || seen != 2_000_000_000 {
		t.Fatalf("last_seen_at=%d err=%v", seen, err)
	}
}
