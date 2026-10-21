package plextv

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestPublishRequestDuringPublishIsNotLost(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		name := "success"
		if failFirst {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakePlexTV(t, testMachineID)
			if !failFirst {
				f.addUser("server-token-42", owner())
			}
			cfg := testConfig()
			cfg.Plex.Token, cfg.Plex.Claimed = "server-token-42", true
			cfg.Plex.RemoteAccess = false
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var mu sync.Mutex
			peer := "http://192.168.1.20:32400"
			entered, release := make(chan struct{}), make(chan struct{})
			var first sync.Once
			r, err := NewRegistrar(RegistrarOptions{
				Config: &memConfig{cfg: cfg}, Client: newServerClient(t, f), Mapper: &fakeMapper{}, WAN: &fakeWAN{},
				LAN: func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
				PeerURIs: func() []string {
					mu.Lock()
					value := peer
					mu.Unlock()
					first.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
					return []string{value}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); r.runDue(ctx) }()
			select {
			case <-entered:
			case <-ctx.Done():
				<-done
				t.Fatal("publish did not reach peer snapshot")
			}
			mu.Lock()
			peer = "http://192.168.1.21:32400"
			mu.Unlock()
			r.RequestPublish()
			close(release)
			<-done
			if failFirst {
				f.addUser("server-token-42", owner())
			}
			r.runDue(ctx)
			if got := r.Status().PublishedURIs; !slices.Contains(got, peer) {
				t.Fatalf("new membership request was lost: published %v", got)
			}
		})
	}
}
