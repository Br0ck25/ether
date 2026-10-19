package plex

import (
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestStartHLSLadderMasterAndRungs(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.trans.decision = core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true, Container: "mpegts"}
	f.trans.rungs = []core.HLSRung{
		{ID: "base", Bandwidth: 9120000, AverageBandwidth: 7600000, Width: 1920, Height: 1080, Codecs: "avc1.640028,mp4a.40.2", FrameRate: 23.976},
		{ID: "720p4m", Bandwidth: 4800000, AverageBandwidth: 4000000, Width: 1280, Height: 720, Codecs: "avc1.64001f,mp4a.40.2", FrameRate: 23.976},
		{ID: "480p1500k", Bandwidth: 1800000, AverageBandwidth: 1500000, Width: 854, Height: 480, Codecs: "avc1.64001e,mp4a.40.2"},
	}
	rec := f.get(t, "/video/:/transcode/universal/start.m3u8?path=%2Flibrary%2Fmetadata%2F1000000000000550&session=lad-1&protocol=hls&directStream=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=9120000,AVERAGE-BANDWIDTH=7600000,RESOLUTION=1920x1080,CODECS=\"avc1.640028,mp4a.40.2\",FRAME-RATE=23.976\nsession/lad-1/base/index.m3u8",
		"BANDWIDTH=4800000,AVERAGE-BANDWIDTH=4000000,RESOLUTION=1280x720",
		"session/lad-1/720p4m/index.m3u8",
		"session/lad-1/480p1500k/index.m3u8",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("master missing %q\n%s", want, body)
		}
	}
	if n := strings.Count(body, "#EXT-X-STREAM-INF"); n != 3 {
		t.Errorf("variants = %d", n)
	}
	// Rung playlist and segment routes reach the rung.
	rec = f.get(t, "/video/:/transcode/universal/session/lad-1/720p4m/index.m3u8")
	if !strings.Contains(rec.Body.String(), "# rung 720p4m") {
		t.Errorf("rung playlist: %s", rec.Body.String())
	}
	rec = f.get(t, "/video/:/transcode/universal/session/lad-1/720p4m/00042.ts")
	if rec.Body.String() != "segment-720p4m-42" {
		t.Errorf("rung segment: %s", rec.Body.String())
	}
	// The base URI still works through the ladder.
	rec = f.get(t, "/video/:/transcode/universal/session/lad-1/base/00007.ts")
	if rec.Body.String() != "segment-base-7" {
		t.Errorf("base segment: %s", rec.Body.String())
	}
	// Unknown rungs 404 via the legacy path not finding a segment.
	if rec := f.get(t, "/video/:/transcode/universal/session/lad-1/999p/index.m3u8"); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "# rung") {
		t.Errorf("unknown rung served: %s", rec.Body.String())
	}
}
