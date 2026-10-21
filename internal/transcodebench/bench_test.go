package transcodebench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

func TestValidate(t *testing.T) {
	base := Config{Input: "x.mp4", Duration: 8 * time.Second}
	if err := base.Validate(); err != nil {
		t.Fatalf("base should validate: %v", err)
	}
	for name, mut := range map[string]func(*Config){
		"no input":      func(c *Config) { c.Input = "" },
		"bad hwaccel":   func(c *Config) { c.HWAccel = "magic" },
		"bad mode":      func(c *Config) { c.Mode = "directplay" },
		"bad codec":     func(c *Config) { c.Codec = "av1" },
		"zero dur":      func(c *Config) { c.Duration = 0 },
		"neg dur":       func(c *Config) { c.Duration = -time.Second },
		"conc zero ok":  func(c *Config) { c.Concurrency = 0 }, // normalized to 1: valid, handled below
		"conc too big":  func(c *Config) { c.Concurrency = 65 },
		"seg too small": func(c *Config) { c.SegmentDur = time.Millisecond },
		"neg bitrate":   func(c *Config) { c.BitrateKbps = -1 },
		"bad height":    func(c *Config) { c.MaxHeight = 99999 },
	} {
		c := base
		mut(&c)
		err := c.Validate()
		if name == "conc zero ok" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	// Remux ignores codec choice.
	c := base
	c.Mode = ModeRemux
	c.Codec = "av1"
	if err := c.Validate(); err != nil {
		t.Errorf("remux with odd codec should validate: %v", err)
	}
}

func TestSegmentAccounting(t *testing.T) {
	cases := []struct {
		dur, seg time.Duration
		want     int
	}{
		{0, 4 * time.Second, 0},
		{8 * time.Second, 4 * time.Second, 2},
		{9 * time.Second, 4 * time.Second, 3},
		{time.Second, 4 * time.Second, 1},
	}
	for _, tc := range cases {
		if got := SegmentCount(tc.dur, tc.seg); got != tc.want {
			t.Errorf("SegmentCount(%s,%s)=%d want %d", tc.dur, tc.seg, got, tc.want)
		}
	}
	if got := DeliveredMediaSeconds(3, 4*time.Second, 9*time.Second); got != 9 {
		t.Errorf("delivered capped = %v want 9", got)
	}
	if got := DeliveredMediaSeconds(2, 4*time.Second, 30*time.Second); got != 8 {
		t.Errorf("delivered = %v want 8", got)
	}
	if got := DeliveredMediaSeconds(0, 4*time.Second, 30*time.Second); got != 0 {
		t.Errorf("delivered zero = %v", got)
	}
}

func TestSummarizeFailureIsNotSuccess(t *testing.T) {
	res := &Result{Sessions: []SessionResult{
		{ID: "a", Success: true, Segments: 2, Bytes: 100, MediaSec: 8},
		{ID: "b", Success: false, Error: "boom", Segments: 1, Bytes: 50, MediaSec: 4},
	}, WallMs: 2000}
	Summarize(res)
	if res.Success {
		t.Fatal("mixed failure must not be success")
	}
	if res.Totals.SessionsOK != 1 || res.Totals.SessionsFailed != 1 || res.Totals.Segments != 3 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	res2 := &Result{Sessions: []SessionResult{{ID: "a", Success: true, Segments: 1, MediaSec: 4}}, WallMs: 60000}
	res2.Timing.MeasurementMs = 1000
	res2.Timing.SetupMs = 50000
	Summarize(res2)
	if !res2.Success || res2.Totals.Speedup != 4 {
		t.Fatalf("speedup must use measurement window only (not setup/wall): %+v", res2.Totals)
	}
	// No measurement window -> no claimed speedup.
	res3 := &Result{Sessions: []SessionResult{{ID: "a", Success: true, Segments: 1, MediaSec: 4}}, WallMs: 1000}
	Summarize(res3)
	if res3.Totals.Speedup != 0 {
		t.Fatalf("speedup without measurement window = %v, want 0", res3.Totals.Speedup)
	}
	empty := &Result{}
	Summarize(empty)
	if empty.Success {
		t.Fatal("no sessions must not be success")
	}
}

func TestRedactArgs(t *testing.T) {
	args := []string{"-headers", "Authorization: Bearer secret\r\nX: y", "-i",
		"http://127.0.0.1:1/f.mp4?token=abc&part=1", "-user_agent", "MyAgent", "token=zzz", "-c:v", "libx264"}
	out := RedactArgs(args)
	joined := strings.Join(out, " ")
	for _, leak := range []string{"secret", "token=abc", "MyAgent", "token=zzz"} {
		if strings.Contains(joined, leak) {
			t.Errorf("redacted args leak %q: %s", leak, joined)
		}
	}
	if !strings.Contains(joined, "part=1") || !strings.Contains(joined, "libx264") {
		t.Errorf("non-secret args should survive: %s", joined)
	}
	// Input slice must not be mutated.
	if args[1] == "[redacted]" {
		t.Error("RedactArgs mutated caller slice")
	}
}

func TestEncoderDetection(t *testing.T) {
	if got := VideoEncoderFromArgs([]string{"-c:v", "h264_nvenc", "-c:a", "copy"}); got != "h264_nvenc" {
		t.Fatalf("encoder = %q", got)
	}
	if VideoEncoderFromArgs([]string{"-c:a", "aac"}) != "" {
		t.Fatal("no video encoder expected")
	}
	for enc, want := range map[string]bool{
		"libx264": false, "libx265": false, "copy": false, "": false,
		"h264_nvenc": true, "hevc_vaapi": true, "h264_videotoolbox": true, "hevc_qsv": true,
	} {
		if got := IsHWEncoder(enc); got != want {
			t.Errorf("IsHWEncoder(%q)=%v want %v", enc, got, want)
		}
	}
	enc, hw, seen := recordedVideoEncoder([]Launch{
		{Kind: "run", Encoder: "libx264"},
		{Kind: "start", Encoder: "libx264"},
	})
	if !seen || hw || enc != "libx264" {
		t.Fatalf("recorded = %q hw=%v seen=%v", enc, hw, seen)
	}
}

func TestCheckOutput(t *testing.T) {
	dec := core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", MaxHeight: 720}
	good := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "h264", Width: 1280, Height: 720, Profile: "High"}}}
	if ok, _ := checkOutput(good, dec, Config{Mode: ModeTranscode}); !ok {
		t.Fatal("matching output should verify")
	}
	badCodec := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "hevc", Width: 1280, Height: 720}}}
	if ok, detail := checkOutput(badCodec, dec, Config{Mode: ModeTranscode}); ok || !strings.Contains(detail, "codec") {
		t.Fatalf("codec mismatch should fail: %v %q", ok, detail)
	}
	tooTall := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "h264", Width: 1920, Height: 1080}}}
	if ok, _ := checkOutput(tooTall, dec, Config{Mode: ModeTranscode}); ok {
		t.Fatal("height over max should fail")
	}
	noVideo := OutputProbe{Streams: []StreamSummary{{Type: "audio", Codec: "aac"}}}
	if ok, _ := checkOutput(noVideo, dec, Config{Mode: ModeTranscode}); ok {
		t.Fatal("missing video should fail")
	}
	remuxDec := core.Decision{Kind: core.DecisionRemux, VideoCopy: true, VideoCodec: "hevc"}
	if ok, _ := checkOutput(badCodec, remuxDec, Config{Mode: ModeRemux}); !ok {
		t.Fatal("remux should accept copied source codec")
	}
}

func TestRunMissingInputWritesFailureResult(t *testing.T) {
	res, err := Run(context.Background(), Config{
		Input: filepath.Join(t.TempDir(), "nope.mp4"), Duration: 4 * time.Second,
	}, nil)
	if err == nil || res == nil || res.Success {
		t.Fatalf("missing input should fail: res=%+v err=%v", res, err)
	}
	if res.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %d", res.SchemaVersion)
	}
}

func TestRecorderWithFakeRunner(t *testing.T) {
	rec := NewRecorderWith(fakeRunner{})
	proc, err := rec.Start(transcode.Command{Path: "ffmpeg", Args: []string{"-headers", "Authorization: Bearer x", "-c:v", "libx264"}})
	if err != nil {
		t.Fatal(err)
	}
	proc.Stop()
	ls := rec.Snapshot()
	if len(ls) != 1 || ls[0].Encoder != "libx264" || ls[0].HWEncoder {
		t.Fatalf("launches = %+v", ls)
	}
	if strings.Contains(strings.Join(ls[0].Args, " "), "Bearer") {
		t.Fatalf("launch args not redacted: %v", ls[0].Args)
	}
	// Optional PID/CPU interfaces absent on the fake: must stay unset, not invented.
	if ls[0].PID != 0 || ls[0].CPUUserMs != nil {
		t.Fatalf("fake process metrics should be absent: %+v", ls[0])
	}
	if err := rec.Run(context.Background(), transcode.Command{Path: "ffprobe", Args: []string{"-version"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.Snapshot()); got != 2 {
		t.Fatalf("launches after Run = %d", got)
	}
	_ = os.Stdin // keep os import if unused elsewhere in file
}

// fakeRunner is an offline transcode.Runner: no processes are started.
type fakeRunner struct{}

func (fakeRunner) Start(cmd transcode.Command) (transcode.Process, error) {
	return fakeProc{done: make(chan struct{})}, nil
}
func (fakeRunner) Run(ctx context.Context, cmd transcode.Command) error { return nil }

type fakeProc struct{ done chan struct{} }

func (p fakeProc) Done() <-chan struct{} { c := make(chan struct{}); close(c); return c }
func (p fakeProc) Err() error            { return nil }
func (p fakeProc) Stop()                 {}
func (p fakeProc) Pause() error          { return transcode.ErrPauseUnsupported }
func (p fakeProc) Resume() error         { return nil }
