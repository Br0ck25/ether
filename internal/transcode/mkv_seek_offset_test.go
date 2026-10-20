package transcode

import (
	"math"
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
