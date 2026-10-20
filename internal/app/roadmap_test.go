package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
	"github.com/braedonsaunders/ether/internal/safety"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoadmapOwnerAPIsAreWiredAndPrivate(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Update(func(c *config.Config) error { c.Transcode.FFmpegPath = "/not-installed/ffmpeg"; return nil }); err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), Options{DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	paths := []string{"/stats/summary", "/notifications/", "/livetv/channels", "/adoption/libraries/", "/adoption/backups/", "/safety/invites/", "/stream-preferences/0", "/safety/policies/1", "/discovery/settings", "/discovery/overlays/settings", "/discovery/ratings/settings", "/subtitle-sync/", "/playback/policy", "/status", "/releases/profiles", "/releases/pins", "/releases/blocklist", "/releases/history", "/cluster/"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://localhost/ether/api"+path, nil)
			r.RemoteAddr = "127.0.0.1:1024"
			w := httptest.NewRecorder()
			a.handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Errorf("status %d: %s", w.Code, w.Body.String())
			}
			r = httptest.NewRequest("GET", "http://ether.example/ether/api"+path, nil)
			r.RemoteAddr = "192.0.2.10:1024"
			w = httptest.NewRecorder()
			a.handler.ServeHTTP(w, r)
			if w.Code != 401 && w.Code != 403 {
				t.Errorf("unauthenticated status %d", w.Code)
			}
		})
	}
	t.Run("passthrough IPTV status and owner termination", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
		}))
		defer upstream.Close()
		channel, err := a.livetv.SaveChannel(context.Background(), livetv.Channel{ID: "fixture-channel", Number: "1", Name: "Fixture channel", Kind: "iptv", StreamURL: upstream.URL + "/live.m3u8", Rating: "PG", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		info, err := a.livetv.Tune(context.Background(), &core.User{ID: 1, Username: "fixture-viewer", StreamLimit: 1}, channel.ID, core.ClientCaps{ClientID: "fixture-client", Platform: "Fixture"})
		if err != nil {
			t.Fatal(err)
		}
		sessions := (ownerPlayback{Playback: a.playback, app: a}).Sessions()
		if len(sessions) != 1 || sessions[0].ID != info.ID || sessions[0].Username != "fixture-viewer" {
			t.Fatalf("viewing status=%+v", sessions)
		}
		r := httptest.NewRequest("POST", "http://localhost/ether/api/stats/sessions/"+info.ID+"/stop", strings.NewReader("{}"))
		r.RemoteAddr = "127.0.0.1:1024"
		r.Header.Set("X-Ether-Request", "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, r)
		if w.Code != 200 || len(a.livetv.ViewingSessions()) != 0 {
			t.Fatalf("stop=%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("invitation defaults preserve viewer restrictions", func(t *testing.T) {
		if err := a.safety.Put(context.Background(), 300, safety.Policy{MaxContentRating: "PG", StreamLimit: 1, BlockedGenres: []string{"Horror"}}); err != nil {
			t.Fatal(err)
		}
		u := &core.User{ID: 300, MaxContentRating: "G", StreamLimit: -1, AllowedSections: []int{1}}
		merged := a.invitedUser(u, []int{1, 2})
		if merged.MaxContentRating != "G" || merged.StreamLimit != 1 || len(merged.AllowedSections) != 1 || merged.AllowedSections[0] != 1 {
			t.Fatalf("restrictions widened: %+v", merged)
		}
		denied := a.invitedUser(u, []int{2})
		if len(denied.AllowedSections) != 1 || denied.AllowedSections[0] != 0 {
			t.Fatalf("disjoint sections became unrestricted: %+v", denied)
		}
		if a.safety.Allowed(merged, &core.Title{ContentRating: "G", Genres: []string{"Horror"}}) {
			t.Fatal("existing genre ban lost")
		}
	})

}
