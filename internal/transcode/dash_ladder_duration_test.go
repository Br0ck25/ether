package transcode

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestDASHCopyLadderUsesShortSharedGrid(t *testing.T) {
	fr := newFakeRunner()
	m := newTestManager(t, fr, config.Transcode{ABRLadder: true}, withRungDwell(0))
	s := startDASHSession(t, m, dashRequest(t, "short-ladder", 10*time.Minute))
	if !s.decision.VideoCopy || len(s.rungs) < 2 {
		t.Fatal("fixture must have a copy primary and encoded lower rungs")
	}
	rec := httptest.NewRecorder()
	s.ServeManifest(rec, httptest.NewRequest(http.MethodGet, "/start.mpd", nil))
	if body := rec.Body.String(); !strings.Contains(body, `minBufferTime="PT4S"`) || strings.Count(body, `duration="2"`) != 2 {
		t.Fatalf("video and audio must share the short grid: %s", body)
	}
	for _, n := range []int{0, 60, 5} {
		for _, rep := range []int{2, 1} {
			if got := media(t, s, rep, n); got.Code != http.StatusOK || got.Body.Len() == 0 {
				t.Fatalf("rep %d segment %d: %d", rep, n, got.Code)
			}
		}
	}
	mustContain(t, fr.lastStart(), "-force_key_frames expr:gte(t,n_forced*2.000)")
}

func TestDASHCopyLadderHonorsExplicitDuration(t *testing.T) {
	m := newTestManager(t, newFakeRunner(), config.Transcode{ABRLadder: true})
	req := dashRequest(t, "explicit-ladder", time.Minute)
	req.SegmentDuration = 3 * time.Second
	s := startDASHSession(t, m, req)
	if s.SegmentDuration() != req.SegmentDuration {
		t.Fatalf("explicit duration changed: %v", s.SegmentDuration())
	}
}

// Real muxer coverage matters: the copied primary can have GOPs longer than
// the grid, while encoded lower rungs must start on that same grid after seeks.
func TestIntegrationDASHShortLadderLongGOPAndSeeks(t *testing.T) {
	e := newRealEnv(t, WithoutHWDetection(), withRungDwell(0), WithSegmentTimeout(10*time.Second))
	e.m.UpdateConfig(config.Transcode{ABRLadder: true})
	e.gen(t, "ladder.mkv", 24, "-vf", "scale=1280:720", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-g", "240", "-sc_threshold", "0", "-c:a", "aac", "-ac", "2")
	info, err := e.m.Probe(context.Background(), e.url("ladder.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	d := e.m.Decide(*info, plexWeb, allow())
	if !d.VideoCopy {
		t.Fatalf("expected copy primary: %+v", d)
	}
	ds := startDASH(t, e.m, core.HLSRequest{SessionID: "short-grid", InputURL: e.url("ladder.mkv"), Info: *info, Decision: d})
	doc := dashManifest(t, ds)
	if len(doc.Period.Sets[0].Reps) < 2 || doc.Period.Sets[0].Template.Duration != 2 {
		t.Fatalf("missing short ladder: %+v", doc)
	}
	// Slot 3 lies inside the first ten-second GOP; it must remain decodable.
	if st, first := e.probeDASH(t, "copy.mp4", dashGet(t, ds, 0, 3)); st.CodecName != "h264" || first > 6 {
		t.Fatalf("copy alias: %+v at %v", st, first)
	}
	// Request a lower rendition far forward and then back. Both tracks retain
	// absolute times; independently decoded video starts with a keyframe.
	for _, n := range []int{9, 2} {
		_, vfirst := e.probeDASH(t, "lower.mp4", dashGet(t, ds, 2, n))
		_, afirst := e.probeDASH(t, "audio.mp4", dashGet(t, ds, 1, n))
		if math.Abs(vfirst-float64(2*n)) > 0.1 || math.Abs(afirst-vfirst) > 0.1 {
			t.Fatalf("segment %d starts video=%v audio=%v", n, vfirst, afirst)
		}
	}
	srv := httptest.NewServer(dashTestHandler(ds, "short-grid"))
	defer srv.Close()
	e.playMPD(t, srv.URL+"/start.mpd", 24)

	// Start cold inside a long GOP: no earlier run can hide copied-audio
	// preroll with already cached segments. Video keeps its decode keyframe,
	// but audio must begin at the requested media time on either rendition.
	cold := startDASH(t, e.m, core.HLSRequest{SessionID: "cold-seek", InputURL: e.url("ladder.mkv"), Info: *info, Decision: d, StartOffset: 18 * time.Second})
	_, afirst := e.probeDASH(t, "cold-audio.mp4", dashGet(t, cold, 1, 9))
	if math.Abs(afirst-18) > 0.1 {
		t.Fatalf("cold seek audio starts %v, want 18", afirst)
	}
	_, vfirst := e.probeDASH(t, "cold-video.mp4", dashGet(t, cold, 0, 9))
	if vfirst > 18 || vfirst < 9.9 {
		t.Fatalf("cold copy seek lost its decode keyframe: %v", vfirst)
	}
	_, lower := e.probeDASH(t, "cold-lower.mp4", dashGet(t, cold, 2, 9))
	if math.Abs(lower-afirst) > 0.1 {
		t.Fatalf("cold lower rendition starts video=%v audio=%v", lower, afirst)
	}
}

func TestSegmentedSeekDropsOnlyCopiedAudioPreroll(t *testing.T) {
	info := mustParse(t, "mp4_h264_aac.json")
	s := baseSpec(info, Decide(info, plexWeb, allow(), Policy{}))
	s.Protocol, s.Start = core.ProtocolDASH, 18*time.Second
	s.Decision.AudioCopy = true
	if args := strings.Join(buildArgs(s), " "); !strings.Contains(args, "-bsf:a noise=amount=0:drop='lt(pts*tb,18.000)'") || !strings.Contains(args, "-c:v copy") {
		t.Fatalf("copy seek must retain video keyframe and trim only audio: %s", args)
	}
	for _, container := range []string{ContainerMPEGTS, ContainerFMP4} {
		hls := s
		hls.Protocol, hls.Decision.Container = core.ProtocolHLS, container
		if !strings.Contains(strings.Join(buildArgs(hls), " "), "noise=amount=0:drop='lt(pts*tb,18.000)'") {
			t.Fatalf("%s HLS seek retained copied audio preroll", container)
		}
	}
	for _, kind := range []string{"initial", "encoded-audio"} {
		t.Run(kind, func(t *testing.T) {
			other := s
			switch kind {
			case "initial":
				other.Start = 0
			case "encoded-audio":
				other.Decision.AudioCopy = false
			}
			if strings.Contains(strings.Join(buildArgs(other), " "), "noise=amount=0") {
				t.Fatal("unneeded audio preroll filter")
			}
		})
	}
}

// An EOF observed by a tail seek on one rendition must not reject the
// advertised final segment while a replacement rendition is still producing it.
func TestDASHNewRenditionClearsPriorRunEOF(t *testing.T) {
	fr := newFakeRunner()
	fr.dashEnd = 24*time.Second + 21*time.Millisecond
	m := newTestManager(t, fr, config.Transcode{ABRLadder: true}, withRungDwell(0))
	s := startDASHSession(t, m, dashRequest(t, "tail-rung", fr.dashEnd))
	s.mu.Lock()
	s.endSeg = 11 // a seek at 24s saw no complete audio/video pair
	s.wantTrack = 2
	err := s.startRunLocked(0, false)
	end := s.endSeg
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if end != -1 {
		t.Fatalf("new rendition inherited EOF at segment %d", end)
	}
	if r := media(t, s, 2, 12); r.Code != http.StatusOK {
		t.Fatalf("advertised final segment: HTTP %d %s", r.Code, r.Body.String())
	}
}
