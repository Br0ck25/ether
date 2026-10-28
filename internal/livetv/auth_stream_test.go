package livetv

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// authLiveProbeFixture reads its input the way ffprobe does: a bare URL,
// with no provider headers.
type authLiveProbeFixture struct {
	fixtureLiveProber
	input, body string
}

func (p *authLiveProbeFixture) ProbeLive(ctx context.Context, input string) (*core.MediaInfo, error) {
	p.input = input
	if res, err := http.Get(input); err == nil {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		p.body = string(b)
	}
	return p.fixtureLiveProber.ProbeLive(ctx, input)
}

func (p *authLiveProbeFixture) Decide(core.MediaInfo, core.ClientCaps, core.DecideOptions) core.Decision {
	return core.Decision{VideoCopy: true, AudioCopy: true}
}

func TestProviderAuthReachesProbeKeyMediaButNotAnonymousBackup(t *testing.T) {
	var failed atomic.Bool
	var mediaRequests atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "" {
			t.Error("viewer token forwarded to IPTV provider")
		}
		if r.URL.Path == "/backup.m3u8" {
			if r.Header.Get("Authorization") != "" {
				t.Error("anonymous backup inherited primary credentials")
			}
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nbackup.ts\n")
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			http.Error(w, "auth needed", 401)
			return
		}
		if failed.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		switch r.URL.Path {
		case "/primary.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:4,\nmedia.ts\n")
		case "/key":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "0123456789abcdef")
		case "/media.ts":
			mediaRequests.Add(1)
			fmt.Fprint(w, "media fixture")
		}
	}))
	defer up.Close()
	p := &authLiveProbeFixture{}
	s := testService(t, Options{Transcoder: p, HTTPClient: up.Client()})
	src := Source{ID: "protected", Name: "Protected", Auth: &SourceAuth{Type: "bearer", Token: "fixture-secret"}, Playlist: fmt.Sprintf("#EXTM3U\n#EXTINF:-1 tvg-id=\"fixture\",Fixture\n%s/primary.m3u8\n", up.URL)}
	if _, err := s.ImportSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	channels, _ := s.Channels(context.Background())
	c := channels[0]
	c.StreamSources = append(c.StreamSources, StreamSource{ID: "anonymous", URL: up.URL + "/backup.m3u8", Enabled: true, Priority: 1})
	if _, err := s.SaveChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	first, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{Product: "Plex for Apple TV"})
	if err != nil || !strings.HasPrefix(p.input, "http://127.0.0.1:") || strings.Contains(p.input, up.URL) || strings.Contains(p.input, "fixture-secret") {
		t.Fatalf("live probe did not read through the relay: %q %v", p.input, err)
	}
	if !strings.Contains(p.body, "#EXT-X-KEY") || strings.Contains(p.body, up.URL) || strings.Contains(p.body, "fixture-secret") {
		t.Fatalf("relayed playlist was not authenticated and rewritten: %q", p.body)
	}
	req := httptest.NewRequest("GET", "/?X-Plex-Token=viewer-only", nil)
	req.Header.Set("X-Plex-Token", "viewer-only")
	playlist := httptest.NewRecorder()
	s.ServePlaylist(playlist, req, first.ID, u)
	for _, path := range []string{"/key", "/media.ts"} {
		out := httptest.NewRecorder()
		s.ServeResource(out, req, first.ID, stableID(up.URL+path), u)
		if out.Code != 200 {
			t.Fatalf("authenticated %s failed: %d", path, out.Code)
		}
	}
	if mediaRequests.Load() != 1 {
		t.Fatal("media did not reach the authenticated provider")
	}
	s.Stop(context.Background(), first.ID, u)
	failed.Store(true)
	second, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil || second.ActiveSourceID != "anonymous" {
		t.Fatalf("anonymous backup could not be tuned: %v", err)
	}
	s.Stop(context.Background(), second.ID, u)
}
