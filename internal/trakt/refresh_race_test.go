package trakt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// Two requests rejected with 401 at once must share one refresh: Trakt
// refresh tokens are single-use, and a second refresh spending the same
// token is rejected, which used to disconnect the viewer.
func TestConcurrent401sShareOneRefresh(t *testing.T) {
	var refreshes atomic.Int32
	var both sync.WaitGroup
	both.Add(2)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		refreshes.Add(1)
		// Hold the first refresh briefly so a concurrent second one (the
		// bug) reads the same refresh token before it is rotated.
		time.Sleep(100 * time.Millisecond)
		if body["refresh_token"] != "ref1" || refreshes.Load() > 1 {
			w.WriteHeader(http.StatusBadRequest) // invalid_grant: already used
			return
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "acc2", RefreshToken: "ref2", ExpiresIn: 3600})
	})
	mux.HandleFunc("/sync/watchlist", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer acc1" {
			both.Done()
			both.Wait() // both callers hold the stale token before either refreshes
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("[]"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := New(Options{
		Config: config.Trakt{Enabled: true, ClientID: "cid", ClientSecret: "sec"},
		DB:     db, BaseURL: srv.URL, MaxRetryAfter: time.Second, MinPostInterval: -1, Logger: quietLog(),
	})
	connect(t, c, 1)

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, err := c.authed(ctx, 1, http.MethodGet, "/sync/watchlist", nil)
			if err == nil && status != http.StatusOK {
				err = &statusErr{status}
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want 1", n)
	}
	if !c.Connected(ctx, 1) {
		t.Fatal("viewer was disconnected by a duplicate refresh")
	}
}

type statusErr struct{ status int }

func (e *statusErr) Error() string { return http.StatusText(e.status) }
