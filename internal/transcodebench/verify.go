package transcodebench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type ffprobeDoc struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Profile   string `json:"profile"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Channels  int    `json:"channels"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

// VerifyOutput probes a retained output file and checks the actual video
// codec/dimensions against the decision. Pure checking lives in checkOutput
// so it can be unit-tested without ffprobe.
func verifyOutput(ctx context.Context, ffprobe, path string, dec core.Decision, cfg Config) OutputProbe {
	op := OutputProbe{}
	if ffprobe == "" {
		op.Error = "no ffprobe path from Manager"
		op.Detail = "ffprobe unavailable"
		return op
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-hide_banner", "-loglevel", "error",
		"-show_streams", "-show_format", "-of", "json", path)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		op.Error = fmt.Sprintf("%v: %s", err, strings.TrimSpace(truncate(errb.String(), 300)))
		op.Detail = "ffprobe failed on retained output"
		return op
	}
	var doc ffprobeDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		op.Error = err.Error()
		op.Detail = "parse ffprobe output"
		return op
	}
	op.Container = doc.Format.FormatName
	if doc.Format.Duration != "" {
		var f float64
		if _, err := fmt.Sscanf(doc.Format.Duration, "%g", &f); err == nil {
			op.DurationSec = f
		}
	}
	for _, s := range doc.Streams {
		op.Streams = append(op.Streams, StreamSummary{Type: s.CodecType, Codec: s.CodecName,
			Profile: s.Profile, Width: s.Width, Height: s.Height, Channels: s.Channels})
	}
	op.Verified, op.Detail = checkOutput(op, dec, cfg)
	return op
}

// checkOutput verifies codec and height against the decision. For a remux
// the expected codec is the copied source codec carried in the decision.
func checkOutput(op OutputProbe, dec core.Decision, cfg Config) (bool, string) {
	var v *StreamSummary
	for i := range op.Streams {
		if op.Streams[i].Type == "video" {
			v = &op.Streams[i]
			break
		}
	}
	if v == nil {
		// Audio-only output is acceptable only when the decision had no video.
		if dec.VideoCodec == "" {
			return true, "no video stream (audio-only decision)"
		}
		return false, "output has no video stream"
	}
	want := strings.ToLower(dec.VideoCodec)
	if want != "" && !strings.EqualFold(v.Codec, want) {
		return false, fmt.Sprintf("output video codec %q != expected %q", v.Codec, want)
	}
	maxH := dec.MaxHeight
	if cfg.Mode == ModeTranscode && cfg.MaxHeight > 0 {
		maxH = cfg.MaxHeight
	}
	if maxH > 0 && v.Height > maxH {
		return false, fmt.Sprintf("output height %d exceeds max-height %d", v.Height, maxH)
	}
	if v.Width <= 0 || v.Height <= 0 {
		return false, "output video has no dimensions"
	}
	return true, fmt.Sprintf("video %s %dx%d profile=%q", v.Codec, v.Width, v.Height, v.Profile)
}

func ffprobeVersion(ctx context.Context, ffprobe string) string {
	if ffprobe == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe, "-hide_banner", "-version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}
