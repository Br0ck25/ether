package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A transport failure must not echo the v3 api_key, which net/http embeds
// in the *url.Error message along with the full request URL.
func TestTransportErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connection refused
	c := New("secretkey0123456789abcdef0123456", nil, WithBaseURL(srv.URL), WithMaxRetries(0))
	defer c.Close()
	_, err := c.Genres(context.Background(), "movie")
	if err == nil {
		t.Fatal("want transport error")
	}
	if strings.Contains(err.Error(), "secretkey") {
		t.Fatalf("error leaks api key: %v", err)
	}
}

// A coalesced waiter must not inherit the leader's context cancellation.
func TestCoalescedWaiterSurvivesLeaderCancel(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		fmt.Fprint(w, `{"genres":[{"id":28,"name":"Action"}]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = c.Genres(leaderCtx, "movie")
	}()
	for hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	waiter := make(chan error, 1)
	go func() {
		_, err := c.Genres(context.Background(), "movie")
		waiter <- err
	}()
	time.Sleep(30 * time.Millisecond) // let the waiter join the flight
	cancel()
	<-leaderDone
	select {
	case err := <-waiter:
		if err != nil {
			t.Fatalf("waiter failed with leader's cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter hung")
	}
}

func TestInvalidJSONDoesNotPoisonResponseCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			fmt.Fprint(w, `<html>temporary gateway error</html>`)
			return
		}
		fmt.Fprint(w, `{"genres":[{"id":28,"name":"Action"}]}`)
	}))
	defer srv.Close()
	c := New("fixture-key", nil, WithBaseURL(srv.URL), WithMaxRetries(0))
	defer c.Close()
	if _, err := c.Genres(context.Background(), "movie"); err == nil {
		t.Fatal("invalid body accepted")
	}
	genres, err := c.Genres(context.Background(), "movie")
	if err != nil || len(genres) != 1 || genres[0].Name != "Action" || hits.Load() != 2 {
		t.Fatalf("bad response poisoned cache: genres=%+v err=%v hits=%d", genres, err, hits.Load())
	}
}
