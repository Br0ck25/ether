package plextv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPresenceIdleWatchdog(t *testing.T) {
	keepalive := make(chan bool, 1) // true: send keep-alives while open
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": -1\n\n")
		fl.Flush()
		send := <-keepalive
		tick := time.NewTicker(40 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if send {
					fmt.Fprint(w, ": -1\n\n")
					fl.Flush()
				}
			}
		}
	}))
	defer sse.Close()
	c, err := New(Options{BaseURL: "http://unused.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPresence(PresenceOptions{
		Client:       c,
		StreamClient: sse.Client(),
		StreamURL:    func(string, int64, string, string) string { return sse.URL },
		IdleTimeout:  200 * time.Millisecond,
	})

	// Keep-alives keep a healthy stream open past the idle timeout.
	keepalive <- true
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	err = p.stream(ctx, "h", 1, "m", "tok")
	cancel()
	if errors.Is(err, errPubsubIdle) {
		t.Fatalf("healthy stream dropped by watchdog: %v", err)
	}

	// A silent (half-open) stream is dropped after the idle timeout.
	keepalive <- false
	start := time.Now()
	err = p.stream(context.Background(), "h", 1, "m", "tok")
	if !errors.Is(err, errPubsubIdle) {
		t.Fatalf("err = %v, want idle watchdog", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("watchdog took %v", d)
	}
}
