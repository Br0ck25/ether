package transcode

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestHLSTargetIsSeparateFromDirectPlayContainers(t *testing.T) {
	for _, tc := range []struct {
		targets []string
		want    string
	}{{nil, ContainerFMP4}, {[]string{"mkv"}, ContainerMatroska}, {[]string{"mp4"}, ContainerFMP4}} {
		caps := appleTV
		caps.Containers = []string{"mkv", "mp4", "mpegts"}
		caps.HLSContainers = tc.targets
		opts := allow()
		opts.DirectPlay = false
		d := Decide(mustParse(t, "mkv_hevc_hdr10.json"), caps, opts, Policy{})
		if d.Container != tc.want || !d.VideoCopy {
			t.Fatalf("targets %v: %+v", tc.targets, d)
		}
	}
}

func TestMatroskaHLSArgsAndPlaylist(t *testing.T) {
	d := core.Decision{Kind: core.DecisionRemux, Container: ContainerMatroska, VideoCopy: true, AudioCopy: true, AudioStream: 1}
	s := baseSpec(mustParse(t, "mkv_hevc_hdr10.json"), d)
	args := buildArgs(s)
	mustContain(t, args, "-f segment", "-segment_format matroska", "-segment_start_number 50", "-segment_list_type m3u8", "%05d.mkv")
	mustNotContain(t, args, "-hls_segment_type", "-tag:v hvc1", "-hls_fmp4_init_filename")
	b := string(mediaPlaylist(20*time.Second, 4*time.Second, ContainerMatroska, func(n int) string { return SegmentName(n, ContainerMatroska) }))
	if strings.Contains(b, "EXT-X-MAP") || !strings.Contains(b, "00000.mkv") {
		t.Fatal(b)
	}
	if n, ok := ParseSegmentName("00050.mkv"); !ok || n != 50 {
		t.Fatalf("segment name: %d %v", n, ok)
	}
}

func TestIntegrationMatroskaHLSDecodeSeekAndSwap(t *testing.T) {
	e := newRealEnv(t)
	e.gen(t, "mkv-a.mkv", 24, "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:keyint=24:min-keyint=24:scenecut=0", "-c:a", "eac3", "-ac", "2")
	e.gen(t, "mkv-b.mkv", 24, "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:keyint=24:min-keyint=24:scenecut=0", "-c:a", "eac3", "-ac", "2")
	ctx := context.Background()
	info, err := e.m.Probe(ctx, e.url("mkv-a.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	caps := appleTV
	caps.HLSContainers = []string{"mkv"}
	opts := allow()
	opts.DirectPlay = false
	d := e.m.Decide(*info, caps, opts)
	if d.Container != ContainerMatroska || !d.VideoCopy || !d.AudioCopy {
		t.Fatalf("decision: %+v", d)
	}
	hs, err := e.m.StartHLS(ctx, core.HLSRequest{SessionID: "it-matroska", InputURL: e.url("mkv-a.mkv"), Info: *info, Decision: d})
	if err != nil {
		t.Fatal(err)
	}
	decode := func(n int, name string, wantStart float64) {
		t.Helper()
		r := httptest.NewRecorder()
		hs.ServeSegment(r, httptest.NewRequest(http.MethodGet, "/", nil), n)
		if r.Code != 200 || r.Header().Get("Content-Type") != "video/x-matroska" {
			t.Fatalf("segment %d: %d %s", n, r.Code, r.Body.String())
		}
		b := r.Body.Bytes()
		if len(b) < 4 || string(b[:4]) != "\x1a\x45\xdf\xa3" {
			t.Fatalf("segment %d has no independent Matroska header", n)
		}
		path := e.writeTemp(t, name, b)
		streams := e.probeFile(t, path)
		if got := startTime(t, streams, "video"); math.Abs(got-wantStart) > 1.1 {
			t.Fatalf("segment %d starts %.3f, want %.3f", n, got, wantStart)
		}
		if out, err := exec.Command(e.ffmpeg, "-v", "error", "-i", path, "-map", "0:v:0", "-frames:v", "3", "-f", "null", "-").CombinedOutput(); err != nil {
			t.Fatalf("decode segment %d: %v %s", n, err, out)
		}
	}
	decode(0, "first.mkv", 0)
	decode(4, "seek.mkv", 16)
	other, err := e.m.Probe(ctx, e.url("mkv-b.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.SwapInput(ctx, e.url("mkv-b.mkv"), *other, 8*time.Second); err != nil {
		t.Fatal(err)
	}
	decode(2, "swap.mkv", 8)
}
