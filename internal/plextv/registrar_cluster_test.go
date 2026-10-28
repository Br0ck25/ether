package plextv

import (
	"context"
	"encoding/xml"
	"net"
	"testing"
	"time"
)

// servers.xml lists every live node's LAN address, not only the leader's.
func TestRegistrarRegistersPeerLANAddresses(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	f.addUser("server-token-42", owner())
	cfg := testConfig()
	cfg.Plex.Token = "server-token-42"
	cfg.Plex.Claimed = true
	cfg.Plex.RemoteAccess = true
	r, err := NewRegistrar(RegistrarOptions{
		Config: &memConfig{cfg: cfg}, Client: newServerClient(t, f), WAN: &fakeWAN{ip: "198.51.100.1"}, Mapper: &fakeMapper{},
		LAN: func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
		PeerURIs: func() []string {
			return []string{
				"http://192.168.1.6:32400",
				"http://198.51.100.1:32401", // a peer's WAN URI: not a LAN address
				"http://192.168.1.5:32400",  // duplicate of our own
				"https://192-168-1-7.0123456789abcdef0123456789abcdef.plex.direct:32400",
				"https://plex.example.com", // custom URL
				"http://10.0.0.8:32402",
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PublishNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registered) == 0 {
		t.Fatal("servers.xml not posted")
	}
	var reg struct {
		Server struct {
			LocalAddresses string `xml:"localAddresses,attr"`
		}
	}
	if err := xml.Unmarshal([]byte(f.registered[len(f.registered)-1]), &reg); err != nil {
		t.Fatal(err)
	}
	if want := "192.168.1.5,192.168.1.6,192.168.1.7,10.0.0.8"; reg.Server.LocalAddresses != want {
		t.Fatalf("localAddresses %q, want %q", reg.Server.LocalAddresses, want)
	}
}

// With NodeMapping the publishing loop leaves port mapping to
// RunPortMapping, and losing leadership (Stop) keeps this node's mapping.
func TestRegistrarNodeMapping(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	cfg := testConfig()
	cfg.Plex.RemoteAccess = true
	cfg.Plex.UseUPnP = true
	cfg.Plex.PublicPort = 32402
	mapper := &fakeMapper{}
	r, err := NewRegistrar(RegistrarOptions{
		Config: &memConfig{cfg: cfg}, Client: newServerClient(t, f), WAN: &fakeWAN{ip: "198.51.100.1"}, Mapper: mapper,
		LAN:         func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
		NodeMapping: true,
		Tick:        10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r.runDue(ctx)
	mapper.mu.Lock()
	ensures := mapper.ensures
	mapper.mu.Unlock()
	if ensures != 0 {
		t.Fatal("publishing loop mapped the port despite NodeMapping")
	}

	mctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.RunPortMapping(mctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for !mapper.Status().Active {
		if time.Now().After(deadline) {
			t.Fatal("node did not map its port")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := mapper.Status(); st.ExternalPort != 32402 || st.InternalPort != 32400 {
		t.Fatalf("mapping %+v", st)
	}
	// The node's own URIs pick up the mapped WAN address.
	uris := r.NodeURIs(ctx)
	found := false
	for _, u := range uris {
		if u == "http://203.0.113.7:32402" {
			found = true
		}
	}
	if !found {
		t.Fatalf("node URIs %v", uris)
	}

	// Leadership lost: the loop stops, the mapping stays.
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	r.Stop()
	if !mapper.Status().Active {
		t.Fatal("Stop removed the node's own mapping")
	}
	cancel()
	<-done
	if mapper.Status().Active {
		t.Fatal("mapping not removed when the node stops")
	}
}
