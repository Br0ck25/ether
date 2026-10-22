package transcode

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestIntegrationMatroskaCopyEncodeDoesNotReplayHeldGOP(t *testing.T) {
	e := newRealEnv(t, WithoutHWDetection())
	e.gen(t, "long.mkv", 24, openGOPx265(192, "ctu=64")...)
	e.gen(t, "small.mkv", 24, sdrBars("640:360")...)
	path, copied, _ := e.playStatic(t, "copy-encode-long", "long.mkv", []contractSwap{{file: "small.mkv", at: 12 * time.Second}}, 5)
	if copied[0] {
		t.Fatal("replacement must encode")
	}
	prev := -1.0
	frames := e.frameFormats(t, path)
	if len(frames) < 470 {
		t.Fatalf("only %d decoded frames", len(frames))
	}
	for _, f := range frames {
		now, err := strconv.ParseFloat(f.Time, 64)
		if err != nil {
			t.Fatal(err)
		}
		if prev >= 0 && (now <= prev || now-prev > .25) {
			t.Fatalf("non-continuous video: %.3f -> %.3f", prev, now)
		}
		prev = now
	}
}

func TestIntegrationMatroskaReplacementRemovesEmbeddedPillars(t *testing.T) {
	e := newRealEnv(t, WithoutHWDetection())
	// Original 4:3; replacement has the same picture baked into a 16:9 canvas.
	e.gen(t, "original.mkv", 16, append([]string{"-vf", "scale=480:360,setsar=1"}, []string{"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "pools=1:frame-threads=1:keyint=24:min-keyint=24:scenecut=0:open-gop=0", "-c:a", "flac"}...)...)
	args := sdrBars("480:360")
	args[1] += ",setsar=1,pad=640:360:80:0"
	e.gen(t, "pillar.mkv", 16, args...)
	info, err := e.m.Probe(context.Background(), e.url("pillar.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	orig, err := e.m.Probe(context.Background(), e.url("original.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := contractOf(*orig, copyMKV())
	s := &Session{m: e.m, sessionInput: sessionInput{contract: c}, log: testLogger(t)}
	crop := s.swapPictureCrop(context.Background(), e.url("pillar.mkv"), *info, 4*time.Second)
	if crop == nil || crop.x != 80 || crop.width != 480 {
		t.Fatalf("crop=%+v", crop)
	}
	path, _, _ := e.playStatic(t, "pillars", "original.mkv", []contractSwap{{file: "pillar.mkv", at: 8 * time.Second}}, 4)
	// The replacement fills the original 4:3 frame; no second set of bars.
	y, _, _ := e.pixel(t, path, 9, 8, 8)
	if y < 900 {
		t.Fatalf("replacement corner Y=%d, want white, not double padding", y)
	}
}
