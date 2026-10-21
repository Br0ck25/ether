// Command ether-transcodebench measures Ether's real transcode.Manager
// (probe, explicit decision, simultaneous HLS sessions consumed through
// ServeSegment) against a local fixture served over loopback range HTTP.
//
// Example (small smoke only; full loads need a granted benchmark window):
//
//	ether-transcodebench -input fixture.mp4 -mode transcode -codec h264 \
//	  -hwaccel none -concurrency 1 -duration 8s -result result.json -output out/
//
// Exit status is nonzero when any session fails, verification fails, or a
// requested hardware accelerator silently fell back to software.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/braedonsaunders/ether/internal/transcodebench"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ether-transcodebench:", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg transcodebench.Config
	var duration, segDur, timeout time.Duration
	flag.StringVar(&cfg.Input, "input", "", "local fixture media file (required)")
	flag.StringVar(&cfg.FFmpeg, "ffmpeg", "", "ffmpeg path (default: Manager discovery)")
	flag.StringVar(&cfg.FFprobe, "ffprobe", "", "ffprobe path (default: Manager discovery)")
	flag.StringVar(&cfg.HWAccel, "hwaccel", "none", "none|auto|videotoolbox|nvenc|qsv|vaapi")
	flag.StringVar(&cfg.Mode, "mode", "transcode", "transcode|remux (remux is the stream-copy control)")
	flag.StringVar(&cfg.Codec, "codec", "h264", "target video codec for -mode transcode: h264|hevc")
	flag.IntVar(&cfg.MaxHeight, "max-height", 0, "max output height; 0 = source height")
	flag.IntVar(&cfg.BitrateKbps, "bitrate-kbps", 0, "target total bitrate kbps; 0 = Manager decision")
	flag.IntVar(&cfg.Concurrency, "concurrency", 1, "simultaneous sessions")
	flag.DurationVar(&duration, "duration", 8*time.Second, "media duration to consume per session (e.g. 30s, 2m)")
	flag.DurationVar(&segDur, "segment-duration", 4*time.Second, "HLS segment duration")
	flag.BoolVar(&cfg.Paced, "paced", false, "consume segments at roughly realtime pace")
	flag.StringVar(&cfg.ResultPath, "result", "", "write versioned JSON result here")
	flag.StringVar(&cfg.OutputDir, "output", "", "retain init/segments/output per session in this dir")
	flag.DurationVar(&timeout, "timeout", 10*time.Minute, "overall timeout (context deadline)")
	flag.StringVar(&cfg.VAAPIDevice, "vaapi-device", "", "VAAPI render device (hwaccel=vaapi)")
	flag.Parse()
	cfg.Duration = duration
	cfg.SegmentDur = segDur
	cfg.Timeout = timeout

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	res, err := transcodebench.Run(context.Background(), cfg, log)
	if res != nil && cfg.ResultPath != "" {
		if werr := transcodebench.WriteResult(cfg.ResultPath, res); werr != nil && err == nil {
			err = fmt.Errorf("write -result: %w", werr)
		}
	}
	if res != nil {
		fmt.Printf("success=%v sessions ok=%d failed=%d segments=%d bytes=%d media=%.1fs wall=%dms hw=%s fallback=%v decision=%s/%s\n",
			res.Success, res.Totals.SessionsOK, res.Totals.SessionsFailed, res.Totals.Segments,
			res.Totals.Bytes, res.Totals.MediaSec, res.WallMs, res.Runtime.HWDetected,
			res.Runtime.HWFallback, res.Decision.Kind, res.Decision.VideoCodec)
		if cfg.ResultPath != "" {
			fmt.Printf("result written to %s\n", cfg.ResultPath)
		}
	}
	return err
}
