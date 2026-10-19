package transcode

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestLiveClockCorrectionDoesNotChangeVODSeekCoordinates(t *testing.T) {
	for _, tt := range []struct {
		name            string
		duration, start time.Duration
		preserve        bool
	}{
		{"live input", 0, 0, false},
		{"movie beginning", time.Hour, 0, true},
		{"movie seek", time.Hour, 20 * time.Second, true},
		{"unknown length seek", 0, 20 * time.Second, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := core.MediaInfo{Duration: tt.duration, Streams: []core.StreamInfo{{Index: 0, Type: "video", Codec: "h264"}}}
			s := baseSpec(info, core.Decision{VideoCopy: true, AudioCopy: true, Container: "mpegts", AudioStream: -1, SubtitleStream: -1})
			s.Start = tt.start
			args := buildArgs(s)
			if tt.preserve {
				mustContain(t, args, "-start_at_zero -copyts")
			} else {
				mustNotContain(t, args, "-copyts", "-start_at_zero")
			}
			if tt.start > 0 && argValue(args[:inputIndex(args)], "-ss") != "20.000" {
				t.Fatal("seek lost input position")
			}
		})
	}
}
