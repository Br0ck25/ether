package transcode

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// runSpec is everything needed to build one ffmpeg HLS run.
type runSpec struct {
	LiveInput    bool
	InputHeaders map[string]string
	Input        string
	Info         core.MediaInfo
	Decision     core.Decision
	// Start is the media timestamp of the run's first segment (StartSeg).
	Start    time.Duration
	SegDur   time.Duration
	StartSeg int
	Dir      string
	Playlist string // ffmpeg's own playlist file name, in Dir
	InitName string // fMP4 init segment file name, in Dir
	// Accel is the hardware to use; Name AccelNone (or "") means CPU.
	Accel    core.HWAccel
	Features features
	// BigProbe raises probesize/analyzeduration (retry after a failed start).
	BigProbe bool
	// Protocol is core.ProtocolHLS or core.ProtocolDASH. For DASH, Dir is
	// the run's own directory and Playlist ffmpeg's manifest in it.
	Protocol string
	// ChunkDur is the DASH muxer's chunk length (default 1s).
	ChunkDur time.Duration
	// FastStart selects the low-latency encode (fastest presets) used for
	// the fast-start ramp segments.
	FastStart bool
	// Rate is the quality-targeted VBV rate control for this run; the zero
	// value keeps the historic bitrate-target encoding.
	Rate rateControl
	// ChunkDir, when set, makes an HLS run cut at (nearly) every keyframe
	// into this directory, numbered from 0, for the session to regroup by
	// timestamp (chunks.go). ChunkCut is the muxer's segment length then.
	ChunkDir string
	ChunkCut time.Duration
	// KeyTimes, when set, are the output timestamps a video encode forces
	// keyframes at (a chunked run's plan boundaries) instead of every
	// SegDur.
	KeyTimes []time.Duration
}

// segmentExt is the segment file extension for a decision's container.
func segmentExt(container string) string {
	if container == ContainerMatroska {
		return "mkv"
	}
	if container == ContainerFMP4 {
		return "m4s"
	}
	return "ts"
}

// segmentFile is the on-disk name of segment n.
func segmentFile(n int, ext string) string { return fmt.Sprintf("%05d.%s", n, ext) }

func secs(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

// usesHW reports whether the run encodes video on hardware.
func (s runSpec) usesHW() bool {
	if s.Decision.VideoCopy || s.Accel.Name == "" || s.Accel.Name == AccelNone {
		return false
	}
	enc, hw := videoEncoder(s.Decision.VideoCodec, s.Accel, s.Features)
	return hw && enc != ""
}

// videoEncoder picks the encoder for a target codec: the confirmed hardware
// encoder when there is one, else libx264/libx265.
func videoEncoder(codec string, accel core.HWAccel, f features) (name string, hw bool) {
	if codec == "" {
		codec = "h264"
	}
	if accel.Name != "" && accel.Name != AccelNone {
		suffix := accel.Name
		hwName := codec + "_" + suffix
		if contains(accel.Encoders, hwName) {
			return hwName, true
		}
	}
	switch codec {
	case "hevc":
		if f.encoders == nil || f.hasEncoder("libx265") {
			return "libx265", false
		}
		return "libx264", false // no HEVC encoder at all: caller asked for the impossible
	default:
		return "libx264", false
	}
}

// buildArgs assembles the ffmpeg command line for a run: HLS, or DASH
// when the spec says so.
func buildArgs(s runSpec) []string {
	args := buildCodecArgs(s)
	if s.Protocol == core.ProtocolDASH {
		return append(args, dashMuxArgs(s)...)
	}
	return append(args, hlsMuxArgs(s)...)
}

// buildCodecArgs is the input, stream mapping and codec part of a run's
// command line, shared by HLS and DASH.
func buildCodecArgs(s runSpec) []string {
	d := s.Decision
	v := s.Info.Video()
	a := findStream(s.Info, d.AudioStream, "audio")
	segDur := s.SegDur
	if segDur <= 0 {
		segDur = DefaultSegmentDuration
	}
	container := d.Container
	if container != ContainerFMP4 {
		container = ContainerMPEGTS
	}
	transcodeVideo := v != nil && !d.VideoCopy

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-nostats", "-progress", "pipe:1", "-y"}
	if transcodeVideo {
		if _, hw := videoEncoder(d.VideoCodec, s.Accel, s.Features); !hw {
			w, h := outputSize(v.Width, v.Height, d.MaxHeight)
			args = append(args, "-filter_complex_threads", strconv.Itoa(min(4, softwareThreadBudget(w, h, runtime.GOMAXPROCS(0)))))
		}
	}

	// ---- input
	args = append(args, inputSafetyArgs(s.Input, s.LiveInput)...)
	args = append(args, httpInputArgs(s.Input, true)...)
	args = appendInputHeaders(args, s.InputHeaders)
	if s.BigProbe {
		args = append(args, "-probesize", "50000000", "-analyzeduration", "10000000")
	} else {
		args = append(args, "-probesize", "5000000", "-analyzeduration", "2000000")
	}
	if !transcodeVideo {
		args = append(args, "-fflags", "+genpts")
	}
	vp := planVideo(s, v)
	args = append(args, vp.inputArgs...)
	args = append(args, inputTimestampArgs(s)...)
	if s.Start > 0 {
		args = append(args, "-ss", secs(s.Start))
	}
	args = append(args, "-i", s.Input)

	// ---- mapping
	if v != nil {
		if transcodeVideo && vp.graph != "" {
			args = append(args, "-filter_complex", vp.graph, "-map", "[vout]")
		} else {
			args = append(args, "-map", fmt.Sprintf("0:%d", v.Index))
		}
	}
	if a != nil {
		args = append(args, "-map", fmt.Sprintf("0:%d", a.Index))
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn")

	// ---- video
	if v != nil {
		if d.VideoCopy {
			args = append(args, "-c:v", "copy")
			if d.StripDolbyVision {
				args = append(args, "-bsf:v", "dovi_rpu=strip=1")
			}
			if container == ContainerFMP4 && v.Codec != "hevc" {
				tag := "0"
				if v.Codec == "h264" {
					tag = "avc1"
				}
				args = append(args, "-tag:v", tag)
			}
			if v.Codec == "hevc" && container == ContainerFMP4 {
				tag := "hvc1"
				if v.DolbyVision && v.DolbyVisionProfile == 5 {
					tag = "dvh1"
				}
				args = append(args, "-tag:v", tag)
			}
			if v.DolbyVision && !d.StripDolbyVision && container == ContainerFMP4 {
				args = append(args, "-strict", "unofficial") // write the dvcC box
			}
		} else {
			args = append(args, vp.encodeArgs...)
			args = append(args, videotoolboxCaptionArgs(v, vp.encodeArgs)...)
			if len(s.KeyTimes) > 0 {
				keys := make([]string, len(s.KeyTimes))
				for i, k := range s.KeyTimes {
					keys[i] = secs(k)
				}
				// Absolute output times (unlike the expression's t, which
				// counts from the run's first frame).
				args = append(args, "-force_key_frames", strings.Join(keys, ","))
			} else {
				args = append(args, "-force_key_frames", "expr:gte(t,n_forced*"+secs(segDur)+")")
			}
		}
	}

	// ---- audio
	if a != nil {
		if d.AudioCopy {
			args = append(args, "-c:a", "copy")
			if s.Start > 0 {
				// Input seeking retains copied packets from the preceding
				// video keyframe. Drop audio preroll without re-encoding or
				// discarding the video keyframe required to decode a copy
				// rendition. amount=0 explicitly leaves retained bytes intact.
				args = append(args, "-bsf:a", "noise=amount=0:drop='lt(pts*tb,"+secs(s.Start)+")'")
			}
			if container == ContainerFMP4 {
				tag := "0"
				if a.Codec == "aac" {
					tag = "mp4a"
				}
				args = append(args, "-tag:a", tag)
			}
		} else {
			codec := d.AudioCodec
			if codec == "" {
				codec = "aac"
			}
			ch := decisionAudioChannels(d, a)
			enc := codec
			switch codec {
			case "mp3":
				enc = "libmp3lame"
			case "opus":
				enc = "libopus"
				if s.Features.encoders != nil && !s.Features.hasEncoder("libopus") {
					enc = "opus"
				}
			}
			args = append(args, "-c:a", enc, "-ac", strconv.Itoa(ch), "-ar", strconv.Itoa(decisionAudioRate(d)),
				"-b:a", fmt.Sprintf("%dk", audioBitrateKbps(codec, ch)))
			if len(d.AudioFilters) > 0 {
				args = append(args, "-af", strings.Join(d.AudioFilters, ","))
			}
			if enc == "opus" {
				args = append(args, "-strict", "-2")
			}
		}
	}

	return args
}

// hlsMuxArgs is the HLS muxer part of a run's command line.
func hlsMuxArgs(s runSpec) []string {
	segDur := s.SegDur
	if segDur <= 0 {
		segDur = DefaultSegmentDuration
	}
	container := s.Decision.Container
	if container == ContainerMatroska {
		return matroskaHLSMuxArgs(s, segDur)
	}
	if container != ContainerFMP4 {
		container = ContainerMPEGTS
	}
	ext := segmentExt(container)
	start, segDir := s.StartSeg, s.Dir
	if s.ChunkDir != "" {
		segDur, start, segDir = s.chunkCut(), 0, s.ChunkDir
	}
	args := []string{
		"-avoid_negative_ts", "disabled",
		"-max_muxing_queue_size", "4096",
		"-f", "hls",
		"-hls_time", secs(segDur),
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "temp_file",
		"-start_number", strconv.Itoa(start),
	}
	if container == ContainerFMP4 {
		// frag_discont: every fragment's decode time is the media time
		// (with -copyts) rather than counted from the run's first packet,
		// so segments of a run restarted mid-playlist line up with the
		// init segment the player already holds.
		args = append(args, "-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", s.InitName,
			"-hls_segment_options", "movflags=+frag_discont")
	} else {
		args = append(args, "-hls_segment_type", "mpegts")
	}
	args = append(args,
		"-hls_segment_filename", filepath.Join(segDir, "%05d."+ext),
		filepath.Join(s.Dir, s.Playlist),
	)
	return args
}

// chunkCut is a chunked run's muxer segment length: short enough that the
// muxer cuts at every keyframe (it cuts at the first keyframe past
// start+k*length, k counting cuts), long enough that all-intra sources do
// not produce a file per frame.
func (s runSpec) chunkCut() time.Duration {
	if s.ChunkCut > 0 {
		return s.ChunkCut
	}
	return chunkCutFor(s.SegDur)
}

// dashMuxArgs is the DASH muxer part of a run's command line: fMP4 chunks
// per representation (video 0, audio 1) with absolute decode times
// (frag_discont), written into the run directory as
// chunk-<rep>-<n>.m4s from n=1 with init-<rep>.m4s.
func dashMuxArgs(s runSpec) []string {
	chunk := s.ChunkDur
	if chunk <= 0 {
		chunk = dashChunkDuration
	}
	var sets []string
	if s.Info.Video() != nil {
		sets = append(sets, fmt.Sprintf("id=%d,streams=v", len(sets)))
	}
	if findStream(s.Info, s.Decision.AudioStream, "audio") != nil {
		sets = append(sets, fmt.Sprintf("id=%d,streams=a", len(sets)))
	}
	return []string{
		"-avoid_negative_ts", "disabled",
		"-max_muxing_queue_size", "4096",
		"-f", "dash",
		"-dash_segment_type", "mp4",
		"-format_options", "movflags=+frag_discont",
		"-use_template", "1",
		"-use_timeline", "0",
		"-seg_duration", secs(chunk),
		"-window_size", "0",
		"-adaptation_sets", strings.Join(sets, " "),
		"-init_seg_name", dashInitPattern,
		"-media_seg_name", dashChunkPattern,
		// The dash muxer places segment files in the manifest's
		// directory, which it finds by scanning the output path for
		// '/'. A Windows path with backslashes has none, so segments
		// landed in ffmpeg's working directory instead of the run
		// directory. Forward slashes work in Windows paths too.
		filepath.ToSlash(filepath.Join(s.Dir, s.Playlist)),
	}
}

// File names ffmpeg's dash muxer writes in a run directory.
const (
	dashInitPattern  = "init-$RepresentationID$.m4s"
	dashChunkPattern = "chunk-$RepresentationID$-$Number%05d$.m4s"
)

func dashInitFile(rep int) string     { return fmt.Sprintf("init-%d.m4s", rep) }
func dashChunkFile(rep, n int) string { return fmt.Sprintf("chunk-%d-%05d.m4s", rep, n) }

func findStream(info core.MediaInfo, idx int, typ string) *core.StreamInfo {
	if idx < 0 {
		return nil
	}
	for i := range info.Streams {
		if info.Streams[i].Index == idx && info.Streams[i].Type == typ {
			return &info.Streams[i]
		}
	}
	return nil
}

// videoPlan is the decode, filter and encode part of a video transcode.
type videoPlan struct {
	inputArgs  []string
	graph      string // -filter_complex, output label [vout]
	encodeArgs []string
}

// outputSize scales (w,h) to height maxH, keeping aspect and even sizes.
func outputSize(w, h, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		if maxH > 0 {
			return even(maxH * 16 / 9), even(maxH)
		}
		return 0, 0
	}
	if maxH <= 0 || maxH >= h {
		return even(w), even(h)
	}
	nw := int(float64(w)*float64(maxH)/float64(h) + 0.5)
	return even(nw), even(maxH)
}

func even(n int) int {
	if n%2 != 0 {
		n--
	}
	if n < 2 {
		n = 2
	}
	return n
}

// cpuToneMap converts PQ/HLG to SDR BT.709 in software. zscale (zimg) does
// the transfer conversions; without it there is no correct CPU path, so the
// fallback only retags the colours (playable, but washed out).
func cpuToneMap(f features) string {
	if f.filters == nil || f.hasFilter("zscale") {
		return "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv"
	}
	return "scale=out_color_matrix=bt709:out_range=tv,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709"
}

// escapeFilterValue escapes a value for use inside a filter option and then
// inside a filtergraph description.
func escapeFilterValue(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `:`, `\:`)
	v = r.Replace(v)
	g := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `[`, `\[`, `]`, `\]`, `,`, `\,`, `;`, `\;`)
	return g.Replace(v)
}

// subtitleOrdinal is the position of a stream among the subtitle streams
// (the si= option of the subtitles filter).
func subtitleOrdinal(info core.MediaInfo, idx int) int {
	n := 0
	for _, s := range info.Streams {
		if s.Type != "subtitle" || s.External {
			continue
		}
		if s.Index == idx {
			return n
		}
		n++
	}
	return 0
}

// planVideo builds decode/filter/encode arguments for a video transcode.
func planVideo(s runSpec, v *core.StreamInfo) videoPlan {
	d := s.Decision
	var p videoPlan
	if v == nil || d.VideoCopy {
		return p
	}
	codec := d.VideoCodec
	if codec == "" {
		codec = "h264"
	}
	enc, hw := videoEncoder(codec, s.Accel, s.Features)
	if !hw {
		p.inputArgs = append(p.inputArgs, "-threads", strconv.Itoa(softwareThreadBudget(v.Width, v.Height, runtime.GOMAXPROCS(0))))
	}
	accel := AccelNone
	if hw {
		accel = s.Accel.Name
	}
	w, h := outputSize(v.Width, v.Height, d.MaxHeight)
	scaling := v.Height > 0 && h < v.Height
	// Keep HDR only where HEVC can carry the decoded picture's HDR10/HLG;
	// any other HDR picture is tone mapped (8-bit SDR output), and an SDR
	// picture (a Dolby Vision SDR base layer) never is.
	keepHDR := keepsHDR(v) && !d.ToneMap && codec == "hevc"
	tonemap := encodedRange(v) != rangeSDR && !keepHDR
	sub := findStream(s.Info, d.SubtitleStream, "subtitle")
	burn := d.BurnSubtitles && sub != nil

	// Software pixel format the encoder receives.
	swFmt := "yuv420p"
	if keepHDR {
		swFmt = "yuv420p10le"
		if hw {
			swFmt = "p010le"
		}
	} else if accel == AccelQSV || accel == AccelVAAPI {
		swFmt = "nv12"
	}

	in := fmt.Sprintf("[0:%d]", v.Index)
	var chain []string
	hwFrames := false
	gpuInput, gpuFilters, gpuOK := gpuPipeline(s, accel, w, h, tonemap, keepHDR, burn)
	switch {
	case gpuOK:
		p.inputArgs = append(p.inputArgs, gpuInput...)
		chain = append(chain, gpuFilters...)
		hwFrames = true
	case accel == AccelVideoToolbox && tonemap && !burn && s.Features.hasFilter("scale_vt"):
		// Decode, tone-map and scale on the GPU.
		p.inputArgs = append(p.inputArgs, "-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld")
		chain = append(chain, fmt.Sprintf("scale_vt=w=%d:h=%d:color_matrix=bt709:color_primaries=bt709:color_transfer=bt709", w, h))
		hwFrames = true
	case accel == AccelVAAPI && !burn && (!tonemap || s.Features.hasFilter("tonemap_vaapi")):
		dev := s.Accel.Device
		if dev == "" {
			dev = DefaultVAAPIDevice
		}
		p.inputArgs = append(p.inputArgs, "-hwaccel", "vaapi", "-hwaccel_device", dev, "-hwaccel_output_format", "vaapi")
		if tonemap {
			chain = append(chain, "tonemap_vaapi=format=nv12:t=bt709:m=bt709:p=bt709")
		}
		f := "nv12"
		if keepHDR {
			f = "p010"
		}
		chain = append(chain, fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=%s", w, h, f))
		hwFrames = true
	case accel == AccelVideoToolbox:
		p.inputArgs = append(p.inputArgs, "-hwaccel", "videotoolbox")
	case accel == AccelNVENC && !(v.Codec == "h264" && v.BitDepth > 8):
		p.inputArgs = append(p.inputArgs, "-hwaccel", "cuda")
	case accel == AccelVAAPI:
		dev := s.Accel.Device
		if dev == "" {
			dev = DefaultVAAPIDevice
		}
		p.inputArgs = append(p.inputArgs, "-vaapi_device", dev)
	}

	if !hwFrames {
		if tonemap {
			chain = append(chain, cpuToneMap(s.Features))
		}
		if scaling && !burn {
			chain = append(chain, fmt.Sprintf("scale=%d:%d", w, h))
		}
		if !burn {
			chain = append(chain, "format="+swFmt)
			if accel == AccelVAAPI {
				chain = append(chain, "hwupload")
			}
		}
	}

	var graph string
	switch {
	case burn && IsImageSubtitle(sub.Codec) && !sub.External:
		pre := strings.Join(chain, ",")
		if pre == "" {
			pre = "null"
		}
		post := []string{}
		if scaling {
			post = append(post, fmt.Sprintf("scale=%d:%d", w, h))
		}
		post = append(post, "format="+swFmt)
		if accel == AccelVAAPI {
			post = append(post, "hwupload")
		}
		subW, subH := v.Width, v.Height
		subScale := "null"
		if subW > 0 && subH > 0 {
			subScale = fmt.Sprintf("scale=%d:%d", subW, subH)
		}
		graph = fmt.Sprintf("%s%s[base];[0:%d]%s[sub];[base][sub]overlay=eof_action=pass,%s[vout]",
			in, pre, sub.Index, subScale, strings.Join(post, ","))
	case burn && (s.Features.filters == nil || s.Features.hasFilter("subtitles")):
		src := s.Input
		opt := ""
		if sub.External && sub.ExternalURL != "" {
			src = sub.ExternalURL
		} else {
			opt = fmt.Sprintf(":si=%d", subtitleOrdinal(s.Info, sub.Index))
		}
		chain = append(chain, fmt.Sprintf("subtitles=filename=%s%s", escapeFilterValue(src), opt))
		if scaling {
			chain = append(chain, fmt.Sprintf("scale=%d:%d", w, h))
		}
		chain = append(chain, "format="+swFmt)
		if accel == AccelVAAPI {
			chain = append(chain, "hwupload")
		}
		graph = in + strings.Join(chain, ",") + "[vout]"
	default:
		if burn {
			// Text burn-in needs libass (the subtitles filter); without it
			// the client gets the subtitle as a sidecar instead.
			chain = appendScaleFormat(chain, scaling, w, h, swFmt, accel, hwFrames)
		}
		if len(chain) == 0 {
			chain = []string{"null"}
		}
		graph = in + strings.Join(chain, ",") + "[vout]"
	}
	p.graph = graph

	// Encoder.
	videoKbps := videoBitrate(s.Info, d)
	if s.Rate.TargetKbps > 0 {
		videoKbps = s.Rate.TargetKbps
	}
	b := fmt.Sprintf("%dk", videoKbps)
	maxr := fmt.Sprintf("%dk", videoKbps*3/2)
	buf := fmt.Sprintf("%dk", videoKbps*2)
	if s.Rate.MaxrateKbps > 0 {
		maxr = fmt.Sprintf("%dk", s.Rate.MaxrateKbps)
	}
	if s.Rate.BufsizeKbps > 0 {
		buf = fmt.Sprintf("%dk", s.Rate.BufsizeKbps)
	}
	e := []string{"-c:v", enc}
	xPreset := "veryfast"
	if s.FastStart {
		xPreset = "ultrafast"
	}
	switch enc {
	case "libx264":
		crf := "21"
		threads := strconv.Itoa(softwareThreadBudget(w, h, runtime.GOMAXPROCS(0)))
		if s.Rate.Quality {
			e = append(e, "-preset", xPreset, "-crf", crf, "-maxrate", maxr, "-bufsize", buf, "-profile:v", "high", "-threads", threads)
		} else {
			e = append(e, "-preset", xPreset, "-crf", crf, "-maxrate", b, "-bufsize", buf, "-profile:v", "high", "-threads", threads)
		}
		if s.FastStart {
			e = append(e, "-tune", "zerolatency")
		}
	case "libx265":
		params := "log-level=error"
		if keepHDR {
			params += hdrX265Params(v)
		}
		peak := b
		if s.Rate.Quality {
			peak = maxr
		}
		e = append(e, "-preset", xPreset, "-crf", "23", "-maxrate", peak, "-bufsize", buf, "-x265-params", params)
	case "h264_videotoolbox", "hevc_videotoolbox":
		if s.Rate.Quality {
			e = append(e, "-q:v", "65", "-maxrate", maxr, "-bufsize", buf, "-allow_sw", "0")
		} else {
			e = append(e, "-b:v", b, "-maxrate", maxr, "-bufsize", buf, "-allow_sw", "0")
		}
		if s.FastStart {
			e = append(e, "-realtime", "1")
		}
		if enc == "h264_videotoolbox" {
			e = append(e, "-profile:v", "high")
		} else if keepHDR {
			e = append(e, "-profile:v", "main10")
		}
	case "h264_nvenc", "hevc_nvenc":
		preset := "p4"
		if s.FastStart {
			preset = "p1"
		}
		if s.Rate.Quality {
			e = append(e, "-preset", preset, "-rc", "vbr", "-cq", "21", "-maxrate", maxr, "-bufsize", buf, "-forced-idr", "1")
		} else {
			e = append(e, "-preset", preset, "-rc", "vbr", "-b:v", b, "-maxrate", maxr, "-bufsize", buf, "-forced-idr", "1")
		}
		if enc == "h264_nvenc" {
			e = append(e, "-profile:v", "high")
		} else if keepHDR {
			e = append(e, "-profile:v", "main10")
		}
	case "h264_qsv", "hevc_qsv":
		if s.Rate.Quality {
			e = append(e, "-preset", "veryfast", "-global_quality", "23", "-maxrate", maxr, "-bufsize", buf, "-look_ahead", "1")
		} else {
			e = append(e, "-preset", "veryfast", "-b:v", b, "-maxrate", maxr, "-bufsize", buf, "-look_ahead", "0")
		}
		if enc == "h264_qsv" {
			e = append(e, "-profile:v", "high")
		} else if keepHDR {
			e = append(e, "-profile:v", "main10")
		}
	case "h264_vaapi", "hevc_vaapi":
		if s.Rate.Quality {
			e = append(e, "-rc_mode", "QVBR", "-global_quality", "23", "-maxrate", maxr, "-bufsize", buf)
		} else {
			e = append(e, "-rc_mode", "VBR", "-b:v", b, "-maxrate", maxr, "-bufsize", buf)
		}
		if enc == "h264_vaapi" {
			e = append(e, "-profile:v", "high")
		} else if keepHDR {
			e = append(e, "-profile:v", "main10")
		}
	}
	if codec == "hevc" {
		e = append(e, "-tag:v", "hvc1")
	}
	// Tag the output colours. Not with GPU frames: the hardware filters set
	// them, and output colour options make ffmpeg insert a software
	// conversion that cannot take hardware frames.
	switch {
	case hwFrames:
	case keepHDR:
		trc := "smpte2084"
		if v.ColorTransfer == "arib-std-b67" {
			trc = "arib-std-b67"
		}
		e = append(e, "-color_primaries", "bt2020", "-color_trc", trc, "-colorspace", "bt2020nc")
	default:
		e = append(e, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}
	p.encodeArgs = e
	return p
}

func hdrX265Params(v *core.StreamInfo) string {
	trc := "smpte2084"
	if v.ColorTransfer == "arib-std-b67" {
		trc = "arib-std-b67"
	}
	return ":hdr10-opt=1:repeat-headers=1:colorprim=bt2020:colormatrix=bt2020nc:transfer=" + trc
}

// videoBitrate is the video share of a transcode decision's bitrate.
func videoBitrate(info core.MediaInfo, d core.Decision) int {
	audio := 0
	if a := findStream(info, d.AudioStream, "audio"); a != nil {
		if d.AudioCopy {
			audio = copiedAudioKbps(a)
		} else {
			audio = audioBitrateKbps(d.AudioCodec, decisionAudioChannels(d, a))
		}
	}
	k := d.BitrateKbps - audio
	if d.BitrateKbps <= 0 {
		k = bitrateCeiling(d.MaxHeight, d.VideoCodec)
	}
	if k < 200 {
		k = 200
	}
	return k
}

// appendScaleFormat adds the scale/format/upload tail that the burn-in
// branches normally add themselves.
func appendScaleFormat(chain []string, scaling bool, w, h int, swFmt, accel string, hwFrames bool) []string {
	if hwFrames {
		return chain
	}
	if scaling {
		chain = append(chain, fmt.Sprintf("scale=%d:%d", w, h))
	}
	chain = append(chain, "format="+swFmt)
	if accel == AccelVAAPI {
		chain = append(chain, "hwupload")
	}
	return chain
}

// subtitleArgs extracts one embedded text subtitle stream as WebVTT on
// stdout.
func subtitleArgs(input string, streamIndex int) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	args = append(args, inputSafetyArgs(input, false)...)
	args = append(args, httpInputArgs(input, true)...)
	return append(args, "-i", input, "-map", fmt.Sprintf("0:%d", streamIndex), "-c:s", "webvtt", "-f", "webvtt", "pipe:1")
}
