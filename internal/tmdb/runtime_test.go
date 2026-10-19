package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestBundledKeyLinker(t *testing.T) {
	if expected := os.Getenv("ETHER_TEST_BUNDLED_KEY"); expected != "" && ResolveKey(config.Catalog{}) != expected {
		t.Fatal("release linker did not set the bundled key")
	}
}

func TestRuntimeCredentialsAndLocale(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		key := r.URL.Query().Get("api_key")
		if key == "old" {
			if r.URL.Query().Get("language") != "en-US" {
				t.Error("old locale")
			}
		} else if r.Header.Get("Authorization") != "Bearer new.bearer.key" || r.URL.Query().Get("language") != "fr-CA" || key != "" {
			t.Error("mixed or stale runtime credentials")
		}
		w.Write([]byte(`{"id":1,"title":"Movie"}`))
	}))
	defer srv.Close()
	c := testClient(t, srv, "old")
	if _, err := c.MovieDetails(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	c.UpdateConfig(config.Catalog{TMDBKey: "new.bearer.key", Language: "fr-CA", Region: "CA"})
	if _, err := c.MovieDetails(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || c.Region() != "CA" {
		t.Fatal("config update reused stale cache")
	}
}
