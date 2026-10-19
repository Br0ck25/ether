package trakt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestCredentialsCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movies/trending" || r.Header.Get("trakt-api-version") != "2" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		if r.Header.Get("trakt-api-key") != "good" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := test(ctx, srv.Client(), srv.URL, config.Trakt{ClientID: "good", ClientSecret: "s"}); err != nil {
		t.Fatalf("valid credentials: %v", err)
	}
	if err := test(ctx, srv.Client(), srv.URL, config.Trakt{ClientID: "bad", ClientSecret: "s"}); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("bad client ID: %v", err)
	}
	if err := test(ctx, srv.Client(), srv.URL, config.Trakt{ClientSecret: "s"}); err == nil {
		t.Fatal("missing client ID accepted")
	}
	if err := test(ctx, srv.Client(), srv.URL, config.Trakt{ClientID: "good"}); err == nil {
		t.Fatal("missing client secret accepted")
	}
}
