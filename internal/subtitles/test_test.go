package subtitles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestSettingsCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/infos/formats":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "sam" || body["password"] != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	run := func(c config.OpenSubtitles) error { return test(ctx, srv.Client(), srv.URL, c) }
	if err := run(config.OpenSubtitles{APIKey: "key"}); err != nil {
		t.Fatalf("API key only: %v", err)
	}
	if err := run(config.OpenSubtitles{APIKey: "key", Username: "sam", Password: "pw"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := run(config.OpenSubtitles{APIKey: "key", Username: "sam", Password: "no"}); err == nil || !strings.Contains(err.Error(), "username or password") {
		t.Fatalf("bad password: %v", err)
	}
	if err := run(config.OpenSubtitles{APIKey: "wrong"}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("bad key: %v", err)
	}
	if err := run(config.OpenSubtitles{}); err == nil {
		t.Fatal("missing API key accepted")
	}
}
