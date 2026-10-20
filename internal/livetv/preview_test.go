package livetv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestOwnerPreviewPlaysThroughTheOwnerAPI(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live/master.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000\nvariant/720.m3u8\n")
		case "/live/variant/720.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg1.ts\n")
		case "/live/variant/seg1.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			fmt.Fprint(w, "TSDATA")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	tc := &compatibilityTranscoder{info: core.MediaInfo{Container: "hls", Duration: time.Hour, Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "h264", Width: 1280, Height: 720},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Default: true},
	}}}
	s := testService(t, Options{HTTPClient: up.Client(), Transcoder: tc})
	c, err := s.SaveChannel(context.Background(), Channel{Name: "News", Number: "1", Kind: "iptv", Enabled: true, StreamURL: up.URL + "/live/master.m3u8"})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	var p PreviewSession
	if code := ownerJSON(t, s, "POST", "/channels/"+c.ID+"/play", "", &p); code != 200 {
		t.Fatalf("play: %d", code)
	}
	if p.ID == "" || p.Playlist != "sessions/"+p.ID+"/index.m3u8" || p.ActiveSourceName == "" {
		t.Fatalf("preview session: %+v", p)
	}
	// Mounted like the admin API: nested URIs must land back under this mount.
	master := httptest.NewRecorder()
	h := http.StripPrefix("/ether/api/livetv", s.Handler())
	h.ServeHTTP(master, httptest.NewRequest("GET", "/ether/api/livetv/"+p.Playlist, nil))
	body := master.Body.String()
	if master.Code != 200 || strings.Contains(body, "/0/") || strings.Contains(body, up.URL) {
		t.Fatalf("master playlist: %d %s", master.Code, body)
	}
	variant := ""
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "/ether/api/livetv/sessions/"+p.ID+"/resource/") {
			variant = strings.TrimPrefix(line, "/ether/api/livetv")
		}
	}
	if variant == "" {
		t.Fatalf("variant not rewritten onto the owner API: %s", body)
	}
	vrec := get(variant)
	if vrec.Code != 200 || !strings.Contains(vrec.Body.String(), "/sessions/"+p.ID+"/resource/") {
		t.Fatalf("variant playlist: %d %s", vrec.Code, vrec.Body.String())
	}
	segment := ""
	for _, line := range strings.Split(vrec.Body.String(), "\n") {
		if strings.Contains(line, "/resource/") {
			segment = line
		}
	}
	if seg := get(segment); seg.Code != 200 || seg.Body.String() != "TSDATA" {
		t.Fatalf("segment: %d %q", seg.Code, seg.Body.String())
	}

	if code := ownerJSON(t, s, "DELETE", "/sessions/"+p.ID, "", nil); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	if rec := get("/" + p.Playlist); rec.Code == 200 {
		t.Fatal("stopped preview still serves its playlist")
	}
}
