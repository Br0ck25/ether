package transcode

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestMPEG2CaptionWorkaroundOnlyForVideoToolboxH264Encode(t *testing.T) {
	for _, tt := range []struct {
		name, source, target, accel string
		copy, workaround            bool
	}{
		{"broadcast captions on hardware", "mpeg2video", "h264", AccelVideoToolbox, false, true},
		{"software keeps captions", "mpeg2video", "h264", AccelNone, false, false},
		{"H264 input keeps captions", "h264", "h264", AccelVideoToolbox, false, false},
		{"HEVC encoder unchanged", "mpeg2video", "hevc", AccelVideoToolbox, false, false},
		{"copy unchanged", "mpeg2video", "mpeg2video", AccelVideoToolbox, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "video", Codec: tt.source, Width: 1280, Height: 720}}}
			s := baseSpec(info, core.Decision{VideoCopy: tt.copy, VideoCodec: tt.target, Container: "mpegts", AudioStream: -1, SubtitleStream: -1, MaxHeight: 720})
			s.Accel = core.HWAccel{Name: tt.accel, Encoders: []string{"h264_videotoolbox", "hevc_videotoolbox"}}
			args := buildArgs(s)
			if got := argValue(args, "-a53cc"); (got == "0") != tt.workaround {
				t.Fatalf("caption injection setting = %q; args %s", got, joined(args))
			}
		})
	}
}
