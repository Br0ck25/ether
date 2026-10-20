package plextv

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/remote"
)

// A transport failure on an endpoint that carries the server token in the
// query string must not echo the token in the returned error (which the
// registrar logs and the admin status shows).
func TestTransportErrorDoesNotLeakQueryToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close() // drop the connection without a response
		}
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL, Identity: ServerIdentity(testConfig(), ""), RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "super-secret-server-token"
	_, err = c.Users(context.Background(), secret)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the token: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) || !strings.Contains(ue.URL, "/api/v2/server/users") {
		t.Fatalf("error lost its url.Error context: %v", err)
	}

	// Context errors stay recognisable after redaction.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.once(ctx, request{method: http.MethodGet, path: "/x", token: secret, tokenQuery: "auth_token"}); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
		t.Fatalf("canceled request error %v", err)
	}
}

// The observed servers.plex.tv access_tokens shape puts a friend's identity
// on an <invited> child of the server token. Friends must keep their Plex
// id (and shared sections), or the auth sync deactivates every friend.
func TestAccessTokensObservedFriendShape(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	f.addUser("server-token-42", owner())
	f.accessXML = `<tokens>
  <token type="device" token="device-token" tokenDisabled="0" owned="1" createdAt="2026-01-02 03:04:05 UTC"/>
  <token type="server" token="friend-server-token" owned="0" createdAt="2026-01-02 03:04:05 UTC">
    <invited id="100" uuid="uuid-100" title="Alice" username="alice" thumb="https://plex.tv/users/a/avatar" scrobbleTypes="">
      <profile autoSelectAudio="1"/>
      <scrobbling/>
    </invited>
    <settings allowSync="1" filterAll=""/>
    <sections><section key="1" createdAt="2026-01-02 03:04:05 UTC"/><section key="2" createdAt="2026-01-02 03:04:05 UTC"/></sections>
  </token>
</tokens>`
	c := newServerClient(t, f)
	friends, err := c.Friends(context.Background(), "server-token-42")
	if err != nil {
		t.Fatal(err)
	}
	var alice *Friend
	for i := range friends {
		if friends[i].Token == "friend-server-token" {
			alice = &friends[i]
		}
	}
	if alice == nil {
		t.Fatalf("friend token missing: %+v", friends)
	}
	if alice.PlexID != 100 || alice.Username != "alice" || alice.Title != "Alice" || alice.UUID != "uuid-100" ||
		alice.Owned || strings.Join(alice.Sections, ",") != "1,2" {
		t.Fatalf("friend %+v", *alice)
	}
}

// leaseMapper grants a lease shorter than the registrar's MappingInterval.
type leaseMapper struct {
	mu      sync.Mutex
	now     func() time.Time
	lease   time.Duration
	st      remote.MappingStatus
	ensures int
}

func (m *leaseMapper) Ensure(ctx context.Context, internal, external int) (remote.MappingStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.st.Active && now.Before(m.st.RenewAt) {
		return m.st, nil
	}
	m.ensures++
	m.st = remote.MappingStatus{Active: true, Method: remote.MethodNATPMP, ExternalPort: external, InternalPort: internal,
		ExternalIP: "203.0.113.7", RenewAt: now.Add(m.lease / 2)}
	return m.st, nil
}
func (m *leaseMapper) Status() remote.MappingStatus { m.mu.Lock(); defer m.mu.Unlock(); return m.st }
func (m *leaseMapper) Close(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st = remote.MappingStatus{}
}

// A gateway granting a 2-minute lease must be renewed before it lapses,
// not only every MappingInterval (5 minutes).
func TestRegistrarRenewsShortLeaseBeforeExpiry(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	cfg := testConfig()
	cfg.Plex.RemoteAccess = true
	cfg.Plex.UseUPnP = true
	mapper := &leaseMapper{now: clock, lease: 2 * time.Minute}
	r, err := NewRegistrar(RegistrarOptions{
		Config: &memConfig{cfg: cfg}, Mapper: mapper, WAN: &fakeWAN{ip: "198.51.100.1"},
		LAN:             func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
		MappingInterval: 5 * time.Minute, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r.runDue(ctx)
	if mapper.ensures != 1 {
		t.Fatalf("initial mapping: %d", mapper.ensures)
	}
	advance(61 * time.Second) // past the renewal point (lease/2), well before MappingInterval
	r.runDue(ctx)
	if mapper.ensures != 2 {
		t.Fatalf("short lease not renewed before it lapsed: %d ensures", mapper.ensures)
	}
}

// A plex.direct request that failed once must be retried after the cert
// manager's RetryAfter, not after the weekly CertInterval: a fresh install
// would otherwise stay on the self-signed fallback for a week.
func TestRegistrarRetriesFailedCertificateAfterRetryAfter(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	f.addUser("server-token-42", owner())
	f.mu.Lock()
	f.csrStatus = http.StatusTooManyRequests
	f.mu.Unlock()
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	certs, err := NewCertManager(CertManagerOptions{Dir: t.TempDir(), Client: newServerClient(t, f), Now: clock,
		RetryAfter: 2 * time.Hour, Request: CertRequest{PollInterval: time.Millisecond, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Plex.Token = "server-token-42" // signed in; publishing is not under test
	r, err := NewRegistrar(RegistrarOptions{
		Config: &memConfig{cfg: cfg}, Client: newServerClient(t, f), Certs: certs, Mapper: &fakeMapper{},
		WAN: &fakeWAN{ip: "198.51.100.1"},
		LAN: func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
		Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r.runDue(ctx)
	if st := r.Status(); !st.Cert.SelfSigned || st.CertError == "" {
		t.Fatalf("first request should fail onto the fallback: %+v %q", st.Cert, st.CertError)
	}
	f.mu.Lock()
	f.csrStatus = 0
	f.mu.Unlock()
	advance(2*time.Hour + time.Minute)
	r.runDue(ctx)
	if st := r.Status(); st.Cert.SelfSigned || st.Cert.Hash != fakeHash {
		t.Fatalf("certificate not retried after RetryAfter: %+v %q", st.Cert, st.CertError)
	}
}
