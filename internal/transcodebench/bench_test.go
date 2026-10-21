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

func TestSoftwareFallbackInspectsAllLaunches(t *testing.T) {
	// Mixed concurrency: a software session must not be masked by a
	// hardware session recorded after it (the old last-only check).
	mixed := []Launch{
		{Kind: "start", Encoder: "h264_nvenc", HWEncoder: true},
		{Kind: "start", Encoder: "libx264", HWEncoder: false},
		{Kind: "start", Encoder: "h264_nvenc", HWEncoder: true},
		{Kind: "run", Encoder: "libx265", HWEncoder: false}, // probe/test runs do not count
	}
	if got := softwareFallbackEncoders(mixed); len(got) != 1 || got[0] != "libx264" {
		t.Fatalf("mixed fallback = %v, want [libx264]", got)
	}
	allHW := []Launch{
		{Kind: "start", Encoder: "h264_nvenc", HWEncoder: true},
		{Kind: "start", Encoder: "hevc_nvenc", HWEncoder: true},
	}
	if got := softwareFallbackEncoders(allHW); len(got) != 0 {
		t.Fatalf("all-HW fallback = %v, want none", got)
	}
	if got := softwareFallbackEncoders(nil); len(got) != 0 {
		t.Fatalf("empty fallback = %v", got)
	}
}

func TestCheckOutput(t *testing.T) {
	src := &StreamSummary{Type: "video", Codec: "h264", Width: 1920, Height: 1080}
	dec := core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", MaxHeight: 720}
	good := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "h264", Width: 1280, Height: 720, Profile: "High"}}}
	if ok, _ := checkOutput(good, dec, Config{Mode: ModeTranscode}, src); !ok {
		t.Fatal("matching output should verify")
	}
	badCodec := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "hevc", Width: 1280, Height: 720}}}
	if ok, detail := checkOutput(badCodec, dec, Config{Mode: ModeTranscode}, src); ok || !strings.Contains(detail, "codec") {
		t.Fatalf("codec mismatch should fail: %v %q", ok, detail)
	}
	tooTall := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "h264", Width: 1920, Height: 1080}}}
	if ok, _ := checkOutput(tooTall, dec, Config{Mode: ModeTranscode}, src); ok {
		t.Fatal("height over max should fail")
	}
	// Under the cap but not the exact scaled size must also fail.
	wrongWidth := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "h264", Width: 1278, Height: 720}}}
	if ok, detail := checkOutput(wrongWidth, dec, Config{Mode: ModeTranscode}, src); ok || !strings.Contains(detail, "expected") {
		t.Fatalf("non-exact width should fail: %v %q", ok, detail)
	}
	smallerButCapped := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "h264", Width: 640, Height: 360}}}
	if ok, _ := checkOutput(smallerButCapped, dec, Config{Mode: ModeTranscode}, src); ok {
		t.Fatal("smaller-than-expected output should fail exact check")
	}
	noVideo := OutputProbe{Streams: []StreamSummary{{Type: "audio", Codec: "aac"}}}
	if ok, _ := checkOutput(noVideo, dec, Config{Mode: ModeTranscode}, src); ok {
		t.Fatal("missing video should fail")
	}
	remuxDec := core.Decision{Kind: core.DecisionRemux, VideoCopy: true, VideoCodec: "hevc"}
	hevcSrc := &StreamSummary{Type: "video", Codec: "hevc", Width: 1280, Height: 720}
	if ok, _ := checkOutput(badCodec, remuxDec, Config{Mode: ModeRemux}, hevcSrc); !ok {
		t.Fatal("remux should accept copied source codec/size")
	}
	resizedRemux := OutputProbe{Streams: []StreamSummary{{Type: "video", Codec: "hevc", Width: 640, Height: 360}}}
	if ok, _ := checkOutput(resizedRemux, remuxDec, Config{Mode: ModeRemux}, hevcSrc); ok {
		t.Fatal("remux must retain exact source dimensions")
	}
}

func TestExpectedDimensions(t *testing.T) {
	cases := []struct {
		name         string
		dec          core.Decision
		cfg          Config
		sw, sh       int
		wantW, wantH int
		wantOK       bool
	}{
		{"no scale at cap", core.Decision{MaxHeight: 720}, Config{Mode: ModeTranscode}, 1280, 720, 1280, 720, true},
		{"scale 1080p to 720p", core.Decision{MaxHeight: 720}, Config{Mode: ModeTranscode}, 1920, 1080, 1280, 720, true},
		{"no cap keeps source", core.Decision{}, Config{Mode: ModeTranscode}, 640, 360, 640, 360, true},
		{"odd source encode evens", core.Decision{}, Config{Mode: ModeTranscode}, 641, 361, 640, 360, true},
		{"odd source copy exact", core.Decision{VideoCopy: true}, Config{Mode: ModeRemux}, 641, 361, 641, 361, true},
		{"portrait aspect", core.Decision{MaxHeight: 720}, Config{Mode: ModeTranscode}, 1080, 1920, 404, 720, true},
		{"unknown source", core.Decision{MaxHeight: 720}, Config{Mode: ModeTranscode}, 0, 0, 0, 0, false},
	}
	for _, tc := range cases {
		w, h, ok := ExpectedDimensions(tc.dec, tc.cfg, tc.sw, tc.sh)
		if w != tc.wantW || h != tc.wantH || ok != tc.wantOK {
			t.Errorf("%s: ExpectedDimensions=%dx%d,%v want %dx%d,%v", tc.name, w, h, ok, tc.wantW, tc.wantH, tc.wantOK)
		}
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
