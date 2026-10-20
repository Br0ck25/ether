package transcode

import (
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestIntegrationMatroskaPresentationStartsAtSeekOffset(t *testing.T) {
	e := newRealEnv(t, WithoutHWDetection(), WithSegmentTimeout(5*time.Second))
	e.gen(t, "seek-offset.mkv", 24, "-c:v", "libx264", "-preset", "ultrafast", "-g", "24", "-c:a", "aac")
	info, err := e.m.Probe(t.Context(), e.url("seek-offset.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	d := core.Decision{Kind: core.DecisionRemux, Container: ContainerMatroska, VideoCopy: true, AudioCopy: true, AudioStream: 1}
	hs, err := e.m.StartHLS(t.Context(), core.HLSRequest{SessionID: "seek-offset", InputURL: e.url("seek-offset.mkv"), Info: *info, Decision: d, StartOffset: 12 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	data := append(fetch(t, hs, InitSegment), fetch(t, hs, 0)...)
	path := e.writeTemp(t, "offset-result.mkv", data)
	if got := startTime(t, e.probeFile(t, path), "video"); math.Abs(got-22) > 1.1 {
		t.Fatalf("seek presentation starts %.3f, want 22", got)
	}
}

func TestIntegrationMatroskaPresentationLongSeekOffset(t *testing.T) {
	e := newRealEnv(t, WithoutHWDetection(), WithSegmentTimeout(5*time.Second))
	out, err := exec.Command(e.ffmpeg, "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "color=size=64x64:rate=1", "-f", "lavfi", "-i", "anullsrc=sample_rate=8000:channel_layout=stereo", "-t", "4500", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:keyint=2:min-keyint=2:scenecut=0", "-c:a", "aac", filepath.Join(e.dir, "long-seek.mkv")).CombinedOutput()
	if err != nil {
		t.Skipf("cannot generate HEVC: %v %s", err, out)
	}
	info, err := e.m.Probe(t.Context(), e.url("long-seek.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	d := core.Decision{Kind: core.DecisionRemux, Container: ContainerMatroska, VideoCopy: true, AudioCopy: true, AudioStream: 1}
	hs, err := e.m.StartHLS(t.Context(), core.HLSRequest{SessionID: "long-seek-offset", InputURL: e.url("long-seek.mkv"), Info: *info, Decision: d, StartOffset: 4451 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	data := append(fetch(t, hs, InitSegment), fetch(t, hs, 0)...)
	path := e.writeTemp(t, "long-offset-result.mkv", data)
	if got := startTime(t, e.probeFile(t, path), "video"); math.Abs(got-4461) > 2.1 {
		t.Fatalf("long seek starts %.3f, want 4461", got)
	}
}
