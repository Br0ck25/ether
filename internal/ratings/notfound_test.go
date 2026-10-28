package ratings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// A title the provider does not have is cached as "no ratings" for the
// cache interval: the background worker must not spend the owner's
// metered API quota asking again every retry. Key and quota errors are
// still errors (retried later).
func TestProviderMissCachedAsNoRatings(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		status         int
		body           string
		miss           bool
	}{
		{"omdb not found", "omdb", 200, `{"Response":"False","Error":"Movie not found!"}`, true},
		{"omdb bad id", "omdb", 200, `{"Response":"False","Error":"Incorrect IMDb ID."}`, true},
		{"omdb quota", "omdb", 200, `{"Response":"False","Error":"Request limit reached!"}`, false},
		{"omdb key", "omdb", 401, `{"Response":"False","Error":"Invalid API key!"}`, false},
		{"mdblist unknown", "mdblist", 404, `{"error":"not found"}`, true},
		{"mdblist outage", "mdblist", 503, ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.OpenMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
			s := New(db, Options{OMDbBaseURL: srv.URL, MDBListBaseURL: srv.URL, Now: func() time.Time { return now }})
			if err := s.SaveSettings(ctx, Settings{Enabled: true, Provider: tc.provider, APIKey: "k", CacheHours: 24}); err != nil {
				t.Fatal(err)
			}
			title := &core.Title{Key: core.MovieKey(7), Type: core.TypeMovie, IDs: core.IDs{IMDB: "tt0000007", TMDB: 7}}
			bs, err := s.Refresh(ctx, title)
			if tc.miss {
				if err != nil || len(bs) != 0 {
					t.Fatalf("miss = %v, %v; want no ratings, no error", bs, err)
				}
				if _, err := s.Refresh(ctx, title); err != nil || calls != 1 {
					t.Fatalf("second lookup: calls = %d, err %v; want the miss cached", calls, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("provider error not reported: %v", bs)
			}
			if _, err := s.Refresh(ctx, title); err == nil || calls != 2 {
				t.Fatalf("transient error cached: calls = %d, err %v", calls, err)
			}
		})
	}
}
