package plextv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCleanHomeUsesAccountAndOnlyAdvertisedPlexSources(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "owner-token" {
			t.Error("wrong token")
		}
		switch r.URL.Path {
		case "/api/v2/user":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"uuid":"owner-uuid","username":"owner"}`)
		case "/api/v2/user/owner-uuid/settings/opt_outs":
			if r.Method == "GET" {
				fmt.Fprint(w, `<MediaContainer><optOut key="tv.plex.provider.vod" value="opt_in"/><optOut key="tv.plex.provider.discover" value="opt_out"/><optOut key="other.feature" value="opt_in"/></MediaContainer>`)
			} else {
				posts++
				if r.URL.Query().Get("key") != "tv.plex.provider.vod" || r.URL.Query().Get("value") != "opt_out" {
					t.Error("unexpected opt out")
				}
			}
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := c.CleanHome(context.Background(), "owner-token")
	if err != nil {
		t.Fatal(err)
	}
	if posts != 1 || sources[0].Value != "opt_out" || sources[2].Value != "opt_in" {
		t.Fatalf("posts=%d sources=%+v", posts, sources)
	}
}
