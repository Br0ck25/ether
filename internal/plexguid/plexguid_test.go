package plexguid

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExactIdentityRequiredAndCached(t *testing.T) {
	for _, tc := range []struct{ name, ids, want string }{{"exact", `[{"id":"tmdb://550"}]`, "plex://movie/5d7768265af944001f1f6976"}, {"mismatch", `[{"id":"tmdb://551"}]`, ""}, {"absent", `[]`, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, e := store.OpenMemory(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("guid") != "tmdb://550" || r.Header.Get("X-Plex-Token") != "fixture" {
					t.Fatal("request identity")
				}
				w.Write([]byte(`{"MediaContainer":{"Metadata":[{"guid":"plex://movie/5d7768265af944001f1f6976","type":"movie","Guid":` + tc.ids + `}]}}`))
			}))
			defer srv.Close()
			s := New(db, Options{BaseURL: srv.URL, Token: func() string { return "fixture" }})
			for i := 0; i < 2; i++ {
				guid, e := s.Resolve(ctx, core.MovieKey(550))
				if e != nil || guid != tc.want {
					t.Fatal(guid, e)
				}
			}
			if calls != 1 || s.Cached(ctx, core.MovieKey(550)) != tc.want {
				t.Fatal("cache", calls)
			}
		})
	}
}
func TestTokenNotForwardedOnRedirect(t *testing.T) {
	ctx := context.Background()
	db, _ := store.OpenMemory(ctx)
	defer db.Close()
	calls := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer srv.Close()
	s := New(db, Options{BaseURL: srv.URL, Token: func() string { return "fixture" }})
	if _, e := s.Resolve(ctx, core.MovieKey(550)); e == nil || calls != 0 {
		t.Fatal("redirect followed", calls, e)
	}
}
