package transcode

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

const rtxBackend = "nvenc-ngx-vsr"

func validRTXQuality(q int) bool { return q >= 1 && q <= 4 || q == 21 || q == 23 }

// rtxBackendState has its own lock: stopping/reaping a process while holding
// Session.mu must never acquire Manager.mu (which can inspect sessions).
type rtxBackendState struct {
	mu             sync.Mutex
	support        core.VideoEnhancementSupport
	path           string
	configuredPath string
	active         int
}

func normalizedRTX(c config.RTXVideo) (config.RTXVideo, string) {
	if c.Mode == "" {
		c.Mode = "off"
	}
	if c.SourceMaxHeight == 0 {
		c.SourceMaxHeight = 720
	}
	if c.TargetMaxHeight == 0 {
		c.TargetMaxHeight = 1080
	}
	if c.Quality == 0 {
		c.Quality = 2
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 1
	}
	if c.Mode != "off" && c.Mode != "auto" && c.Mode != "on" {
		return c, "invalid RTX Video mode"
	}
	if c.SourceMaxHeight < 144 || c.SourceMaxHeight > 2160 || c.TargetMaxHeight < 144 || c.TargetMaxHeight > 2160 || !validRTXQuality(c.Quality) || c.MaxConcurrent < 1 || c.MaxConcurrent > 8 {
		return c, "invalid RTX Video limits or quality"
	}
	return c, ""
}

func (m *Manager) detectRTX(ctx context.Context, goos string) {
	path, support := probeRTX(ctx, m.runner, goos, m.cfg.RTXVideo.ExecutablePath)
	m.rtx.path, m.rtx.support = path, support
	m.rtx.configuredPath = m.cfg.RTXVideo.ExecutablePath
}

func probeRTX(ctx context.Context, runner Runner, goos, configured string) (path string, support core.VideoEnhancementSupport) {
	b := &rtxBackendState{configuredPath: configured}
	defer func() { path, support = b.path, b.support }()
	b.support.Backend = rtxBackend
	b.support.SetupState = "unavailable"
	fail := func(reason string) { b.support.Reason = reason }
	if goos != "windows" {
		fail("RTX Video requires native Windows and an NVIDIA RTX GPU")
		return
	}
	candidate := b.configuredPath
	if candidate == "" {
		candidate = "NVEncC64.exe"
	}
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		fail("NVEncC executable not found; install the NVEncC 9.38 x64 package")
		return
	}
	b.path = resolved
	for _, name := range []string{"nvngx_vsr.dll", "NVEncNVSDKNGX.dll"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(resolved), name)); err != nil {
			fail("NVEncC NGX runtime DLLs are missing")
			return
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	query := func(arg string) (string, error) {
		var out boundedRTXOutput
		err := runner.Run(ctx, Command{Path: resolved, Args: []string{arg}, Stdout: &out, Stderr: &out})
		return out.String(), err
	}
	version, err := query("--version")
	if err != nil || !strings.Contains(version, "NVEncC (x64) 9.38 (") || !strings.Contains(version, "ngx        : yes") {
		fail("NVEncC 9.38 with NGX support is required")
		return
	}
	b.support.Version = strings.Split(strings.TrimSpace(version), "\n")[0]
	devices, err := query("--check-device")
	if err != nil || !strings.Contains(devices, "DeviceId #0: NVIDIA") || !strings.Contains(strings.Split(devices, "\n")[0], "RTX") {
		fail("NVEncC device 0 is not a detected NVIDIA RTX GPU")
		return
	}
	formats, err := query("--check-formats")
	if err != nil || !rtxFormatWritable(formats, "hls") || !rtxFormatWritable(formats, "dash") {
		fail("NVEncC requires HLS and DASH muxers")
		return
	}
	b.support.Available = true
	b.support.SetupState = "ready"
	b.support.Protocols = []string{core.ProtocolHLS, core.ProtocolDASH}
	b.support.Reason = "NVEncC/NGX detected; runtime success is reported per session"
	return
}

// Detection output is bounded even for a broken owner-supplied executable.
type boundedRTXOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedRTXOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if left := 64*1024 - len(b.data); left > 0 {
		b.data = append(b.data, p[:min(left, n)]...)
	}
	return n, nil
}
func (b *boundedRTXOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
func rtxFormatWritable(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[1] == name && strings.Contains(f[0], "M") {
			return true
		}
	}
	return false
}

// EnhancementStatus is suitable for an owner status API. Available means
// detection passed; it deliberately does not mean a VSR run has succeeded.
func (m *Manager) EnhancementStatus() core.VideoEnhancementSupport {
	m.rtx.mu.Lock()
	defer m.rtx.mu.Unlock()
	s := m.rtx.support
	s.Active = m.rtx.active
	s.Protocols = append([]string(nil), s.Protocols...)
	return s
}

func enhancementSelected(d core.Decision) bool {
	return d.Enhancement.Backend == rtxBackend && (d.Enhancement.State == "selected" || d.Enhancement.State == "starting" || d.Enhancement.State == "running")
}

func (m *Manager) enhanceDecision(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions, d core.Decision) core.Decision {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	c, invalid := normalizedRTX(cfg.RTXVideo)
	status := core.VideoEnhancement{Backend: rtxBackend, State: "ineligible"}
	reject := func(state, reason string) core.Decision {
		status.State, status.Reason = state, reason
		d.Enhancement = status
		return d
	}
	if invalid != "" {
		return reject("unavailable", invalid)
	}
	if c.Mode == "off" {
		return reject("off", "disabled by owner")
	}
	support := m.EnhancementStatus()
	m.rtx.mu.Lock()
	configured := m.rtx.configuredPath
	m.rtx.mu.Unlock()
	if c.ExecutablePath != configured {
		return reject("unavailable", "backend path changed; setup pending")
	}
	if !support.Available {
		reason := support.Reason
		if reason == "" {
			reason = "RTX Video backend is unavailable"
		}
		return reject("unavailable", reason)
	}
	if support.Active >= c.MaxConcurrent {
		return reject("fallback", "enhancement concurrency cap reached")
	}
	m.mu.Lock()
	atEncoderCap := cfg.MaxTranscodes > 0 && m.encodingLocked("") >= cfg.MaxTranscodes
	m.mu.Unlock()
	if atEncoderCap {
		return reject("fallback", "ordinary encoder concurrency cap reached")
	}
	if opts.DisableVideoTranscode || caps.Download {
		return reject("ineligible", "client forbids optional video transcoding")
	}
	if c.Mode == "auto" && caps.Remote {
		return reject("ineligible", "auto mode enhances LAN playback only")
	}
	v := info.Video()
	if info.Estimated || info.Duration <= 0 || v == nil || v.Width <= 0 || v.Height <= 0 || v.Height > c.SourceMaxHeight {
		return reject("ineligible", "source is not a probed VOD title within the source resolution ceiling")
	}
	if r := rtxSourceReason(info, d); r != "" {
		return reject("ineligible", r)
	}
	if len(caps.VideoCodecs) > 0 && !caps.SupportsVideo("h264") {
		return reject("ineligible", "client does not support H.264 output")
	}
	if profiles := caps.VideoProfiles["h264"]; len(profiles) > 0 && !contains(profiles, "high") {
		return reject("ineligible", "client does not support the H.264 high profile")
	}
	height := even(minPositive(c.TargetMaxHeight, caps.MaxHeight, opts.MaxHeight))
	if height <= v.Height {
		return reject("ineligible", "target/client resolution cap leaves no upscale")
	}
	width := even(int(math.Round(float64(v.Width) * float64(height) / float64(v.Height))))
	if math.Ceil(float64(width)/16)*math.Ceil(float64(height)/16)*v.FrameRate > 983040 {
		return reject("ineligible", "enhanced output exceeds the validated H.264 level 5.1 frame-rate limit")
	}
	if width > 3840 {
		return reject("ineligible", "target exceeds 3840 pixels wide")
	}
	// Fixed level 5.1 accommodates our maximum 2160p/30 or 1080p/60 output.
	// Lower explicit decoder levels are not silently exceeded.
	level := 41
	if width*height > 1920*1088 || v.FrameRate > 30 {
		level = 51
	}
	if maxLevel := caps.VideoLevels["h264"]; maxLevel > 0 && maxLevel < level {
		return reject("ineligible", "client H.264 level limit is below the enhanced output")
	}
	remote := 0
	if caps.Remote {
		remote = cfg.RemoteMaxBitrateKbps
	}
	cap := minPositive(opts.MaxBitrateKbps, caps.MaxBitrateKbps, remote)
	// Re-negotiate audio through the ordinary decision policy before selecting
	// enhancement. Never drop a track/filter to make VSR eligible.
	forced := opts
	forced.DirectPlay = false
	forced.DirectStream = false
	nd := Decide(info, caps, forced, m.Policy())
	nd.VideoCodec = "h264"
	nd.ToneMap = false
	nd.StripDolbyVision = false
	nd.Container = HLSContainer(caps, "h264", nd.AudioCodec)
	if nd.Container == ContainerMatroska {
		return reject("ineligible", "RTX Video does not support segmented Matroska")
	}
	if r := rtxSourceReason(info, nd); r != "" {
		return reject("ineligible", r)
	}
	minVideo := minRTXVideoKbps(height)
	if cap > 0 && cap-decisionAudioKbps(info, nd) < minVideo {
		return reject("ineligible", "bitrate cap is too low for enhanced resolution")
	}
	if cap == 0 {
		cap = bitrateCeiling(height, "h264") + decisionAudioKbps(info, nd)
	}
	nd.Kind = core.DecisionTranscode
	nd.VideoCopy = false
	nd.MaxHeight = height
	nd.BitrateKbps = cap
	nd.Enhancement = core.VideoEnhancement{Backend: rtxBackend, State: "selected", Reason: "eligible for NVIDIA RTX Video Super Resolution", Width: width, Height: height, Quality: c.Quality}
	nd.Reason = fmt.Sprintf("transcode for NVIDIA RTX Video Super Resolution (%dx%d, quality %d); selected audio preserved", width, height, c.Quality)
	return nd
}

func rtxSourceReason(info core.MediaInfo, d core.Decision) string {
	v := info.Video()
	if v == nil || v.Width <= 0 || v.Height <= 0 || v.FrameRate < 1 || v.FrameRate > 60 || math.IsNaN(v.FrameRate) || math.IsInf(v.FrameRate, 0) {
		return "source frame rate or dimensions are unsupported"
	}
	if _, ok := rtxFrameRate(v.FrameRate); !ok {
		return "source frame-rate rational is not supported by the native keyframe scheduler"
	}
	if v.VariableFrameRate || v.Interlaced {
		return "interlaced or variable-rate input requires the ordinary transcoder"
	}
	if isHDR(v) || v.BitDepth > 8 || d.ToneMap {
		return "RTX Video currently supports 8-bit SDR sources only"
	}
	if !contains([]string{"h264", "hevc", "mpeg2video", "vp9", "av1"}, v.Codec) {
		return "source codec is not validated for NVDEC"
	}
	if d.BurnSubtitles || d.SubtitleStream >= 0 {
		return "selected subtitles require the ordinary transcoder"
	}
	if len(d.AudioFilters) > 0 {
		return "audio filters require the ordinary transcoder"
	}
	if a := findStream(info, d.AudioStream, "audio"); a != nil {
		if !d.AudioCopy || !contains([]string{"aac", "ac3", "eac3", "mp3"}, a.Codec) {
			return "audio conversion or this audio codec requires the ordinary transcoder"
		}
		if d.AudioChannels > 0 && d.AudioChannels != a.Channels || d.AudioSampleRate > 0 && d.AudioSampleRate != a.SampleRate {
			return "audio configuration preservation requires the ordinary transcoder"
		}
	}
	return ""
}

// decisionOutputSize is used only for manifest metadata. Ordinary FFmpeg
// outputSize retains its no-upscale policy.
func decisionOutputSize(v *core.StreamInfo, d core.Decision) (int, int) {
	if enhancementSelected(d) || d.Enhancement.Backend == rtxBackend && d.Enhancement.State == "stopped" && d.Enhancement.Width > 0 {
		return d.Enhancement.Width, d.Enhancement.Height
	}
	return outputSize(v.Width, v.Height, d.MaxHeight)
}

func minRTXVideoKbps(height int) int {
	switch {
	case height > 1080:
		return 16000
	case height > 720:
		return 5000
	case height > 480:
		return 2000
	default:
		return 1000
	}
}

// Ether's historic probe field rounds to three decimals. Reconstruct standard
// NTSC rationals before frame-index scheduling; 59.94 instead of 60000/1001
// can put a keyframe before its boundary late in a film, doubling a segment.
// Unknown fractional rates need a future exact-rational core field.
func rtxFrameRate(rate float64) (float64, bool) {
	for _, n := range []float64{24000, 30000, 48000, 60000} {
		r := n / 1001
		if math.Abs(rate-r) < 0.00051 {
			return r, true
		}
	}
	if rate >= 1 && rate <= 60 && math.Abs(rate-math.Round(rate)) < 1e-8 {
		return rate, true
	}
	return 0, false
}
