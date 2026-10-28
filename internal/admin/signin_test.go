package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
)

// A reverse proxy can use loopback and Host: localhost. Neither grants access.
func TestLoopbackStillRequiresOwner(t *testing.T) {
	store := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = true })
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	for _, tc := range []struct {
		remote, host string
		want         int
	}{
		{"127.0.0.1:5000", "localhost:32400", 401},
		{"[::1]:5000", "[::1]:32400", 401},
		{"127.0.0.1:5000", "127.0.0.1:32400", 401},
		{"127.0.0.1:5000", "evil.example:32400", 401}, // DNS rebinding
		{"10.0.0.5:1", "localhost:32400", 401},        // LAN peer claiming localhost
	} {
		r := req(http.MethodGet, "/ether/api/setup/status", "")
		r.RemoteAddr, r.Host = tc.remote, tc.host
		if rec := serve(h, r); rec.Code != tc.want {
			t.Errorf("%s via %s: got %d want %d", tc.remote, tc.host, rec.Code, tc.want)
		}
	}
	// Bootstrap requires sign-in locally as well.
	r := req(http.MethodGet, "/ether/api/bootstrap", "")
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "localhost:32400"
	var boot struct {
		Authenticated, AuthRequired bool
	}
	if err := json.Unmarshal(serve(h, r).Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.Authenticated || !boot.AuthRequired {
		t.Fatalf("bootstrap %+v", boot)
	}
}

type fakeSignIn struct {
	mu      sync.Mutex
	forward string
	tokens  map[string]string // pin id -> token once approved
}

func (f *fakeSignIn) StartPIN(_ context.Context, forward string) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forward = forward
	return "4242", "ABCD", "https://app.plex.tv/auth#?code=ABCD", nil
}

func (f *fakeSignIn) PINToken(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[id], nil
}

func (f *fakeSignIn) approve(id, token string) {
	f.mu.Lock()
	f.tokens[id] = token
	f.mu.Unlock()
}

func TestSignInWithPlex(t *testing.T) {
	store := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = true })
	si := &fakeSignIn{tokens: map[string]string{}}
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store), SignIn: si})

	start := func() string {
		r := req(http.MethodPost, "/ether/api/auth/pin", "{}")
		r.RemoteAddr, r.Host = "10.0.0.5:1", "ether.lan:32400"
		rec := serve(h, r)
		if rec.Code != 200 {
			t.Fatalf("start: %d %s", rec.Code, rec.Body)
		}
		var out struct{ Handle, Code, AuthURL string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Handle == "" || out.Handle == "4242" || out.Code != "ABCD" {
			t.Fatalf("start response %+v: handle must be opaque, not the plex.tv PIN id", out)
		}
		return out.Handle
	}
	poll := func(handle string) (int, map[string]any) {
		r := req(http.MethodGet, "/ether/api/auth/pin/"+handle, "")
		r.RemoteAddr = "10.0.0.5:1"
		rec := serve(h, r)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	handle := start()
	if si.forward != "http://ether.lan:32400/ether/" {
		t.Errorf("forward URL %q", si.forward)
	}
	if code, body := poll(handle); code != 200 || body["done"] != false {
		t.Fatalf("pending poll: %d %v", code, body)
	}
	if code, _ := poll("4242"); code != 404 {
		t.Fatalf("polling by raw PIN id must fail, got %d", code)
	}
	si.approve("4242", ownerToken)
	code, body := poll(handle)
	if code != 200 || body["done"] != true || body["token"] != ownerToken {
		t.Fatalf("approved poll: %d %v", code, body)
	}
	if code, _ := poll(handle); code != 404 {
		t.Fatalf("handle must be single-use, got %d", code)
	}

	// A Plex account that doesn't own the server gets no token.
	handle = start()
	si.approve("4242", "someone-else")
	if code, body := poll(handle); code != 403 || body["token"] != nil {
		t.Fatalf("non-owner: %d %v", code, body)
	}

	// Starting a sign-in is a mutation: it needs the CSRF header.
	r := req(http.MethodPost, "/ether/api/auth/pin", "{}")
	r.Header.Del("X-Ether-Request")
	if rec := serve(h, r); rec.Code != http.StatusForbidden {
		t.Fatalf("start without CSRF header: %d", rec.Code)
	}
}
