package debrid

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type startupTransport func(*http.Request) (*http.Response, error)

func (f startupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStartupRegeneratesLinkOnDeadCDN(t *testing.T) {
	r := newEngineRig(t, 256<<10, -1)
	r.eng.hc = &http.Client{Transport: startupTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/m/realdebrid-1" {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	before := r.provs["realdebrid-0"].callCount("link")
	ok, err := r.eng.Check(context.Background(), r.pl)
	if err != nil || !ok {
		t.Fatalf("dead CDN was retried without a new link: %v", err)
	}
	if got := r.provs["realdebrid-0"].callCount("link") - before; got != 1 {
		t.Fatalf("link calls=%d, want one", got)
	}
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	b := make([]byte, 16)
	if _, err = src.ReadAt(b, 0); err != nil || !bytes.Equal(b, r.media.data[:16]) {
		t.Fatalf("refreshed file: %v", err)
	}
}

func TestCDNOutageRefreshIsBoundedAndTransient(t *testing.T) {
	r := newEngineRig(t, 256<<10, -1)
	r.eng.now = func() time.Time { return time.Unix(1700000000, 0) }
	r.eng.hc = &http.Client{Transport: startupTransport(func(req *http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})}
	before := r.provs["realdebrid-0"].callCount("link")
	for n := 0; n < 2; n++ {
		ok, err := r.eng.Check(context.Background(), r.pl)
		if ok || !errors.Is(err, core.ErrSourceUnavailable) || errors.Is(err, core.ErrArticleMissing) {
			t.Fatalf("outage misclassified: ok=%v err=%v", ok, err)
		}
	}
	if got := r.provs["realdebrid-0"].callCount("link") - before; got != 1 {
		t.Fatalf("outage generated %d links, want one", got)
	}
}
