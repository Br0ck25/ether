package plextv

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestRegistrarPublishesOwnerFeatures(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	account := owner()
	account.Subscription.Features = []string{"pass", "remote-watch-pass"}
	f.addUser("user-token", account)
	r, err := NewRegistrar(RegistrarOptions{Config: &memConfig{cfg: testConfig()}, Client: newServerClient(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func(want []string) {
		t.Helper()
		b, err := json.Marshal(r.Status())
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			OwnerFeatures []string `json:"ownerFeatures"`
		}
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(st.OwnerFeatures, want) {
			t.Fatalf("owner features = %v, want %v", st.OwnerFeatures, want)
		}
	}
	if _, err := r.Claim(ctx, "user-token"); err != nil {
		t.Fatal(err)
	}
	check(account.Subscription.Features)
	// Changes must replace the previous snapshot, including revoked features.
	account.Subscription.Features = []string{"pass"}
	f.addUser("server-token-42", account)
	if err := r.RefreshOwner(ctx); err != nil {
		t.Fatal(err)
	}
	check(account.Subscription.Features)
	if err := r.Unclaim(ctx); err != nil {
		t.Fatal(err)
	}
	check(nil)
}

func TestRegistrarRefreshesOwnerSubscription(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	account := owner()
	account.Subscription = Subscription{}
	f.addUser("user-token", account)
	var mu sync.Mutex
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	r, err := NewRegistrar(RegistrarOptions{
		Config: &memConfig{cfg: testConfig()}, Client: newServerClient(t, f), Users: &fakeUsers{},
		WAN: &fakeWAN{ip: "198.51.100.1"}, Mapper: &fakeMapper{},
		LAN: func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.Claim(ctx, "user-token"); err != nil {
		t.Fatal(err)
	}
	f.addUser("server-token-42", account)
	r.runDue(ctx)
	if r.Status().PlexPass {
		t.Fatal("Plex Pass without a subscription")
	}

	// Plex Pass bought while Ether runs: picked up on the next owner check,
	// not only after a restart.
	account.Subscription = owner().Subscription
	f.addUser("server-token-42", account)
	r.runDue(ctx)
	if r.Status().PlexPass {
		t.Fatal("owner re-checked before its interval")
	}
	mu.Lock()
	now = now.Add(6*time.Hour + time.Minute)
	mu.Unlock()
	r.runDue(ctx)
	if !r.Status().PlexPass {
		t.Fatal("Plex Pass bought while running was not picked up")
	}
}
