package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plextv"
)

func TestDelegationExpiryRevocationAndScope(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	if err := s.SetOwner(ctx, plextv.Account{ID: 42, Username: "owner"}, "account-token"); err != nil {
		t.Fatal(err)
	}
	owner, err := s.Authenticate(ctx, "account-token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.o.Now = func() time.Time { return now }
	token, err := s.IssueDelegation(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, delegationPrefix) || token == "account-token" {
		t.Fatal("account token leaked")
	}
	u, err := s.Authenticate(ctx, token)
	if err != nil || u.ID != owner.ID || u.IsOwner || !u.Delegated {
		t.Fatalf("delegation scope: %+v %v", u, err)
	}
	now = now.Add(delegationTTL)
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("expired credential: %v", err)
	}
	token, err = s.IssueDelegation(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE auth_users SET active=0 WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("revoked membership: %v", err)
	}
	if _, err := s.IssueDelegation(ctx, owner.ID); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("issued for inactive viewer: %v", err)
	}
}

func TestDelegationWorksAcrossNodesWithoutUpstreamValidation(t *testing.T) {
	s, v, clock := setup(t)
	ctx := context.Background()
	alice, err := s.Authenticate(ctx, "alice-access")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueDelegation(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := New(ctx, s.db, Options{Validator: v, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	before := v.calls.Load()
	got, err := peer.Authenticate(ctx, token)
	if err != nil || got.ID != alice.ID || len(got.AllowedSections) != 1 || got.AllowedSections[0] != 1 {
		t.Fatalf("peer delegation: %+v %v", got, err)
	}
	if _, err := peer.Authenticate(ctx, delegationPrefix+"invalid"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("unknown delegation: %v", err)
	}
	if v.calls.Load() != before {
		t.Fatal("server-local token sent to plex.tv")
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM auth_delegations WHERE user_id=?`, alice.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || stored != HashToken(token) {
		t.Fatal("casting credential not stored as hash")
	}
	if err := s.SyncFriends(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Authenticate(ctx, token); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("peer accepted revoked viewer: %v", err)
	}
}
