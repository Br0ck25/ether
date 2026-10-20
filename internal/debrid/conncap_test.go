package debrid

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnCapNeverExceedsHostLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Count the connections this process holds open, as the CDN does.
	var peak, open atomic.Int32
	d := &net.Dialer{}
	counting := func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if n := open.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		return &cappedConn{Conn: c, release: func() { open.Add(-1) }}, nil
	}
	limit := newConnCap(3)
	a, b := newStreamingTransport(limit), newStreamingTransport(limit)
	a.DialContext, b.DialContext = limit.wrap(counting), limit.wrap(counting)
	limit.closeIdle = func() { a.CloseIdleConnections(); b.CloseIdleConnections() }
	clients := []*http.Client{{Transport: a}, {Transport: b}}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			resp, err := clients[i%2].Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	if p := peak.Load(); p > 3 {
		t.Fatalf("peak connections to one host = %d, cap 3", p)
	}
	// Idle connections held by one pool are handed back to the other.
	a.CloseIdleConnections()
	b.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for limit.inUse(srv.Listener.Addr().String()) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := limit.inUse(srv.Listener.Addr().String()); n != 0 {
		t.Fatalf("connections still counted after close: %d", n)
	}
}

func TestConnCapDialHonorsContext(t *testing.T) {
	limit := newConnCap(1)
	if err := limit.acquire(context.Background(), "h:443"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := limit.acquire(ctx, "h:443"); err == nil {
		t.Fatal("second connection admitted past the cap")
	}
	limit.release("h:443")
	if err := limit.acquire(context.Background(), "h:443"); err != nil {
		t.Fatal(err)
	}
}
