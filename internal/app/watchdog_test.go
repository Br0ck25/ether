package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testWatchdog(probe func(context.Context) error, threshold int) (*watchdog, chan struct{}) {
	forced := make(chan struct{}, 1)
	return &watchdog{
		interval:  time.Millisecond,
		threshold: threshold,
		probe:     probe,
		log:       quietLogger(),
		forceExit: func() { forced <- struct{}{} },
		exitGrace: 20 * time.Millisecond,
	}, forced
}

func TestWatchdogFiresAfterConsecutiveFailures(t *testing.T) {
	var calls atomic.Int32
	w, forced := testWatchdog(func(context.Context) error {
		calls.Add(1)
		return errors.New("down")
	}, 3)
	if err := w.run(context.Background()); !errors.Is(err, ErrUnresponsive) {
		t.Fatalf("run = %v, want ErrUnresponsive", err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("probes = %d, want 3", n)
	}
	select {
	case <-forced:
	case <-time.After(5 * time.Second):
		t.Fatal("force-exit fallback not armed")
	}
}

func TestWatchdogResetsOnSuccess(t *testing.T) {
	// fail, fail, ok, fail, fail, ok ... never three in a row.
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, _ := testWatchdog(func(context.Context) error {
		n := calls.Add(1)
		if n >= 30 {
			cancel()
		}
		if n%3 == 0 {
			return nil
		}
		return errors.New("down")
	}, 3)
	if err := w.run(ctx); err != nil {
		t.Fatalf("run = %v, want nil (failures were never consecutive)", err)
	}
}

func TestWatchdogStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w, _ := testWatchdog(func(context.Context) error { return nil }, 1)
	done := make(chan error, 1)
	go func() { done <- w.run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not stop")
	}
}

func TestHTTPProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/identity" {
			t.Errorf("probed %s", r.URL.Path)
		}
	}))
	defer ok.Close()
	if err := httpProbe(ok.URL+"/identity", time.Second)(context.Background()); err != nil {
		t.Fatalf("healthy server: %v", err)
	}

	// Any status counts as alive.
	unhappy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhappy.Close()
	if err := httpProbe(unhappy.URL, time.Second)(context.Background()); err != nil {
		t.Fatalf("503 should count as alive: %v", err)
	}

	// A hung handler times out.
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)
	if err := httpProbe(hung.URL, 50*time.Millisecond)(context.Background()); err == nil {
		t.Fatal("hung server reported alive")
	}

	// Nothing listening.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	if err := httpProbe(url, time.Second)(context.Background()); err == nil {
		t.Fatal("closed server reported alive")
	}
}

func TestWatchdogAgainstHungServer(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)
	w, _ := testWatchdog(httpProbe(hung.URL+"/identity", 20*time.Millisecond), 2)
	if err := w.run(context.Background()); !errors.Is(err, ErrUnresponsive) {
		t.Fatalf("run = %v, want ErrUnresponsive", err)
	}
}

func TestProbeURL(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.IPv4zero, Port: 32400}, "http://127.0.0.1:32400/identity"},
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 32400}, "http://127.0.0.1:32400/identity"},
		{&net.TCPAddr{IP: net.ParseIP("192.168.1.5"), Port: 8080}, "http://192.168.1.5:8080/identity"},
		{&net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 1}, "http://[fe80::1]:1/identity"},
	} {
		if got := probeURL(tc.addr); got != tc.want {
			t.Errorf("probeURL(%v) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
