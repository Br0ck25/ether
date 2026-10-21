// Package transcodebench implements a reproducible benchmark harness that
// drives the real transcode.Manager (probe, decision, HLS sessions and
// ServeSegment) against a loopback range-capable HTTP source serving a
// local fixture file. It never builds ad-hoc ffmpeg command lines for the
// measured work: every measured encode/remux is a Manager session. Direct
// ffprobe use is limited to verifying the retained output after a run.
package transcodebench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// SchemaVersion is the version of the Result JSON document.
const SchemaVersion = 1

// Modes accepted by Config.Mode.
const (
	ModeTranscode = "transcode"
	ModeRemux     = "remux"
)

// Config is the benchmark configuration. All fields are plain values so the
// CLI (and tests) can build one without flag parsing.
type Config struct {
	Input       string        // local fixture path (required)
	FFmpeg      string        // ffmpeg path/lookup hint; empty = Manager discovery
	FFprobe     string        // ffprobe path/lookup hint
	HWAccel     string        // none|auto|videotoolbox|nvenc|qsv|vaapi (default none)
	Mode        string        // transcode|remux (default transcode)
	Codec       string        // target video codec for transcode: h264|hevc (default h264)
	MaxHeight   int           // 0 = source height
	BitrateKbps int           // 0 = Manager decision's bitrate
	Concurrency int           // simultaneous sessions (default 1)
	Duration    time.Duration // media seconds to consume per session (required > 0)
	SegmentDur  time.Duration // HLS segment duration (default 4s)
	Paced       bool          // consume at roughly realtime pace
	ResultPath  string        // JSON result path; empty = stdout only in CLI
	OutputDir   string        // retain init/segments/concatenated output here when set
	Timeout     time.Duration // overall context timeout (default 10m)
	VAAPIDevice string
}

// Normalized returns cfg with defaults applied.
func (c Config) Normalized() Config {
	if c.HWAccel == "" {
		c.HWAccel = transcode.AccelNone
	}
	c.HWAccel = strings.ToLower(strings.TrimSpace(c.HWAccel))
	if c.Mode == "" {
		c.Mode = ModeTranscode
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Codec == "" {
		c.Codec = "h264"
	}
	c.Codec = strings.ToLower(strings.TrimSpace(c.Codec))
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	if c.SegmentDur <= 0 {
		c.SegmentDur = 4 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Minute
	}
	return c
}

// Validate reports configuration errors without touching any service.
func (c Config) Validate() error {
	c = c.Normalized()
	if strings.TrimSpace(c.Input) == "" {
		return errors.New("transcodebench: -input is required")
	}
	switch c.HWAccel {
	case transcode.AccelNone, "auto", "cpu", "software",
		transcode.AccelVideoToolbox, transcode.AccelNVENC, transcode.AccelQSV, transcode.AccelVAAPI:
	default:
		return fmt.Errorf("transcodebench: unsupported -hwaccel %q", c.HWAccel)
	}
	switch c.Mode {
	case ModeTranscode, ModeRemux:
	default:
		return fmt.Errorf("transcodebench: unsupported -mode %q (want transcode|remux)", c.Mode)
	}
	if c.Mode == ModeTranscode {
		switch c.Codec {
		case "h264", "hevc":
		default:
			return fmt.Errorf("transcodebench: unsupported -codec %q (want h264|hevc)", c.Codec)
		}
	}
	if c.Concurrency < 1 || c.Concurrency > 64 {
		return fmt.Errorf("transcodebench: -concurrency %d out of range 1..64", c.Concurrency)
	}
	if c.Duration <= 0 {
		return errors.New("transcodebench: -duration must be > 0")
	}
	if c.SegmentDur < 500*time.Millisecond || c.SegmentDur > time.Minute {
		return fmt.Errorf("transcodebench: -segment-duration %s out of range 500ms..1m", c.SegmentDur)
	}
	if c.MaxHeight < 0 || c.MaxHeight > 4320 {
		return fmt.Errorf("transcodebench: -max-height %d out of range", c.MaxHeight)
	}
	if c.BitrateKbps < 0 {
		return errors.New("transcodebench: -bitrate-kbps must be >= 0")
	}
	return nil
}

// SegmentCount is the number of whole segments needed to cover dur.
func SegmentCount(dur, seg time.Duration) int {
	if dur <= 0 || seg <= 0 {
		return 0
	}
	return int((dur + seg - 1) / seg)
}

// DeliveredMediaSeconds is the media time covered by n served segments,
// capped at the requested duration.
func DeliveredMediaSeconds(n int, seg, requested time.Duration) float64 {
	if n <= 0 {
		return 0
	}
	d := time.Duration(n) * seg
	if requested > 0 && d > requested {
		d = requested
	}
	return d.Seconds()
}

// StreamSummary is a compact, JSON-friendly stream description.
type StreamSummary struct {
	Type     string `json:"type"`
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Channels int    `json:"channels,omitempty"`
}

// DecisionSummary mirrors the explicit decision handed to StartHLS.
type DecisionSummary struct {
	Kind        string `json:"kind"`
	VideoCopy   bool   `json:"videoCopy"`
	AudioCopy   bool   `json:"audioCopy"`
	VideoCodec  string `json:"videoCodec"`
	AudioCodec  string `json:"audioCodec,omitempty"`
	Container   string `json:"container"`
	MaxHeight   int    `json:"maxHeight,omitempty"`
	BitrateKbps int    `json:"bitrateKbps,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// SessionResult is one concurrent session's measurements.
type SessionResult struct {
	ID           string       `json:"id"`
	Success      bool         `json:"success"`
	Error        string       `json:"error,omitempty"`
	StartupMs    int64        `json:"startupMs"` // barrier release -> first segment served
	WallMs       int64        `json:"wallMs"`    // barrier release -> last segment served
	Segments     int          `json:"segments"`  // segments successfully served
	MediaSec     float64      `json:"mediaSec"`  // delivered media seconds (capped at request)
	Bytes        int64        `json:"bytes"`     // segment bytes (init counted separately)
	InitBytes    int64        `json:"initBytes,omitempty"`
	PlaylistB    int          `json:"playlistBytes,omitempty"`
	OutputPath   string       `json:"outputPath,omitempty"` // retained concatenated output, when kept
	OutputSHA256 string       `json:"outputSha256,omitempty"`
	OutputProbe  *OutputProbe `json:"outputProbe,omitempty"`
	HWFallback   bool         `json:"hwFallback,omitempty"`

	retainPath string `json:"-"`
	retainDir  string `json:"-"` // temp dir to remove after verification (when -output unset)
}

// OutputProbe is ffprobe verification of the retained output.
type OutputProbe struct {
	Container   string          `json:"container,omitempty"`
	DurationSec float64         `json:"durationSec,omitempty"`
	Streams     []StreamSummary `json:"streams,omitempty"`
	Error       string          `json:"error,omitempty"`
	Verified    bool            `json:"verified"`
	Detail      string          `json:"detail,omitempty"`
}

// Result is the versioned JSON document written to -result.
type Result struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	StartedAt     time.Time `json:"startedAt"`
	WallMs        int64     `json:"wallMs"` // total elapsed (setup+measurement+verification+teardown)
	// Timing splits WallMs so Speedup is never computed over setup/probe.
	Timing struct {
		SetupMs        int64 `json:"setupMs"`        // hash, Manager start, probe, decision
		MeasurementMs  int64 `json:"measurementMs"`  // barrier release -> last segment delivered (encoders then stopped)
		VerificationMs int64 `json:"verificationMs"` // ffprobe of retained output, after encoders stopped
	} `json:"timing"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`

	Config struct {
		Mode        string  `json:"mode"`
		Codec       string  `json:"codec,omitempty"`
		HWAccel     string  `json:"hwAccel"`
		MaxHeight   int     `json:"maxHeight,omitempty"`
		BitrateKbps int     `json:"bitrateKbps,omitempty"`
		Concurrency int     `json:"concurrency"`
		DurationSec float64 `json:"durationSec"`
		SegmentSec  float64 `json:"segmentSec"`
		Paced       bool    `json:"paced"`
	} `json:"config"`

	Fixture struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"fixture"`

	Runtime struct {
		GoVersion   string   `json:"goVersion"`
		GOOS        string   `json:"goos"`
		GOARCH      string   `json:"goarch"`
		NumCPU      int      `json:"numCPU"`
		FFmpegPath  string   `json:"ffmpegPath,omitempty"`
		FFprobePath string   `json:"ffprobePath,omitempty"`
		FFmpegVer   string   `json:"ffmpegVersion,omitempty"`
		FFprobeVer  string   `json:"ffprobeVersion,omitempty"`
		HWDetected  string   `json:"hwDetected,omitempty"`
		HWEncoders  []string `json:"hwEncoders,omitempty"`
		HWFallback  bool     `json:"hwFallback,omitempty"` // requested HW not actually used
	} `json:"runtime"`

	Probe struct {
		Container   string          `json:"container"`
		DurationSec float64         `json:"durationSec"`
		BitrateKbps int             `json:"bitrateKbps,omitempty"`
		Streams     []StreamSummary `json:"streams,omitempty"`
	} `json:"probe"`

	Decision DecisionSummary `json:"decision"`

	Sessions []SessionResult `json:"sessions"`
	Launches []Launch        `json:"launches"`

	Totals struct {
		SessionsOK     int     `json:"sessionsOk"`
		SessionsFailed int     `json:"sessionsFailed"`
		Segments       int     `json:"segments"`
		Bytes          int64   `json:"bytes"`
		MediaSec       float64 `json:"mediaSec"`
		Speedup        float64 `json:"speedup,omitempty"` // mediaSec / wallSec (aggregate)
	} `json:"totals"`
}

// Summarize folds per-session results into totals. Pure; unit-tested.
func Summarize(res *Result) {
	var ok, failed, segs int
	var bytes int64
	var media float64
	for _, s := range res.Sessions {
		if s.Success {
			ok++
		} else {
			failed++
		}
		segs += s.Segments
		bytes += s.Bytes + s.InitBytes
		media += s.MediaSec
	}
	res.Totals.SessionsOK = ok
	res.Totals.SessionsFailed = failed
	res.Totals.Segments = segs
	res.Totals.Bytes = bytes
	res.Totals.MediaSec = media
	// Throughput is measured work only: media seconds over the
	// barrier->last-delivery window, never over setup/probe/ffprobe time.
	if res.Timing.MeasurementMs > 0 && media > 0 {
		res.Totals.Speedup = media / (float64(res.Timing.MeasurementMs) / 1000.0)
	} else {
		res.Totals.Speedup = 0
	}
	res.Success = failed == 0 && ok > 0 && res.Error == ""
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func summarizeStreams(ss []core.StreamInfo) []StreamSummary {
	out := make([]StreamSummary, 0, len(ss))
	for _, s := range ss {
		out = append(out, StreamSummary{Type: s.Type, Codec: s.Codec, Profile: s.Profile,
			Width: s.Width, Height: s.Height, Channels: s.Channels})
	}
	return out
}

// explicitDecision asks the Manager to decide, then applies the benchmark's
// explicit overrides and verifies the result matches the requested mode, so
// a remux control can never silently become a transcode or vice versa.
func explicitDecision(mgr *transcode.Manager, info core.MediaInfo, cfg Config) (core.Decision, error) {
	v := info.Video()
	srcCodec := ""
	if v != nil {
		srcCodec = v.Codec
	}
	capsCodecs := []string{cfg.Codec}
	if cfg.Mode == ModeRemux {
		capsCodecs = []string{srcCodec}
	}
	caps := core.ClientCaps{
		Product:     "ether-transcodebench",
		Platform:    "benchmark",
		MaxHeight:   cfg.MaxHeight,
		VideoCodecs: capsCodecs,
		AudioCodecs: []string{"aac", "ac3", "eac3", "mp3"},
		Containers:  []string{"mp4", "mkv", "mpegts"},
	}
	if cfg.BitrateKbps > 0 {
		caps.MaxBitrateKbps = cfg.BitrateKbps
	}
	opts := core.DecideOptions{DirectPlay: false, DirectStream: cfg.Mode == ModeRemux,
		AudioStream: -1, SubtitleStream: -1}
	d := mgr.Decide(info, caps, opts)
	switch cfg.Mode {
	case ModeRemux:
		if d.Kind != core.DecisionRemux || !d.VideoCopy {
			return d, fmt.Errorf("transcodebench: requested remux but decision is kind=%q videoCopy=%v (%s)",
				d.Kind, d.VideoCopy, d.Reason)
		}
	case ModeTranscode:
		if d.Kind != core.DecisionTranscode || d.VideoCopy {
			return d, fmt.Errorf("transcodebench: requested transcode but decision is kind=%q videoCopy=%v (%s)",
				d.Kind, d.VideoCopy, d.Reason)
		}
		d.VideoCodec = cfg.Codec
		if cfg.MaxHeight > 0 {
			d.MaxHeight = cfg.MaxHeight
		}
		if cfg.BitrateKbps > 0 {
			d.BitrateKbps = cfg.BitrateKbps
		}
	}
	return d, nil
}

// Run executes the benchmark. It always returns a Result describing what
// happened; a non-nil error (and Result.Success == false) means the run
// failed. The caller should exit nonzero whenever Success is false.
func Run(ctx context.Context, cfg Config, log *slog.Logger) (*Result, error) {
	cfg = cfg.Normalized()
	res := &Result{SchemaVersion: SchemaVersion, Kind: "ether-transcodebench/result", StartedAt: time.Now()}
	res.Config.Mode = cfg.Mode
	res.Config.Codec = cfg.Codec
	res.Config.HWAccel = cfg.HWAccel
	res.Config.MaxHeight = cfg.MaxHeight
	res.Config.BitrateKbps = cfg.BitrateKbps
	res.Config.Concurrency = cfg.Concurrency
	res.Config.DurationSec = cfg.Duration.Seconds()
	res.Config.SegmentSec = cfg.SegmentDur.Seconds()
	res.Config.Paced = cfg.Paced
	res.Runtime.GoVersion = runtime.Version()
	res.Runtime.GOOS = runtime.GOOS
	res.Runtime.GOARCH = runtime.GOARCH
	res.Runtime.NumCPU = runtime.NumCPU()
	overallStart := time.Now()
	defer func() {
		res.WallMs = time.Since(overallStart).Milliseconds()
		Summarize(res)
	}()

	fail := func(err error) (*Result, error) {
		res.Error = err.Error()
		res.WallMs = time.Since(overallStart).Milliseconds()
		Summarize(res)
		return res, err
	}

	if err := cfg.Validate(); err != nil {
		return fail(err)
	}
	fi, err := os.Stat(cfg.Input)
	if err != nil || fi.IsDir() {
		if err == nil {
			err = errors.New("is a directory")
		}
		return fail(fmt.Errorf("transcodebench: -input %s: %w", cfg.Input, err))
	}
	abs, _ := filepath.Abs(cfg.Input)
	res.Fixture.Path = abs
	sum, size, err := sha256File(cfg.Input)
	if err != nil {
		return fail(fmt.Errorf("transcodebench: hash fixture: %w", err))
	}
	res.Fixture.SHA256 = sum
	res.Fixture.Size = size

	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// Loopback range-capable source for the fixture. Each request opens
	// its own handle: concurrent ffmpeg readers must not share a seeker.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fh, err := os.Open(cfg.Input)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer fh.Close()
		http.ServeContent(w, r, filepath.Base(cfg.Input), fi.ModTime(), fh)
	}))
	defer srv.Close()
	inputURL := srv.URL + "/" + filepath.Base(cfg.Input)

	rec := NewRecorder()
	tmp, err := os.MkdirTemp("", "ether-transcodebench-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tmp)

	tcfg := config.Transcode{
		FFmpegPath:    cfg.FFmpeg,
		FFprobePath:   cfg.FFprobe,
		HWAccel:       cfg.HWAccel,
		VAAPIDevice:   cfg.VAAPIDevice,
		TempDir:       filepath.Join(tmp, "sessions"),
		MaxTranscodes: cfg.Concurrency,
	}
	mgr, err := transcode.New(tcfg, log, transcode.WithRunner(rec))
	if err != nil {
		res.Launches = rec.Snapshot()
		return fail(fmt.Errorf("transcodebench: start manager: %w", err))
	}
	defer mgr.Close()
	res.Runtime.FFmpegPath = mgr.FFmpegPath()
	res.Runtime.FFprobePath = mgr.FFprobePath()
	res.Runtime.FFmpegVer = mgr.Version()
	res.Runtime.FFprobeVer = ffprobeVersion(ctx, mgr.FFprobePath())
	hw := mgr.HWAccel()
	res.Runtime.HWDetected = hw.Name
	res.Runtime.HWEncoders = hw.Encoders

	// A specifically requested accelerator that the Manager could not
	// confirm is a fallback, never a hardware win: fail before measuring.
	if isExplicitHW(cfg.HWAccel) && hw.Name != cfg.HWAccel {
		res.Runtime.HWFallback = true
		res.Launches = rec.Snapshot()
		return fail(fmt.Errorf("transcodebench: requested hwaccel %q but detected %q (software fallback would not count as a hardware result)",
			cfg.HWAccel, hw.Name))
	}

	info, err := mgr.Probe(ctx, inputURL)
	if err != nil {
		res.Launches = rec.Snapshot()
		return fail(fmt.Errorf("transcodebench: probe: %w", err))
	}
	res.Probe.Container = info.Container
	res.Probe.DurationSec = info.Duration.Seconds()
	res.Probe.BitrateKbps = info.BitrateKbps
	res.Probe.Streams = summarizeStreams(info.Streams)
	// Never invent media past the end of the source: a request longer
	// than the fixture cannot deliver that many seconds.
	if info.Duration > 0 && cfg.Duration > info.Duration {
		res.Launches = rec.Snapshot()
		return fail(fmt.Errorf("transcodebench: -duration %s exceeds source duration %s", cfg.Duration, info.Duration.Round(time.Millisecond)))
	}

	decision, err := explicitDecision(mgr, *info, cfg)
	if err != nil {
		res.Launches = rec.Snapshot()
		return fail(err)
	}
	res.Decision = DecisionSummary{Kind: string(decision.Kind), VideoCopy: decision.VideoCopy,
		AudioCopy: decision.AudioCopy, VideoCodec: decision.VideoCodec, AudioCodec: decision.AudioCodec,
		Container: decision.Container, MaxHeight: decision.MaxHeight,
		BitrateKbps: decision.BitrateKbps, Reason: decision.Reason}

	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			res.Launches = rec.Snapshot()
			return fail(fmt.Errorf("transcodebench: -output dir: %w", err))
		}
	}

	segCount := SegmentCount(cfg.Duration, cfg.SegmentDur)
	results := make([]SessionResult, cfg.Concurrency)
	var ready sync.WaitGroup
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		ready.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = runSession(ctx, mgr, cfg, *info, decision, inputURL, i, segCount, &ready, release)
		}(i)
	}
	res.Timing.SetupMs = time.Since(overallStart).Milliseconds()
	measureStart := time.Now()
	ready.Wait()
	close(release) // all sessions start consuming simultaneously
	wg.Wait()
	// Measurement ends at last delivery. runSession has already closed
	// each session (stopping its encoder) before returning, so no encode
	// runs during verification below.
	res.Timing.MeasurementMs = time.Since(measureStart).Milliseconds()
	mgr.Close()
	res.Sessions = results
	// Snapshot now: PID/CPU/argv reflect the measured window only.
	res.Launches = rec.Snapshot()

	// Verification phase (separate from measurement): ffprobe the
	// retained output of each successful session.
	verifyStart := time.Now()
	for i := range res.Sessions {
		s := &res.Sessions[i]
		if s.retainPath == "" {
			continue
		}
		if s.Success {
			op := verifyOutput(ctx, mgr.FFprobePath(), s.retainPath, decision, cfg)
			s.OutputProbe = &op
			if !op.Verified {
				s.Success = false
				s.Error = "output verification: " + op.Detail
				if op.Error != "" {
					s.Error += ": " + op.Error
				}
			}
		}
		if s.retainDir != "" {
			_ = os.RemoveAll(s.retainDir)
			s.retainDir = ""
		}
	}
	res.Timing.VerificationMs = time.Since(verifyStart).Milliseconds()

	// Encoder-level fallback check: a transcode that claimed hardware but
	// whose recorded launches used a software encoder is a fallback.
	if cfg.Mode == ModeTranscode && isExplicitHW(cfg.HWAccel) {
		if enc, hwEnc, seen := recordedVideoEncoder(res.Launches); seen && !hwEnc {
			res.Runtime.HWFallback = true
			for i := range res.Sessions {
				res.Sessions[i].HWFallback = true
				if res.Sessions[i].Success {
					res.Sessions[i].Success = false
					res.Sessions[i].Error = fmt.Sprintf("software encoder %q used despite requested hwaccel %q", enc, cfg.HWAccel)
				}
			}
		}
	}
	for i := range res.Sessions {
		if !res.Sessions[i].Success {
			res.WallMs = time.Since(overallStart).Milliseconds()
			Summarize(res)
			return res, fmt.Errorf("transcodebench: %d/%d sessions failed (first: %s)",
				res.Totals.SessionsFailed, cfg.Concurrency, firstSessionError(res.Sessions))
		}
	}
	res.WallMs = time.Since(overallStart).Milliseconds()
	Summarize(res)
	return res, nil
}

func firstSessionError(ss []SessionResult) string {
	for _, s := range ss {
		if !s.Success && s.Error != "" {
			return s.ID + ": " + s.Error
		}
	}
	return "unknown"
}

func isExplicitHW(hw string) bool {
	switch strings.ToLower(hw) {
	case transcode.AccelVideoToolbox, transcode.AccelNVENC, transcode.AccelQSV, transcode.AccelVAAPI:
		return true
	}
	return false
}

// runSession starts one HLS session and consumes segments through the real
// ServeSegment path. ready/release implement the simultaneous start.
func runSession(ctx context.Context, mgr *transcode.Manager, cfg Config, info core.MediaInfo,
	decision core.Decision, inputURL string, idx, segCount int,
	ready *sync.WaitGroup, release <-chan struct{}) (out SessionResult) {

	id := fmt.Sprintf("bench-%d", idx)
	out.ID = id
	ready.Done()
	<-release // simultaneous start after probe/setup
	start := time.Now()
	sess, err := mgr.StartHLS(ctx, core.HLSRequest{
		SessionID:       id,
		InputURL:        inputURL,
		Info:            info,
		Decision:        decision,
		SegmentDuration: cfg.SegmentDur,
		Protocol:        core.ProtocolHLS,
	})
	if err != nil {
		out.Error = "StartHLS: " + err.Error()
		out.WallMs = time.Since(start).Milliseconds()
		return out
	}
	defer func() { out.WallMs = time.Since(start).Milliseconds() }()
	// The encoder is stopped explicitly right after the last segment is
	// delivered (below), before any verification runs. This deferred close
	// is only a safety net for early returns; Session.Close is idempotent.
	defer sess.Close()

	fail := func(format string, args ...any) SessionResult {
		out.Error = fmt.Sprintf(format, args...)
		return out
	}

	// Always retain (init + segments, streamed) long enough to verify the
	// actual output with ffprobe; kept on disk only when -output is set.
	ext := ".ts"
	if decision.Container == transcode.ContainerFMP4 {
		ext = ".mp4"
	} else if decision.Container == transcode.ContainerMatroska {
		ext = ".mkv"
	}
	var retained *os.File
	if cfg.OutputDir != "" {
		dir := filepath.Join(cfg.OutputDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fail("output dir: %v", err)
		}
		out.retainPath = filepath.Join(dir, "output"+ext)
		out.OutputPath = out.retainPath
	} else {
		td, err := os.MkdirTemp("", "ether-transcodebench-out-")
		if err != nil {
			return fail("temp output: %v", err)
		}
		out.retainDir = td // removed by Run after verification
		out.retainPath = filepath.Join(td, "output"+ext)
	}
	retained, err = os.Create(out.retainPath)
	if err != nil {
		return fail("create retained output: %v", err)
	}
	retainedClosed := false
	closeRetained := func() error {
		if retainedClosed {
			return nil
		}
		retainedClosed = true
		return retained.Close()
	}
	defer closeRetained()

	// Playlist through the real session (small; captured to parse EXTINF).
	var segDurs []float64
	{
		rec := newCaptureWriter(1 << 20)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/index.m3u8", nil)
		sess.ServePlaylist(rec, req)
		if rec.status != http.StatusOK {
			return fail("ServePlaylist: HTTP %d", rec.status)
		}
		out.PlaylistB = int(rec.count)
		segDurs = parsePlaylistDurations(rec.buf)
		if cfg.OutputDir != "" {
			if err := os.WriteFile(filepath.Join(cfg.OutputDir, id, "index.m3u8"), rec.buf, 0o644); err != nil {
				return fail("write playlist: %v", err)
			}
		}
	}
	// Init segment (fMP4/Matroska); a 404 on MPEG-TS is expected.
	{
		var initFile *os.File
		var sinks []io.Writer
		sinks = append(sinks, retained)
		if cfg.OutputDir != "" {
			initFile, err = os.Create(filepath.Join(cfg.OutputDir, id, "init.bin"))
			if err != nil {
				return fail("create init file: %v", err)
			}
			sinks = append(sinks, initFile)
		}
		sw := newStreamWriter(sinks...)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/init", nil)
		sess.ServeSegment(sw, req, transcode.InitSegment)
		if initFile != nil {
			if cerr := initFile.Close(); cerr != nil && out.Error == "" {
				return fail("close init file: %v", cerr)
			}
		}
		if sw.status == http.StatusOK && sw.count > 0 {
			if sw.err != nil {
				return fail("write init segment: %v", sw.err)
			}
			out.InitBytes = sw.count
		}
	}

	var firstAt time.Time
	for n := 0; n < segCount; n++ {
		if err := ctx.Err(); err != nil {
			out.Error = "context: " + err.Error()
			break
		}
		segStart := time.Now()
		var segFile *os.File
		sinks := []io.Writer{retained}
		if cfg.OutputDir != "" {
			ext2 := filepath.Ext(transcode.SegmentName(n, decision.Container))
			segFile, err = os.Create(filepath.Join(cfg.OutputDir, id, fmt.Sprintf("seg-%05d%s", n, ext2)))
			if err != nil {
				out.Error = fmt.Sprintf("create segment file %d: %v", n, err)
				break
			}
			sinks = append(sinks, segFile)
		}
		sw := newStreamWriter(sinks...)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("/seg/%d", n), nil)
		sess.ServeSegment(sw, req, n)
		if segFile != nil {
			if cerr := segFile.Close(); cerr != nil && sw.err == nil {
				sw.err = cerr
			}
		}
		if sw.status != http.StatusOK {
			out.Error = fmt.Sprintf("ServeSegment %d: HTTP %d: %s", n, sw.status,
				strings.TrimSpace(truncate(string(sw.snippet), 300)))
			break
		}
		if sw.err != nil {
			out.Error = fmt.Sprintf("write segment %d: %v", n, sw.err)
			break
		}
		if firstAt.IsZero() {
			firstAt = time.Now()
			out.StartupMs = firstAt.Sub(start).Milliseconds()
		}
		out.Segments++
		out.Bytes += sw.count
		if cfg.Paced && n < segCount-1 {
			if wait := cfg.SegmentDur - time.Since(segStart); wait > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
			}
		}
	}
	// Delivered media from actual playlist EXTINF durations (a shortened
	// final segment counts for what it is), capped at the request.
	out.MediaSec = deliveredSeconds(segDurs, out.Segments, cfg.SegmentDur.Seconds(), cfg.Duration.Seconds())
	if out.Error == "" && out.Segments < segCount {
		out.Error = fmt.Sprintf("served %d/%d segments", out.Segments, segCount)
	}
	// Stop the encoder NOW, before verification: no encode may run while
	// ffprobe checks the output, and CPU is measured over delivery only.
	if cerr := sess.Close(); cerr != nil && out.Error == "" {
		out.Error = fmt.Sprintf("close session: %v", cerr)
	}
	if cerr := closeRetained(); cerr != nil && out.Error == "" {
		out.Error = fmt.Sprintf("close retained output: %v", cerr)
	}
	if out.Error == "" {
		if sum, _, herr := sha256File(out.retainPath); herr != nil {
			out.Error = fmt.Sprintf("hash retained output: %v", herr)
		} else {
			out.OutputSHA256 = sum
		}
	}
	out.Success = out.Error == ""
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// WriteResult writes res as indented JSON to path (atomically via rename).
func WriteResult(path string, res *Result) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
