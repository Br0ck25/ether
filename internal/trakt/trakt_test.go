package trakt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/state"
	"github.com/ether-media/ether/internal/store"
)

type fakeTrakt struct {
	mu        sync.Mutex
	polls     int
	scrobbles []map[string]any
	history   []map[string]any
	removed   []map[string]any
	ratings   []map[string]any
	rateLimit bool
	hits      map[string]int
	// deviceSeq, when set, is the sequence of device-token statuses to
	// return (200 issues tokens).
	deviceSeq     []int
	refreshStatus int    // non-zero: /oauth/token responds with it
	rejectToken   string // Bearer token answered with 401
}

func (f *fakeTrakt) hit(path string) {
	f.mu.Lock()
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[path]++
	f.mu.Unlock()
}

func (f *fakeTrakt) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeTrakt) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	inner := mux
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hit(r.URL.Path)
		if r.Header.Get("trakt-api-version") != "2" {
			http.Error(w, "missing api version", 400)
			return
		}
		f.mu.Lock()
		reject := f.rejectToken
		f.mu.Unlock()
		if reject != "" && r.Header.Get("Authorization") == "Bearer "+reject {
			w.WriteHeader(401)
			return
		}
		inner.ServeHTTP(w, r)
	})
	mux.HandleFunc("/oauth/device/code", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("trakt-api-key") != "cid" {
			http.Error(w, "bad key", 403)
			return
		}
		json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dev123", UserCode: "ABCD", VerificationURL: "https://trakt.tv/activate", ExpiresIn: 600, Interval: 1})
	})
	mux.HandleFunc("/oauth/device/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.polls++
		n := f.polls
		seq := f.deviceSeq
		f.mu.Unlock()
		if seq != nil {
			code := seq[min(n-1, len(seq)-1)]
			if code != 200 {
				w.WriteHeader(code)
				return
			}
		} else if n < 2 {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "acc1", RefreshToken: "ref1", ExpiresIn: 3600, CreatedAt: time.Now().Unix()})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		rs := f.refreshStatus
		f.mu.Unlock()
		if rs != 0 {
			w.WriteHeader(rs)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["refresh_token"] == "" {
			http.Error(w, "bad refresh", 400)
			return
		}
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "acc2", RefreshToken: "ref2", ExpiresIn: 3600})
	})
	mux.HandleFunc("/users/me", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"username": "etherfan"})
	})
	mux.HandleFunc("/scrobbling/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["_action"] = strings.TrimPrefix(r.URL.Path, "/scrobbling/")
		f.mu.Lock()
		f.scrobbles = append(f.scrobbles, body)
		f.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"action": "scrobble"})
	})
	mux.HandleFunc("/sync/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.history = append(f.history, body)
			f.mu.Unlock()
			w.WriteHeader(201)
			return
		}
		w.WriteHeader(405)
	})
	record := func(dst *[]map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			*dst = append(*dst, body)
			f.mu.Unlock()
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		}
	}
	mux.HandleFunc("/sync/history/remove", record(&f.removed))
	mux.HandleFunc("/sync/ratings", record(&f.ratings))
	mux.HandleFunc("/sync/ratings/remove", record(&f.removed))
	mux.HandleFunc("/sync/history/movies", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]historyMovie{{WatchedAt: time.Now(), Movie: traktTitle{Title: "Heat", IDs: traktIDs{TMDB: 949}}}})
	})
	mux.HandleFunc("/sync/history/episodes", func(w http.ResponseWriter, r *http.Request) {
		io := `[{"watched_at":"2026-01-01T00:00:00Z","show":{"title":"Severance","ids":{"tmdb":95396}},"episode":{"season":1,"number":2,"ids":{}}}]`
		w.Write([]byte(io))
	})
	mux.HandleFunc("/recommendations/movies", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		rl := f.rateLimit
		if rl {
			f.rateLimit = false
		}
		f.mu.Unlock()
		if rl {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		json.NewEncoder(w).Encode([]traktTitle{{Title: "Heat", Year: 1995, IDs: traktIDs{TMDB: 949}}})
	})
	mux.HandleFunc("/recommendations/shows", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]traktTitle{{Title: "Severance", Year: 2022, IDs: traktIDs{TMDB: 95396}}})
	})
	mux.HandleFunc("/sync/watchlist", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]watchlistItem{{Type: "movie", Movie: &traktTitle{Title: "Dune", IDs: traktIDs{TMDB: 438631}}}})
	})
	return wrapped
}

func newTestClient(t *testing.T, f *fakeTrakt) *Client {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return New(Options{
		Config: config.Trakt{Enabled: true, ClientID: "cid", ClientSecret: "sec"},
		DB:     db, BaseURL: srv.URL, MaxRetryAfter: time.Second, MinPostInterval: -1,
	})
}

func TestDeviceFlowAndScrobble(t *testing.T) {
	f := &fakeTrakt{rateLimit: true}
	c := newTestClient(t, f)
	ctx := context.Background()

	dc, err := c.StartDevice(ctx)
	if err != nil || dc.UserCode != "ABCD" {
		t.Fatalf("device: %+v %v", dc, err)
	}
	st, err := c.PollDevice(ctx, 1, dc.DeviceCode)
	if err != nil || st != PollPending {
		t.Fatalf("poll1: %v %v", st, err)
	}
	st, err = c.PollDevice(ctx, 1, dc.DeviceCode)
	if err != nil || st != PollAuthorized {
		t.Fatalf("poll2: %v %v", st, err)
	}
	toks, ok, _ := c.Tokens(ctx, 1)
	if !ok || toks.AccessToken != "acc1" || toks.Username != "etherfan" {
		t.Fatalf("tokens: %+v %v", toks, ok)
	}
	if !c.Connected(ctx, 1) || c.Connected(ctx, 2) {
		t.Fatal("Connected wrong")
	}

	// Scrobble + history push through events.
	key := core.EpisodeKey(95396, 1, 2)
	c.HandleEvent(ctx, state.Event{Kind: state.EventProgress, UserID: 1, Key: key, Fraction: 0.5, State: "playing", At: time.Now()})
	c.HandleEvent(ctx, state.Event{Kind: state.EventScrobble, UserID: 1, Key: key, At: time.Now(), Completed: true})
	f.mu.Lock()
	scrobbles, history := f.scrobbles, f.history
	f.mu.Unlock()
	if len(scrobbles) != 1 || scrobbles[0]["_action"] != "start" || scrobbles[0]["progress"] != 50.0 {
		t.Fatalf("scrobbles: %+v", scrobbles)
	}
	if ep, ok := scrobbles[0]["episode"].(map[string]any); !ok || ep["season"] != 1.0 {
		t.Fatalf("episode payload: %+v", scrobbles[0])
	}
	if len(history) != 1 {
		t.Fatalf("history pushes: %+v", history)
	}

	// Pull + recommendations (first rec call hits the 429 retry).
	hist, err := c.PullHistory(ctx, 1, 10)
	if err != nil || len(hist) != 2 || hist[0].Key != core.MovieKey(949) || hist[1].Key != core.EpisodeKey(95396, 1, 2) {
		t.Fatalf("pull history: %+v %v", hist, err)
	}
	recs, err := c.Recommendations(ctx, 1, 10)
	if err != nil || len(recs) != 2 {
		t.Fatalf("recs: %+v %v", recs, err)
	}
	wl, err := c.Watchlist(ctx, 1, 10)
	if err != nil || len(wl) != 1 || wl[0].Title != "Dune" {
		t.Fatalf("watchlist: %+v %v", wl, err)
	}

	// Expired token refreshes transparently on next call.
	_ = c.saveTokens(ctx, Tokens{UserID: 1, AccessToken: "old", RefreshToken: "refX", ExpiresAt: time.Now().Add(-time.Hour)})
	c.invalidateUser(1) // bypass the pull cache so a request is made
	if _, err := c.Watchlist(ctx, 1, 10); err != nil {
		t.Fatal(err)
	}
	toks, _, _ = c.Tokens(ctx, 1)
	if toks.AccessToken != "acc2" {
		t.Fatalf("after refresh: %+v", toks)
	}

	if err := c.Disconnect(ctx, 1); err != nil || c.Connected(ctx, 1) {
		t.Fatalf("disconnect: %v", err)
	}
}

func TestDisabledIsInert(t *testing.T) {
	f := &fakeTrakt{}
	c := newTestClient(t, f)
	c.UpdateConfig(config.Trakt{Enabled: false})
	if _, err := c.StartDevice(context.Background()); err == nil {
		t.Fatal("disabled StartDevice should error")
	}
	// Events for unconnected users are silent no-ops.
	c.HandleEvent(context.Background(), state.Event{Kind: state.EventScrobble, UserID: 9, Key: core.MovieKey(1)})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.history) != 0 || len(f.scrobbles) != 0 {
		t.Fatal("disabled client made calls")
	}
}
