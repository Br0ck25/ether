package transcode

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestRecoveryBufferStopsAtHolesAndRequiresAudio(t *testing.T) {
	s := &Session{segDur: 2 * time.Second, proto: core.ProtocolDASH, total: 20}
	s.lastReq = 2
	s.dash = &dashState{reps: []dashRep{{Kind: "video"}, {Kind: "audio"}, {Kind: "video"}}}
	s.done = []map[int]bool{{2: true, 3: true, 4: true, 6: true}, {2: true, 3: true, 6: true}, {}}
	b := s.RecoveryBuffer()
	if b.Position != 4*time.Second || b.ReadyUntil != 8*time.Second {
		t.Fatal(b)
	}
	s.proto = core.ProtocolHLS
	b = s.RecoveryBuffer()
	if b.ReadyUntil != 10*time.Second {
		t.Fatal("hole counted as playable", b)
	}
	s.lastReq = 6
	b = s.RecoveryBuffer()
	if b.Position != 12*time.Second || b.ReadyUntil != 14*time.Second {
		t.Fatal("seek reused old buffer", b)
	}
}

func TestBufferedSwapPreservesPublishedMediaAndViewerPosition(t *testing.T) {
	fr := newFakeRunner()
	fr.segDelay = 20 * time.Millisecond
	m := newTestManager(t, fr, config.Transcode{}, WithBuffering(16*time.Second, 8*time.Second, 8*time.Second))
	req := remuxRequest(t, "buffer-swap", time.Hour)
	s := startSession(t, m, req)
	original := get(t, s, 0).Body.Bytes()
	waitFor(t, "prepared buffer", func() bool { return s.RecoveryBuffer().ReadyUntil >= 8*time.Second })
	if err := s.SwapInputBuffered(context.Background(), "http://127.0.0.1:1/source/b", req.Info, 0); err != nil {
		t.Fatal(err)
	}
	if s.RequestedPosition() != 0 {
		t.Fatal("swap moved viewer ahead", s.RequestedPosition())
	}
	if got := get(t, s, 0).Body.Bytes(); !bytes.Equal(got, original) {
		t.Fatal("buffered original replaced")
	}
	if argValue(fr.lastStart(), "-ss") == "0.000" || argValue(fr.lastStart(), "-ss") == "" {
		t.Fatal("alternate restarted before buffer boundary")
	}
}

// Real ffmpeg must decode the preserved prefix and replacement suffix without
// fetching old input or restarting the viewer at the handoff.
func TestIntegrationBufferedRepairHandoff(t *testing.T) {
	e := newRealEnv(t, WithoutHWDetection(), WithBuffering(8*time.Second, 4*time.Second, 4*time.Second))
	e.gen(t, "buffer-a.mkv", 32, "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", "-g", "48", "-c:a", "aac")
	info, err := e.m.Probe(t.Context(), e.url("buffer-a.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	hs, err := e.m.StartHLS(t.Context(), core.HLSRequest{SessionID: "buffered-repair", InputURL: e.url("buffer-a.mkv"), Info: *info, Decision: e.m.Decide(*info, plexWeb, allow()), SegmentDuration: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s := hs.(*Session)
	first := fetch(t, hs, 0)
	waitFor(t, "real prepared buffer", func() bool { return s.RecoveryBuffer().ReadyUntil >= 6*time.Second })
	if err := s.SwapInputBuffered(t.Context(), e.url("buffer-a.mkv")+"?replacement=1", *info, 0); err != nil {
		t.Fatal(err)
	}
	if s.RequestedPosition() != 0 || !bytes.Equal(fetch(t, hs, 0), first) {
		t.Fatal("handoff discarded original buffer")
	}
	// Consume beyond the retained buffer and decode the assembled TS stream.
	var media []byte
	for n := 0; n < 10; n++ {
		media = append(media, fetch(t, hs, n)...)
	}
	path := e.writeTemp(t, "repair-handoff.ts", media)
	out, err := exec.CommandContext(t.Context(), e.ffmpeg, "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("handoff decode: %v: %s", err, out)
	}
}
