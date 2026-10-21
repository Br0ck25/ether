package transcode

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func rtxFixture() (core.MediaInfo, core.ClientCaps, core.DecideOptions) {
	return core.MediaInfo{Container: "mp4", Duration: time.Minute, BitrateKbps: 3000, Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "h264", Width: 1280, Height: 720, FrameRate: 24, BitDepth: 8},
		{Index: 2, Type: "audio", Codec: "aac", Channels: 2, SampleRate: 48000, BitrateKbps: 128},
		{Index: 4, Type: "audio", Codec: "aac", Channels: 2, SampleRate: 48000, BitrateKbps: 128},
	}}, core.ClientCaps{MaxHeight: 1080, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, Containers: []string{"mp4"}}, core.DecideOptions{DirectPlay: true, DirectStream: true, AudioStream: 4, SubtitleStream: -1}
}
func rtxManager() *Manager {
	return &Manager{cfg: config.Transcode{RTXVideo: config.RTXVideo{Mode: "on"}}, rtx: rtxBackendState{path: "NVEncC64.exe", support: core.VideoEnhancementSupport{Backend: rtxBackend, Available: true, Protocols: []string{core.ProtocolHLS, core.ProtocolDASH}}}}
}
func TestRTXPromotesDirectPlayAndRespectsConstraints(t *testing.T) {
	info, caps, opts := rtxFixture()
	m := rtxManager()
	if Decide(info, caps, opts, Policy{}).Kind != core.DecisionDirectPlay {
		t.Fatal("fixture must direct play")
	}
	d := m.Decide(info, caps, opts)
	if d.Kind != core.DecisionTranscode || d.VideoCopy || d.Enhancement.State != "selected" || d.Enhancement.Width != 1920 || d.Enhancement.Height != 1080 || d.AudioStream != 4 || !d.AudioCopy {
		t.Fatalf("decision: %+v", d)
	}
	r := buildRungs(info, d, adaptiveSettings{})[0]
	if r.width != 1920 || r.height != 1080 {
		t.Fatalf("rung: %+v", r)
	}
	reps := dashReps(info, d)
	if reps[0].Width != 1920 || reps[0].Height != 1080 {
		t.Fatalf("DASH metadata %+v", reps)
	}
	tests := []struct {
		name   string
		change func(*Manager, *core.MediaInfo, *core.ClientCaps, *core.DecideOptions)
	}{
		{"off", func(m *Manager, _ *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) {
			m.cfg.RTXVideo.Mode = "off"
		}},
		{"unavailable", func(m *Manager, _ *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) {
			m.rtx.support.Available = false
		}},
		{"forbidden", func(_ *Manager, _ *core.MediaInfo, _ *core.ClientCaps, o *core.DecideOptions) {
			o.DisableVideoTranscode = true
		}},
		{"download", func(_ *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) { c.Download = true }},
		{"client cap", func(_ *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) { c.MaxHeight = 720 }},
		{"request cap", func(_ *Manager, _ *core.MediaInfo, _ *core.ClientCaps, o *core.DecideOptions) { o.MaxHeight = 720 }},
		{"bitrate", func(_ *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) {
			c.MaxBitrateKbps = 4000
		}},
		{"codec", func(_ *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) {
			c.VideoCodecs = []string{"hevc"}
		}},
		{"profile", func(_ *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) {
			c.VideoProfiles = map[string][]string{"h264": {"baseline"}}
		}},
		{"level", func(_ *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) {
			c.VideoLevels = map[string]int{"h264": 31}
		}},
		{"HDR", func(_ *Manager, i *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) {
			i.Streams[0].ColorTransfer = "smpte2084"
		}},
		{"source cap", func(m *Manager, _ *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) {
			m.cfg.RTXVideo.SourceMaxHeight = 480
		}},
		{"estimate", func(_ *Manager, i *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) { i.Estimated = true }},
		{"audio filter", func(_ *Manager, _ *core.MediaInfo, _ *core.ClientCaps, o *core.DecideOptions) {
			o.AudioFilters = []string{"loudnorm"}
		}},
		{"audio conversion", func(_ *Manager, i *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) {
			i.Streams[2].Codec = "dts"
		}},
		{"subtitle", func(_ *Manager, i *core.MediaInfo, _ *core.ClientCaps, o *core.DecideOptions) {
			i.Streams = append(i.Streams, core.StreamInfo{Index: 5, Type: "subtitle", Codec: "subrip"})
			o.SubtitleStream = 5
		}},
		{"auto remote", func(m *Manager, _ *core.MediaInfo, c *core.ClientCaps, _ *core.DecideOptions) {
			m.cfg.RTXVideo.Mode = "auto"
			c.Remote = true
		}},
		{"invalid quality", func(m *Manager, _ *core.MediaInfo, _ *core.ClientCaps, _ *core.DecideOptions) {
			m.cfg.RTXVideo.Quality = -1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, c, o := rtxFixture()
			m := rtxManager()
			tt.change(m, &i, &c, &o)
			d := m.Decide(i, c, o)
			if enhancementSelected(d) || d.Enhancement.Reason == "" {
				t.Fatalf("must explain ineligibility: %+v", d)
			}
		})
	}
}

func rtxSpec(t *testing.T) runSpec {
	t.Helper()
	info, caps, opts := rtxFixture()
	d := rtxManager().Decide(info, caps, opts)
	return runSpec{Input: "http://127.0.0.1:9999/source?token=secret", Info: info, Decision: d, Protocol: core.ProtocolHLS, Dir: t.TempDir(), Playlist: "run.m3u8", InitName: "init.mp4", SegDur: 4 * time.Second}
}
func TestRTXArgsRealNGXAndMuxContracts(t *testing.T) {
	for _, proto := range []string{core.ProtocolHLS, core.ProtocolDASH} {
		t.Run(proto, func(t *testing.T) {
			s := rtxSpec(t)
			s.Protocol = proto
			s.InputHeaders = map[string]string{"Authorization": "Bearer secret", "User-Agent": "private-agent"}
			if proto == core.ProtocolDASH {
				s.Decision.Container = ContainerFMP4
				s.Playlist = "manifest.mpd"
				s.ChunkDur = time.Second
			}
			args, keys, err := buildRTXArgs(s)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(args, " ")
			for _, want := range []string{"--avhw", "--vpp-resize algo=ngx-vsr,vsr-quality=2", "--output-res 1920x1080", "--audio-copy 2", "--bframes 0", "format_whitelist:" + vodInputFormats, "protocol_whitelist:http,https,tcp,tls,crypto", "output_ts_offset:0.000000000"} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %s in %v", want, args)
				}
			}
			if string(keys) != "96\n192\n288\n384\n480\n576\n672\n768\n864\n960\n1056\n1152\n1248\n1344\n" {
				t.Fatalf("wrong frame boundaries %q", keys)
			}
			if proto == core.ProtocolDASH && !strings.Contains(joined, "media_seg_name:chunk-$RepresentationID$-$Number%05d$.m4s") {
				t.Fatalf("DASH names %v", args)
			}
			redacted := strings.Join(redactedRTXArgs(args), " ")
			for _, secret := range []string{"secret", "private-agent"} {
				if strings.Contains(redacted, secret) {
					t.Fatalf("leaked input secret: %s", redacted)
				}
			}
		})
	}
}
func TestRTXSeekUsesIndexAndAbsoluteFrameClock(t *testing.T) {
	s := rtxSpec(t)
	s.Start = 10 * time.Second
	s.StartSeg = 2
	if _, _, err := buildRTXArgs(s); err == nil {
		t.Fatal("unindexed seek accepted")
	}
	s.Info.Keyframes = []core.KeyframePoint{{Time: 0}, {Time: 8 * time.Second}}
	a, k, e := buildRTXArgs(s)
	if e != nil {
		t.Fatal(e)
	}
	if argValue(a, "--seek") != "7.999000" || argValue(a, "--trim") != "48:0" || !strings.Contains(strings.Join(a, " "), "output_ts_offset:10.000000000") || !strings.HasPrefix(string(k), "96\n") {
		t.Fatalf("seek args %v keys %s", a, k)
	}
	s.Info.Streams[0].FrameRate = 24000.0 / 1001
	s.Start = 0
	_, k, e = buildRTXArgs(s)
	if e != nil {
		t.Fatal(e)
	}
	keys := strings.Fields(string(k))
	if keys[0] != "96" || keys[10] != "1055" {
		t.Fatalf("fractional boundaries drift: %v", keys)
	}
	for _, input := range []string{"concat:http://host/a|file:///secret", "file:///secret", "http://user:password@host/a"} {
		s.Input = input
		if _, _, err := buildRTXArgs(s); err == nil {
			t.Errorf("unsafe input accepted %q", input)
		}
	}
}

// Native fake translates mux options into the existing filesystem simulator.
// The recorded command remains the real executable API, not FFmpeg flags.
type rtxTestRunner struct {
	Runner
	fake        *fakeRunner
	mu          sync.Mutex
	calls       []Command
	fail        bool
	failRuntime bool
}

func (r *rtxTestRunner) Start(c Command) (Process, error) {
	if c.Path != "NVEncC64.exe" {
		return r.Runner.Start(c)
	}
	r.mu.Lock()
	r.calls = append(r.calls, c)
	fail, runtimeFail := r.fail, r.failRuntime
	r.mu.Unlock()
	if fail {
		return nil, errors.New("simulated launch failure")
	}
	if c.Stderr != nil {
		io.WriteString(c.Stderr, "Vpp Filters     resize: ngx-vsr 1280x720 -> 1920x1080\n")
	}
	a := []string{"-f", argValue(c.Args, "-f"), "-c:v", "h264_nvenc", "-ss", muxValue(c.Args, "output_ts_offset")}
	for i := 0; i+1 < len(c.Args); i++ {
		if c.Args[i] == "-m" {
			k, v, _ := strings.Cut(c.Args[i+1], ":")
			a = append(a, "-"+k, v)
			i++
		}
	}
	a = append(a, argValue(c.Args, "-o"))
	c.Args = a
	if runtimeFail {
		p := &fakeProc{done: make(chan struct{}), kill: make(chan struct{}), err: errors.New("NGX init failed")}
		close(p.done)
		return p, nil
	}
	return r.Runner.Start(c)
}
func muxValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-m" && strings.HasPrefix(args[i+1], name+":") {
			return strings.TrimPrefix(args[i+1], name+":")
		}
	}
	return ""
}
func (r *rtxTestRunner) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }
func newRTXSessionManager(t *testing.T) (*Manager, *rtxTestRunner, core.HLSRequest) {
	fr := newFakeRunner()
	fr.segDelay = 20 * time.Millisecond
	rr := &rtxTestRunner{Runner: fr, fake: fr}
	m := newTestManager(t, fr, config.Transcode{RTXVideo: config.RTXVideo{Mode: "on"}}, WithRunner(rr))
	m.rtx.path = "NVEncC64.exe"
	m.rtx.support = core.VideoEnhancementSupport{Backend: rtxBackend, Available: true, Protocols: []string{core.ProtocolHLS, core.ProtocolDASH}}
	i, c, o := rtxFixture()
	i.Duration = 10 * time.Minute
	return m, rr, core.HLSRequest{SessionID: "rtx", InputURL: "http://127.0.0.1:9999/source", Info: i, Decision: m.Decide(i, c, o)}
}
func TestRTXSessionLifecycleAndConcurrencyFallback(t *testing.T) {
	m, r, req := newRTXSessionManager(t)
	s := startSession(t, m, req)
	if s.Decision().Enhancement.State != "starting" {
		t.Fatal("must not claim runtime success at launch")
	}
	if rec := get(t, s, 0); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	waitFor(t, "VSR running", func() bool { return s.Decision().Enhancement.State == "running" })
	if m.EnhancementStatus().Active != 1 {
		t.Fatal("missing slot")
	}
	req.SessionID = "second"
	other := startSession(t, m, req)
	if d := other.Decision(); d.Enhancement.State != "fallback" || !strings.Contains(d.Enhancement.Reason, "concurrency") {
		t.Fatalf("cap fallback %+v", d)
	}
	if r.count() != 1 {
		t.Fatal("cap started another enhancer")
	}
	old := r.fake.lastProc()
	_ = old // last proc belongs to ordinary fallback
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if m.EnhancementStatus().Active != 0 {
		t.Fatal("permit leaked on stop")
	}
}
func TestRTXStartupFailureFallsBackOnce(t *testing.T) {
	for _, runtimeFail := range []bool{false, true} {
		t.Run(strconv.FormatBool(runtimeFail), func(t *testing.T) {
			m, r, req := newRTXSessionManager(t)
			r.fail = !runtimeFail
			r.failRuntime = runtimeFail
			s := startSession(t, m, req)
			if rec := get(t, s, 0); rec.Code != 200 {
				t.Fatal(rec.Code)
			}
			if d := s.Decision(); d.Enhancement.State != "fallback" {
				t.Fatalf("fallback missing %+v", d)
			}
			if r.count() != 1 || m.EnhancementStatus().Active != 0 {
				t.Fatal("retry loop or permit leak")
			}
			if s.rungs[0].height != 720 {
				t.Fatal("stale enhanced manifest dimensions")
			}
		})
	}
}
func TestRTXUnsupportedSeekFallsBackAndMarksDiscontinuity(t *testing.T) {
	m, r, req := newRTXSessionManager(t)
	s := startSession(t, m, req)
	if rec := get(t, s, 0); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := get(t, s, 100); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if d := s.Decision(); d.Enhancement.State != "fallback" || !strings.Contains(d.Enhancement.Reason, "keyframe index") {
		t.Fatalf("seek fallback %+v", d)
	}
	s.mu.Lock()
	discont := s.discontBeforeLocked(0, 100)
	s.mu.Unlock()
	if !discont || r.count() != 1 || m.EnhancementStatus().Active != 0 {
		t.Fatal("seek lifecycle/manifest incorrect")
	}
}
func TestRTXDetectionRejectsUnsupportedPlatform(t *testing.T) {
	m := rtxManager()
	m.rtx = rtxBackendState{}
	m.detectRTX(context.Background(), "linux")
	if s := m.EnhancementStatus(); s.Available || !strings.Contains(s.Reason, "Windows") {
		t.Fatalf("%+v", s)
	}
}
func TestRTXKeyfileIsSessionLocal(t *testing.T) {
	m, _, req := newRTXSessionManager(t)
	s := startSession(t, m, req)
	s.mu.Lock()
	dir := s.cur.spec.Dir
	s.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "rtx-keyframes.txt")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("session files survived close")
	}
}

func TestRTXIndexedSeekRestartsNativeAndRevalidatesFailover(t *testing.T) {
	m, r, req := newRTXSessionManager(t)
	req.Info.Keyframes = []core.KeyframePoint{{Time: 0}, {Time: 396 * time.Second}}
	s := startSession(t, m, req)
	if rec := get(t, s, 0); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := get(t, s, 100); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if r.count() != 2 {
		t.Fatal("indexed seek did not restart native backend")
	}
	r.mu.Lock()
	last := r.calls[len(r.calls)-1]
	r.mu.Unlock()
	if argValue(last.Args, "--trim") != "96:0" || muxValue(last.Args, "output_ts_offset") != "400.000000000" {
		t.Fatalf("bad seek clock %v", last.Args)
	}
	replacement := req.Info
	replacement.Streams = append([]core.StreamInfo(nil), req.Info.Streams...)
	replacement.Streams[0].ColorTransfer = "smpte2084"
	if err := s.SwapInput(context.Background(), req.InputURL+"-replacement", replacement, 400*time.Second); err != nil {
		t.Fatal(err)
	}
	if d := s.Decision(); d.Enhancement.State != "fallback" || !strings.Contains(d.Enhancement.Reason, "SDR") {
		t.Fatalf("failover did not revalidate: %+v", d)
	}
	if m.EnhancementStatus().Active != 0 {
		t.Fatal("failover leaked native permit")
	}
}

func TestRTXSelectedDecisionCannotBypassOwnerOff(t *testing.T) {
	m, r, req := newRTXSessionManager(t)
	cfg := m.cfg
	cfg.RTXVideo.Mode = "off"
	m.UpdateConfig(cfg)
	s := startSession(t, m, req)
	if r.count() != 0 || s.Decision().Enhancement.State != "fallback" {
		t.Fatal("stale selected decision bypassed owner off")
	}
}

func TestRTXNativeSourceFailureUsesExistingFailureHandler(t *testing.T) {
	m, r, req := newRTXSessionManager(t)
	r.fake.failAfter = 1
	failures := make(chan string, 1)
	m.opts.onInputFailure = func(_ string, input string, _ time.Duration, _ error) { failures <- input }
	s := startSession(t, m, req)
	select {
	case input := <-failures:
		if input != req.InputURL {
			t.Fatal("wrong source generation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native source failure not reported")
	}
	if m.EnhancementStatus().Active != 0 {
		t.Fatal("failed process slot leaked")
	}
	if s.Decision().Enhancement.State == "running" {
		t.Fatal("failure retained runtime success")
	}
}

func TestRTXHonorsRunVBVBudgetAndRejectsQualityStarvation(t *testing.T) {
	s := rtxSpec(t)
	s.Rate = rateControl{Quality: true, MaxrateKbps: 8000, BufsizeKbps: 64000}
	args, _, err := buildRTXArgs(s)
	if err != nil {
		t.Fatal(err)
	}
	if argValue(args, "--max-bitrate") != "8000" || argValue(args, "--vbv-bufsize") != "64000" {
		t.Fatalf("run reservoir ignored: %v", args)
	}
	s.Rate.MaxrateKbps = 1000
	if _, _, err := buildRTXArgs(s); err == nil {
		t.Fatal("kept VSR at a quality-starved bitrate")
	}
}
func TestRTXFractionalTrimSeekRejectsDisabledKeyfile(t *testing.T) {
	s := rtxSpec(t)
	s.Info.Streams[0].FrameRate = 24000.0 / 1001
	s.Start = 8 * time.Second
	s.Info.Keyframes = []core.KeyframePoint{{Time: 0}, {Time: 6 * time.Second}}
	if _, _, err := buildRTXArgs(s); err == nil || !strings.Contains(err.Error(), "fractional-rate") {
		t.Fatalf("fractional trim must not drift: %v", err)
	}
}

func TestRTXRejectsProbedInterlaceAndVariableRate(t *testing.T) {
	for _, stream := range []ffprobeStream{
		{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720, AvgFrameRate: "24/1", RFrameRate: "24/1", FieldOrder: "tt"},
		{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720, AvgFrameRate: "25/1", RFrameRate: "30/1", FieldOrder: "progressive"},
	} {
		mapped, ok := mapStream(stream)
		if !ok {
			t.Fatal("probe did not map video")
		}
		i, c, o := rtxFixture()
		i.Streams[0] = mapped
		if d := rtxManager().Decide(i, c, o); enhancementSelected(d) || !strings.Contains(d.Enhancement.Reason, "ordinary") {
			t.Fatalf("unsupported frame structure accepted: %+v", d)
		}
	}
}

func TestRTXLongNTSCKeyPlanNeverPrecedesBoundary(t *testing.T) {
	for _, rate := range []float64{23.976, 29.97, 47.952, 59.94} {
		s := rtxSpec(t)
		s.Info.Duration = 16000 * time.Second
		s.Info.Streams[0].FrameRate = rate
		_, keys, err := buildRTXArgs(s)
		if err != nil {
			t.Fatal(err)
		}
		exact, _ := rtxFrameRate(rate)
		for i, key := range strings.Fields(string(keys)) {
			frame, err := strconv.Atoi(key)
			if err != nil {
				t.Fatal(err)
			}
			pts := float64(frame) / exact
			boundary := float64(i+1) * 4
			if pts+1e-7 < boundary || pts >= boundary+1/exact+1e-7 {
				t.Fatalf("rate %f boundary %f scheduled at %f", rate, boundary, pts)
			}
		}
	}
}

func TestRTXBusyDecisionKeepsDirectPlay(t *testing.T) {
	m := rtxManager()
	m.rtx.active = 1
	i, c, o := rtxFixture()
	d := m.Decide(i, c, o)
	if d.Kind != core.DecisionDirectPlay || d.Enhancement.State != "fallback" {
		t.Fatalf("busy optional enhancer forced an unnecessary encode: %+v", d)
	}
}
