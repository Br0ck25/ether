package transcode

import (
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// The segment plan is the one abstraction the adaptive HLS features share:
// every HLS session has a Timeline saying, for each segment number, when it
// starts, how long it lasts and how it must be produced (copy, transcode,
// fast-start or clamped transcode), and whether a discontinuity precedes it.
// All ABR rungs of a session share the same Timeline so segment N covers the
// same media on every rung.
//
// Trade-offs, deliberately documented:
//   - Boundaries are exact for transcoded output (keyframes are forced at
//     them). For a copy (remux) stream ffmpeg can only cut at source
//     keyframes, so when MediaInfo.Keyframes is known the timeline is built
//     FROM those keyframes (each nominal boundary snaps to the next
//     keyframe) and every rung inherits them; without a keyframe index the
//     boundaries are nominal and a copy segment may really start at the
//     next keyframe, exactly as before this abstraction existed.
//   - The spike governor may re-plan segments the player has not requested
//     yet (copy -> clamped transcode). That is only visible to clients that
//     reload the media playlist, so governed sessions serve an EVENT
//     playlist instead of VOD; see Session.maybeGovernLocked.

// SegMode is how one segment is produced.
type SegMode uint8

const (
	ModeCopy      SegMode = iota // stream copy (remux)
	ModeTranscode                // steady-state encode
	ModeFastStart                // quick low-latency opening encode (or short copy segment)
	ModeClamp                    // bitrate-clamped encode spliced into a copy stream
)

// adaptiveSettings is the session snapshot of the adaptive config fields,
// with zero numerics replaced by defaults.
type adaptiveSettings struct {
	FastStart          bool
	RampSegs           int
	RampDur            time.Duration
	FastStartMaxHeight int
	Ladder             bool
	VBV                bool
	VBVBufSeconds      int
	Governor           bool
	SpikeMargin        float64 // e.g. 1.25
}

func adaptiveFromConfig(c config.Transcode) adaptiveSettings {
	a := adaptiveSettings{
		FastStart:          c.FastStart,
		RampSegs:           c.FastStartSegments,
		RampDur:            c.FastStartSegment.D(),
		FastStartMaxHeight: c.FastStartMaxHeight,
		Ladder:             c.ABRLadder,
		VBV:                c.VBVReservoir,
		VBVBufSeconds:      c.VBVBufferSeconds,
		Governor:           c.SpikeGovernor,
		SpikeMargin:        float64(c.SpikeMarginPercent) / 100,
	}
	if a.RampSegs <= 0 {
		a.RampSegs = 2
	}
	if a.RampDur <= 0 {
		a.RampDur = 2 * time.Second
	}
	if a.FastStartMaxHeight <= 0 {
		a.FastStartMaxHeight = 720
	}
	if a.VBVBufSeconds <= 0 {
		a.VBVBufSeconds = 8
	}
	if a.SpikeMargin < 1 {
		a.SpikeMargin = 1.25
	}
	return a
}

// Timeline is a session's segment plan. Starts are relative to the
// session's playlist position zero (the HLS StartOffset); callers add base
// for absolute media time.
type Timeline struct {
	steady time.Duration // steady-state segment length
	span   time.Duration // known total span; 0 = open-ended
	durs   []time.Duration
	starts []time.Duration // len(durs)+1 prefix sums (closed timelines)
	modes  []SegMode
	// Ramp geometry, used to answer queries for open timelines and runs.
	rampSegs   int
	rampDur    time.Duration
	steadyMode SegMode
	// SwitchSeg is the first steady segment after the fast-start ramp
	// (== len(durs) when the whole title is ramp, 0 when there is none).
	SwitchSeg int
	// clamps holds governor-planned clamp bitrates (kbps) per segment.
	clamps map[int]int
}

type timelineOptions struct {
	steadyMode SegMode
	fastStart  bool
	rampSegs   int
	rampDur    time.Duration
	keyframes  []core.KeyframePoint // absolute media times, sorted
	base       time.Duration        // playlist position zero, absolute
	snap       bool                 // snap copy boundaries to keyframes
}

// buildTimeline plans a span (0 = unknown duration) at a steady segment
// length. With no fast-start and no snapping it reproduces the historic
// uniform segmentation (segmentCount/segmentLength) exactly, including
// folding a sub-quarter-segment remainder into the last segment.
func buildTimeline(span, steady time.Duration, o timelineOptions) *Timeline {
	if steady <= 0 {
		steady = DefaultSegmentDuration
	}
	tl := &Timeline{steady: steady, span: span, steadyMode: o.steadyMode}
	if o.fastStart {
		tl.rampSegs = o.rampSegs
		tl.rampDur = o.rampDur
		if tl.rampSegs <= 0 {
			tl.rampSegs = 2
		}
		if tl.rampDur <= 0 || tl.rampDur >= steady {
			tl.rampDur = steady / 2
		}
	}
	if span <= 0 {
		for i := 0; i < tl.rampSegs; i++ {
			tl.durs = append(tl.durs, tl.rampDur)
			tl.modes = append(tl.modes, ModeFastStart)
		}
		tl.SwitchSeg = tl.rampSegs
		return tl
	}
	// Relative keyframe times for snapping.
	var kfs []time.Duration
	if o.snap {
		for _, k := range o.keyframes {
			if rel := k.Time - o.base; rel > 0 && rel < span {
				kfs = append(kfs, rel)
			}
		}
		sort.Slice(kfs, func(i, j int) bool { return kfs[i] < kfs[j] })
	}
	switchAt := 0 // first segment produced in steady mode
	if tl.rampSegs > 0 {
		switchAt = tl.rampSegs
	}
	bounds := []time.Duration{0}
	for i := 0; bounds[len(bounds)-1] < span; i++ {
		last := bounds[len(bounds)-1]
		dur := steady
		if i < tl.rampSegs {
			dur = tl.rampDur
		}
		next := last + dur
		// Snap boundaries a copy stream must cut at: every boundary of a
		// pure copy timeline, and the ramp->copy switch onward.
		if len(kfs) > 0 && i >= switchAt-1 && next < span {
			if sn, ok := snapBoundary(kfs, next, last, steady); ok {
				next = sn
			}
		}
		if next >= span || span-next < steady/4 {
			next = span
		}
		bounds = append(bounds, next)
	}
	tl.starts = bounds
	for i := 0; i+1 < len(bounds); i++ {
		tl.durs = append(tl.durs, bounds[i+1]-bounds[i])
		if i < tl.rampSegs {
			tl.modes = append(tl.modes, ModeFastStart)
		} else {
			tl.modes = append(tl.modes, o.steadyMode)
		}
	}
	tl.SwitchSeg = tl.rampSegs
	if tl.SwitchSeg > len(tl.durs) {
		tl.SwitchSeg = len(tl.durs)
	}
	return tl
}

// snapBoundary moves a nominal boundary to the first keyframe at or after
// it, when that keeps the segment sane (at least 200ms of new media, at
// most one steady segment of drift).
func snapBoundary(kfs []time.Duration, next, last, steady time.Duration) (time.Duration, bool) {
	i := sort.Search(len(kfs), func(i int) bool { return kfs[i] >= next })
	if i >= len(kfs) {
		return 0, false
	}
	sn := kfs[i]
	if sn-last < 200*time.Millisecond || sn-next > steady {
		return 0, false
	}
	return sn, true
}

// Total is the number of segments; 0 when the duration is unknown.
func (tl *Timeline) Total() int {
	if tl == nil || tl.span <= 0 {
		return 0
	}
	return len(tl.durs)
}

// Start is segment n's offset from playlist zero.
func (tl *Timeline) Start(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	if tl.starts != nil {
		if n >= len(tl.starts) {
			return tl.starts[len(tl.starts)-1]
		}
		return tl.starts[n]
	}
	if n <= tl.rampSegs {
		return time.Duration(n) * tl.rampDur
	}
	return time.Duration(tl.rampSegs)*tl.rampDur + time.Duration(n-tl.rampSegs)*tl.steady
}

// Dur is segment n's playlist duration.
func (tl *Timeline) Dur(n int) time.Duration {
	if tl.span > 0 {
		if n >= 0 && n < len(tl.durs) {
			return tl.durs[n]
		}
		return tl.steady
	}
	if n < tl.rampSegs {
		return tl.rampDur
	}
	return tl.steady
}

// ModeAt is how segment n must be produced.
func (tl *Timeline) ModeAt(n int) SegMode {
	if kbps, ok := tl.clamps[n]; ok && kbps > 0 {
		return ModeClamp
	}
	if tl.span > 0 {
		if n >= 0 && n < len(tl.modes) {
			return tl.modes[n]
		}
		return tl.steadyMode
	}
	if n < tl.rampSegs {
		return ModeFastStart
	}
	return tl.steadyMode
}

// ClampKbps is the governor's bitrate ceiling for segment n (0 = none).
func (tl *Timeline) ClampKbps(n int) int { return tl.clamps[n] }

// SetClamp plans segment n as a clamped transcode (governor).
func (tl *Timeline) SetClamp(n, kbps int) {
	if tl.clamps == nil {
		tl.clamps = map[int]int{}
	}
	tl.clamps[n] = kbps
}

// TargetDuration is the HLS #EXT-X-TARGETDURATION: the ceiling of the
// longest segment duration.
func (tl *Timeline) TargetDuration() time.Duration {
	m := tl.steady
	if tl.rampDur > m {
		m = tl.rampDur
	}
	for _, d := range tl.durs {
		if d > m {
			m = d
		}
	}
	d := (m + time.Second - 1) / time.Second * time.Second
	if d < time.Second {
		d = time.Second
	}
	return d
}

// IndexAt is the segment covering playlist-relative time rel.
func (tl *Timeline) IndexAt(rel time.Duration) int {
	if rel <= 0 {
		return 0
	}
	if tl.starts != nil {
		i := sort.Search(len(tl.starts), func(i int) bool { return tl.starts[i] > rel }) - 1
		if i < 0 {
			return 0
		}
		if i >= len(tl.durs) {
			return len(tl.durs) - 1
		}
		return i
	}
	rampSpan := time.Duration(tl.rampSegs) * tl.rampDur
	if rel < rampSpan {
		if tl.rampDur <= 0 {
			return 0
		}
		return int(rel / tl.rampDur)
	}
	if tl.steady <= 0 {
		return tl.rampSegs
	}
	return tl.rampSegs + int((rel-rampSpan)/tl.steady)
}

// NextChange is the first segment after n produced in a different mode
// than n (the exclusive end of a run starting at n), or -1 when the mode
// runs to the end of what is known.
func (tl *Timeline) NextChange(n int) int {
	m := tl.ModeAt(n)
	limit := n + 4096 // bounded scan for open timelines
	if total := tl.Total(); total > 0 && total < limit {
		limit = total
	}
	for j := n + 1; j < limit; j++ {
		if tl.ModeAt(j) != m {
			return j
		}
	}
	if total := tl.Total(); total > 0 && limit == total {
		return -1
	}
	return -1
}

// SegmentPlan is one planned segment, for tests and admin reporting.
type SegmentPlan struct {
	Index               int
	Start               time.Duration // playlist-relative
	Duration            time.Duration
	Mode                SegMode
	DiscontinuityBefore bool
}

// Plan returns segment n's plan. discont reports discontinuities for the
// primary encode; rung playlists pass their own via playlist rendering.
func (tl *Timeline) Plan(n int) SegmentPlan {
	return SegmentPlan{Index: n, Start: tl.Start(n), Duration: tl.Dur(n), Mode: tl.ModeAt(n)}
}

// fastStartDecision turns a copy (remux) decision into the quick
// low-latency transcode used for the ramp segments of an MPEGTS copy
// session: same audio handling, video re-encoded at a capped height and
// bitrate in the source codec (h264 when the source codec is not one we
// encode quickly), so the first frame is produced without waiting for a
// full-rate copy segment (whose delivery needs the read-ahead buffer).
func fastStartDecision(d core.Decision, info core.MediaInfo, a adaptiveSettings) core.Decision {
	out := d
	out.Kind = core.DecisionTranscode
	out.VideoCopy = false
	v := info.Video()
	codec := "h264"
	srcH := 0
	if v != nil {
		srcH = v.Height
		if v.Codec == "h264" || v.Codec == "hevc" {
			codec = v.Codec
		}
	}
	out.VideoCodec = codec
	h := a.FastStartMaxHeight
	if srcH > 0 && srcH < h {
		h = srcH
	}
	if d.MaxHeight > 0 && d.MaxHeight < h {
		h = d.MaxHeight
	}
	out.MaxHeight = h
	audioKbps := 0
	if au := findStream(info, d.AudioStream, "audio"); au != nil {
		if d.AudioCopy {
			audioKbps = copiedAudioKbps(au)
		} else {
			audioKbps = audioBitrateKbps(d.AudioCodec, outputChannels(d.AudioCodec, au))
		}
	}
	out.BitrateKbps = bitrateCeiling(h, codec) + audioKbps
	// Never spend more bits on the ramp than the steady stream.
	if info.BitrateKbps > 0 && info.BitrateKbps < out.BitrateKbps {
		out.BitrateKbps = info.BitrateKbps
	}
	if v != nil && isHDR(v) && codec == "h264" {
		out.ToneMap = true
	}
	out.Reason = d.Reason + "; fast-start ramp encode"
	return out
}

// clampDecision turns a copy decision into the governor's clamped
// transcode: same resolution and audio, video re-encoded with a total
// bitrate ceiling of kbps.
func clampDecision(d core.Decision, info core.MediaInfo, kbps int) core.Decision {
	out := d
	out.Kind = core.DecisionTranscode
	out.VideoCopy = false
	v := info.Video()
	codec := "h264"
	if v != nil && (v.Codec == "h264" || v.Codec == "hevc") {
		codec = v.Codec
	}
	if out.VideoCodec == "" || d.VideoCopy {
		out.VideoCodec = codec
	}
	if kbps < 300 {
		kbps = 300
	}
	out.BitrateKbps = kbps
	if v != nil && isHDR(v) && out.VideoCodec == "h264" {
		out.ToneMap = true
	}
	out.Reason = d.Reason + "; VBR spike clamp"
	return out
}
