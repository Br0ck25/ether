package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
