package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// fakeDASH is a DASH session that echoes what was asked of it.
type fakeDASH struct{ fakeHLS }

func (d *fakeDASH) ServeManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/dash+xml")
	fmt.Fprintf(w, "<MPD session=%q/>", d.id)
}
func (d *fakeDASH) ServeInit(w http.ResponseWriter, r *http.Request, rep int) {
	fmt.Fprintf(w, "init-%d", rep)
}
func (d *fakeDASH) ServeMedia(w http.ResponseWriter, r *http.Request, rep, n int) {
	fmt.Fprintf(w, "media-%d-%d", rep, n)
}

var _ core.DASHSession = (*fakeDASH)(nil)

// dashTranscoder starts fakeDASH sessions for DASH requests and defers to
// the shared fake for everything else.
type dashTranscoder struct {
	*fakeTranscoder
	mu      sync.Mutex
	dash    map[string]*fakeDASH
	stopped []string
}

func (t *dashTranscoder) StartHLS(ctx context.Context, req core.HLSRequest) (core.HLSSession, error) {
	if req.Protocol != core.ProtocolDASH {
		return t.fakeTranscoder.StartHLS(ctx, req)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d := &fakeDASH{fakeHLS{id: req.SessionID, req: req}}
	t.dash[req.SessionID] = d
	return d, nil
}

func (t *dashTranscoder) Session(id string) (core.HLSSession, bool) {
	t.mu.Lock()
	d, ok := t.dash[id]
	t.mu.Unlock()
	if ok {
		return d, true
	}
	return t.fakeTranscoder.Session(id)
}

func (t *dashTranscoder) Stop(id string) {
	t.mu.Lock()
	if _, ok := t.dash[id]; ok {
		t.stopped = append(t.stopped, id)
		delete(t.dash, id)
	}
	t.mu.Unlock()
	t.fakeTranscoder.Stop(id)
}

func (t *dashTranscoder) session(id string) *fakeDASH {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dash[id]
}

func dashFixture() (*fixture, *dashTranscoder) {
	f := newFixture()
	f.trans.decision = core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: false, AudioCodec: "aac", Container: "mpegts"}
	dt := &dashTranscoder{fakeTranscoder: f.trans, dash: map[string]*fakeDASH{}}
	f.srv.d.Transcoder = dt
	return f, dt
}

const dashQuery = "path=%2Flibrary%2Fmetadata%2F1000000000000550&protocol=dash&directPlay=0&directStream=1&session=web-1"

func TestDASHDecisionMedia(t *testing.T) {
	f, _ := dashFixture()
	defer f.srv.Close()
	rec := f.get(t, "/video/:/transcode/universal/decision?"+dashQuery, jsonAccept...)
	if rec.Code != 200 {
		t.Fatalf("decision: %d %s", rec.Code, rec.Body.String())
	}
	var v struct {
		MediaContainer struct {
			Metadata []struct {
				Media []struct {
					Container string `json:"container"`
					Protocol  string `json:"protocol"`
					Part      []struct {
						Container string `json:"container"`
						Protocol  string `json:"protocol"`
						Decision  string `json:"decision"`
						Stream    []struct {
							StreamType int    `json:"streamType"`
							Location   string `json:"location"`
							Decision   string `json:"decision"`
						} `json:"Stream"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	m := v.MediaContainer.Metadata[0].Media[0]
	if m.Container != "mp4" || m.Protocol != "dash" || m.Part[0].Container != "mp4" || m.Part[0].Protocol != "dash" ||
		m.Part[0].Decision != "transcode" {
		t.Fatalf("media %+v", m)
	}
	locations := map[int]string{}
	for _, st := range m.Part[0].Stream {
		locations[st.StreamType] = st.Location
	}
	if locations[1] != "segments-video" || locations[2] != "segments-audio" {
		t.Errorf("stream locations %v", locations)
	}
}

func TestStartDASH(t *testing.T) {
	f, dt := dashFixture()
	defer f.srv.Close()
	rec := f.get(t, "/video/:/transcode/universal/start.mpd?"+dashQuery+"&offset=62.5", "X-Plex-Product", "Plex Web", "X-Plex-Platform", "Chrome")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/dash+xml" || rec.Body.String() != `<MPD session="web-1"/>` {
		t.Fatalf("start.mpd: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	d := dt.session("web-1")
	if d == nil {
		t.Fatal("no DASH session started")
	}
	if d.req.Protocol != core.ProtocolDASH || d.req.StartOffset != 62500*time.Millisecond ||
		!strings.Contains(d.req.InputURL, "ps-1-client-1") || d.req.Decision.Kind != core.DecisionRemux {
		t.Fatalf("request %+v", d.req)
	}
	for path, want := range map[string]string{
		"/video/:/transcode/universal/session/web-1/0/header": "init-0",
		"/video/:/transcode/universal/session/web-1/1/header": "init-1",
		"/video/:/transcode/universal/session/web-1/0/0.m4s":  "media-0-0",
		"/video/:/transcode/universal/session/web-1/1/42.m4s": "media-1-42",
	} {
		if rec := f.get(t, path); rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("%s: %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	for _, path := range []string{
		"/video/:/transcode/universal/session/web-1/0/abc.m4s",
		"/video/:/transcode/universal/session/web-1/0/12.ts",
		"/video/:/transcode/universal/session/web-1/0/-1.m4s",
		"/video/:/transcode/universal/session/nope/0/header",
		"/video/:/transcode/universal/session/nope/0/3.m4s",
	} {
		if rec := f.get(t, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}

	// A repeated start (Plex Web after a seek) keeps the session: the MPD
	// covers the whole title.
	f.get(t, "/video/:/transcode/universal/start.mpd?"+dashQuery+"&offset=600")
	if len(dt.stopped) != 0 || dt.session("web-1") != d {
		t.Fatalf("session restarted: %v", dt.stopped)
	}
	// A new decision request means new settings: the next start restarts.
	f.get(t, "/video/:/transcode/universal/decision?"+dashQuery)
	f.get(t, "/video/:/transcode/universal/start.mpd?"+dashQuery+"&offset=600")
	if len(dt.stopped) != 1 || dt.session("web-1") == d || dt.session("web-1").req.StartOffset != 600*time.Second {
		t.Fatalf("restart after decision: %v", dt.stopped)
	}
	f.get(t, "/video/:/transcode/universal/stop?session=web-1")
	if dt.session("web-1") != nil {
		t.Fatal("not stopped")
	}
}

func TestDASHRoutesLeaveHLSAlone(t *testing.T) {
	f, _ := dashFixture()
	defer f.srv.Close()
	rec := f.get(t, "/video/:/transcode/universal/start.m3u8?path=%2Flibrary%2Fmetadata%2F1000000000000550&session=hls-1&protocol=hls")
	if rec.Code != 200 {
		t.Fatalf("start.m3u8: %d", rec.Code)
	}
	if rec := f.get(t, "/video/:/transcode/universal/session/hls-1/base/00042.ts"); rec.Body.String() != "segment-42" {
		t.Errorf("HLS segment: %d %q", rec.Code, rec.Body.String())
	}
	if rec := f.get(t, "/video/:/transcode/universal/session/hls-1/base/index.m3u8"); !strings.Contains(rec.Body.String(), "#EXTM3U") {
		t.Errorf("HLS playlist: %d %q", rec.Code, rec.Body.String())
	}
	// DASH URLs on an HLS session are not found.
	if rec := f.get(t, "/video/:/transcode/universal/session/hls-1/0/header"); rec.Code != http.StatusNotFound {
		t.Errorf("DASH init on HLS session: %d", rec.Code)
	}
}

func TestStartDASHFailures(t *testing.T) {
	f, _ := dashFixture()
	defer f.srv.Close()
	if rec := f.get(t, "/video/:/transcode/universal/start.mpd?protocol=dash"); rec.Code != http.StatusBadRequest {
		t.Errorf("no item: %d", rec.Code)
	}
	f.playback.failures[fightClub.Key] = core.ErrNotAvailable
	// Not available is temporary, not "this item is gone": 503 + Retry-After.
	if rec := f.get(t, "/video/:/transcode/universal/start.mpd?"+dashQuery); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("not available: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// The plain shared fake returns HLS sessions: refused cleanly.
	f2 := newFixture()
	defer f2.srv.Close()
	if rec := f2.get(t, "/video/:/transcode/universal/start.mpd?"+dashQuery); rec.Code != http.StatusInternalServerError {
		t.Errorf("non-DASH transcoder: %d", rec.Code)
	}
	if len(f2.trans.stopped) != 1 {
		t.Errorf("non-DASH session not stopped: %v", f2.trans.stopped)
	}
}
