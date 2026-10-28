package plextv

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistrarPublishesOwnerFeatures(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	f.features = []Feature{
		{ID: "remote-watch-pass", UUID: "76ddd91e-8321-4916-94b6-ded8e3727a64"},
		{ID: "pass", UUID: "82999dd3-a2be-482e-9f44-357879b4f603"},
	}
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
	want := []string{f.features[0].UUID, f.features[1].UUID, "pass", "remote-watch-pass"}
	check(want)
	h := f.header("/api/v2/features")
	if h.Get("X-Plex-Token") != "server-token-42" || h.Get("X-Plex-Product") != ServerProduct || h.Get("X-Plex-Provides") != "server" || h.Get("X-Plex-Client-Identifier") != testMachineID {
		t.Fatal("feature lookup did not use the claimed server identity and token")
	}
	// A temporary API failure must not remove verified UUIDs from native clients.
	f.mu.Lock()
	f.featuresErr = http.StatusServiceUnavailable
	f.mu.Unlock()
	if err := r.RefreshOwner(ctx); err == nil {
		t.Fatal("feature lookup failure was hidden")
	}
	check(want)
	// Changes must replace the previous snapshot, including revoked features.
	account.Subscription.Features = []string{"pass"}
	f.addUser("server-token-42", account)
	f.mu.Lock()
	f.featuresErr = 0
	f.features = []Feature{f.features[1]}
	f.mu.Unlock()
	if err := r.RefreshOwner(ctx); err != nil {
		t.Fatal(err)
	}
	check([]string{f.features[0].UUID, "pass"})
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
	f.mu.Lock()
	f.features = []Feature{{ID: "pass", UUID: "82999dd3-a2be-482e-9f44-357879b4f603"}}
	f.mu.Unlock()
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
	if !slices.Contains(r.Status().OwnerFeatures, "82999dd3-a2be-482e-9f44-357879b4f603") {
		t.Fatal("six-hour refresh did not update feature UUIDs")
	}
}

func TestRegistrarFeatureLookupFailureDoesNotUndoClaim(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	account := owner()
	account.Subscription.Features = []string{"pass"}
	f.addUser("user-token", account)
	f.featuresErr = http.StatusServiceUnavailable
	cfg := &memConfig{cfg: testConfig()}
	r, err := NewRegistrar(RegistrarOptions{Config: cfg, Client: newServerClient(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Claim(t.Context(), "user-token"); err != nil {
		t.Fatal(err)
	}
	if !cfg.Get().Plex.Claimed || !slices.Equal(r.Status().OwnerFeatures, []string{"pass"}) {
		t.Fatal("feature outage undid the claim or lost legacy features")
	}
	// Existing installations fetch UUIDs at startup without requiring a re-claim.
	f.mu.Lock()
	f.featuresErr = 0
	f.features = []Feature{{ID: "pass", UUID: "82999dd3-a2be-482e-9f44-357879b4f603"}}
	f.mu.Unlock()
	restarted, err := NewRegistrar(RegistrarOptions{Config: cfg, Client: newServerClient(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RefreshOwner(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(restarted.Status().OwnerFeatures, []string{f.features[0].UUID, "pass"}) {
		t.Fatalf("startup features: %v", restarted.Status().OwnerFeatures)
	}
}

func TestOwnerFeatureLookupDoesNotRestoreUnclaimedEntitlements(t *testing.T) {
	for _, duringClaim := range []bool{false, true} {
		name := "refresh"
		if duringClaim {
			name = "claim"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakePlexTV(t, testMachineID)
			f.addUser("user-token", owner())
			f.features = []Feature{{ID: "pass", UUID: "82999dd3-a2be-482e-9f44-357879b4f603"}}
			var block atomic.Bool
			started, release := make(chan struct{}), make(chan struct{})
			f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/api/v2/features" && block.Load() {
					close(started)
					select {
					case <-release:
					case <-req.Context().Done():
						return
					}
				}
				f.handle(w, req)
			})
			r, err := NewRegistrar(RegistrarOptions{Config: &memConfig{cfg: testConfig()}, Client: newServerClient(t, f)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if !duringClaim {
				if _, err := r.Claim(ctx, "user-token"); err != nil {
					t.Fatal(err)
				}
			}
			block.Store(true)
			done := make(chan error, 1)
			go func() {
				if duringClaim {
					_, err := r.Claim(ctx, "user-token")
					done <- err
				} else {
					done <- r.RefreshOwner(ctx)
				}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := r.Unclaim(ctx); err != nil {
				close(release)
				t.Fatal(err)
			}
			close(release)
			if err := <-done; !duringClaim && err == nil {
				t.Fatal("stale refresh succeeded after unclaim")
			}
			if st := r.Status(); st.PlexPass || len(st.OwnerFeatures) != 0 {
				t.Fatal("old owner entitlements survived unclaim")
			}
		})
	}
}
