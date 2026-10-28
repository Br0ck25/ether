package transcode

import (
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestGPUFramePipeline(t *testing.T) {
	for _, tc := range []struct {
		name, accel, filter, frameFormat string
	}{
		{"nvidia", AccelNVENC, "scale_cuda", "cuda"},
		{"apple", AccelVideoToolbox, "scale_vt", "videotoolbox_vld"},
		{"intel", AccelQSV, "vpp_qsv", "qsv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := mustParse(t, "mp4_h264_aac.json")
			opts := allow()
			opts.MaxHeight = 720
			s := baseSpec(info, Decide(info, appleTV, opts, Policy{}))
			s.Accel = core.HWAccel{Name: tc.accel, Encoders: []string{"h264_" + tc.accel, "hevc_" + tc.accel}}
			s.Features.hwaccels = map[string]bool{"cuda": true, "qsv": true, "videotoolbox": true}
			s.Features.filters[tc.filter] = true
			args := buildArgs(s)
			mustContain(t, args, "-hwaccel_output_format "+tc.frameFormat, tc.filter+"=w=1280:h=720:format=nv12")
			mustNotContain(t, args, "hwdownload", "hwupload", "scale=", ",format=yuv420p")

			// A custom/older FFmpeg missing the device scaler must keep a
			// usable software frame graph instead of failing at startup.
			delete(s.Features.filters, tc.filter)
			mustNotContain(t, buildArgs(s), "-hwaccel_output_format", tc.filter+"=")
		})
	}
}

func TestGPUHDRPipelineAndFallback(t *testing.T) {
	for _, accel := range []string{AccelNVENC, AccelVideoToolbox} {
		t.Run(accel, func(t *testing.T) {
			info := mustParse(t, "mkv_hevc_hdr10.json")
			s := baseSpec(info, Decide(info, plexWeb, allow(), Policy{}))
			s.Accel = core.HWAccel{Name: accel, Encoders: []string{"h264_" + accel, "hevc_" + accel}}
			s.Features.hwaccels = map[string]bool{"cuda": true, "videotoolbox": true}
			s.Features.filters = map[string]bool{"scale_cuda": true, "scale_vt": true, "tonemap_cuda": true, "tonemap_videotoolbox": true, "zscale": true, "tonemap": true}
			args := buildArgs(s)
			filter := "tonemap_cuda"
			if accel == AccelVideoToolbox {
				filter = "tonemap_videotoolbox"
			}
			mustContain(t, args, filter+"=tonemap=hable:format=nv12:t=bt709:m=bt709:p=bt709")
			mustNotContain(t, args, "zscale=", "hwdownload")

			// HDR passthrough preserves ten-bit pictures and does not tone
			// map just because a hardware filter is available.
			s.Decision.VideoCodec = "hevc"
			s.Decision.ToneMap = false
			args = buildArgs(s)
			mustContain(t, args, "format=p010le", "-profile:v main10")
			mustNotContain(t, args, "tonemap_")
		})
	}
}

func TestGPUPipelineDoesNotDiscardSubtitles(t *testing.T) {
	info := mustParse(t, "mkv_pgs.json")
	opts := allow()
	opts.SubtitleStream = 2
	s := baseSpec(info, Decide(info, appleTV, opts, Policy{}))
	s.Accel = core.HWAccel{Name: AccelNVENC, Encoders: []string{"h264_nvenc", "hevc_nvenc"}}
	s.Features.hwaccels = map[string]bool{"cuda": true}
	s.Features.filters["scale_cuda"] = true
	args := buildArgs(s)
	if !strings.Contains(argValue(args, "-filter_complex"), "overlay") {
		t.Fatal("hardware pipeline dropped bitmap subtitles")
	}
	mustNotContain(t, args, "-hwaccel_output_format cuda", "scale_cuda=")
}

func TestVideoToolboxScalerOptionCompatibility(t *testing.T) {
	info := mustParse(t, "mp4_h264_aac.json")
	opts := allow()
	opts.MaxHeight = 720
	s := baseSpec(info, Decide(info, appleTV, opts, Policy{}))
	s.Accel = core.HWAccel{Name: AccelVideoToolbox, Encoders: []string{"h264_videotoolbox"}}
	s.Features.scaleVTFormat = false
	args := buildArgs(s)
	mustContain(t, args, "scale_vt=w=1280:h=720[vout]", "-allow_sw 0")
	mustNotContain(t, args, "scale_vt=w=1280:h=720:format=", "hwdownload")
	// A filter name alone does not prove support for the format option.
	for _, tc := range []struct {
		help string
		want bool
	}{
		{"format <pix_fmt> Output format", true},
		{"w <string> Output width\nh <string> Output height", false},
		{"Unknown filter 'scale_vt'\nformat in explanatory prose", false},
	} {
		if got := filterHasOption([]byte(tc.help), "format"); got != tc.want {
			t.Errorf("filterHasOption(%q) = %v, want %v", tc.help, got, tc.want)
		}
	}
}

func TestHigh10KeepsNVENCWithoutCUDADecoding(t *testing.T) {
	info := mustParse(t, "mp4_h264_aac.json")
	for i := range info.Streams {
		if info.Streams[i].Type == "video" {
			info.Streams[i].BitDepth = 10
		}
	}
	opts := allow()
	opts.MaxHeight = 720
	s := baseSpec(info, Decide(info, appleTV, opts, Policy{}))
	s.Accel = core.HWAccel{Name: AccelNVENC, Encoders: []string{"h264_nvenc"}}
	s.Features.hwaccels = map[string]bool{"cuda": true}
	s.Features.filters["scale_cuda"] = true
	args := buildArgs(s)
	mustContain(t, args, "-c:v h264_nvenc", "scale=1280:720")
	mustNotContain(t, args, "-hwaccel cuda", "-hwaccel_output_format cuda", "scale_cuda=")
}
