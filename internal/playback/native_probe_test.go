package playback

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func withNativeProbe(probe func(context.Context, core.MediaSource) (*core.MediaInfo, error), trailer func(context.Context, core.MediaSource, *core.MediaInfo) bool) harnessOpt {
	return func(h *harness, _ *Options) {
		h.nativeProbe, h.nativeTrailer = probe, trailer
	}
}

func nativeInfo(src core.MediaSource) *core.MediaInfo {
	return &core.MediaInfo{Container: "mkv", Duration: time.Minute, Size: src.Size(), Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160, BitDepth: 10, ColorTransfer: "smpte2084"},
		{Index: 1, Type: "audio", Codec: "eac3", Channels: 6, SampleRate: 48000},
	}}
}

func TestNativeProbeSkipsFFprobe(t *testing.T) {
	var native atomic.Int32
	h := newHarness(t, []*fakeRelease{mkv("rel", 10000, time.Minute)}, []string{"rel"}, withNativeProbe(
		func(ctx context.Context, src core.MediaSource) (*core.MediaInfo, error) {
			native.Add(1)
			// The prober reads the source directly, not the loopback URL.
			if _, err := src.ReadAt(make([]byte, 64), 0); err != nil {
				return nil, err
			}
			return nativeInfo(src), nil
		}, nil))
	ps := h.prepare(owner, movieKey, "native")
	if native.Load() != 1 {
		t.Fatalf("native probes = %d", native.Load())
	}
	if h.tr.probes != 0 {
		t.Fatalf("ffprobe ran %d times after a complete native probe", h.tr.probes)
	}
	if v := ps.Info.Video(); v == nil || v.Codec != "hevc" || v.ColorTransfer != "smpte2084" || ps.Info.Estimated {
		t.Fatalf("session info = %+v", ps.Info)
	}
}

func TestIncompleteNativeProbeFallsBackToFFprobe(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("rel", 10000, time.Minute)}, []string{"rel"}, withNativeProbe(
		func(context.Context, core.MediaSource) (*core.MediaInfo, error) {
			return nil, errors.New("native probe incomplete: video codec V_MS/VFW/FOURCC")
		}, nil))
	ps := h.prepare(owner, movieKey, "fallback")
	if h.tr.probes != 1 {
		t.Fatalf("ffprobe runs = %d, want 1", h.tr.probes)
	}
	if ps.Info.Estimated || ps.Info.Video() == nil {
		t.Fatalf("fallback info = %+v", ps.Info)
	}
}

func TestNativeProbeMissingArticleTriesNextRelease(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("broken", 10000, time.Minute), mkv("good", 10000, time.Minute)}, []string{"broken", "good"}, withNativeProbe(
		func(_ context.Context, src core.MediaSource) (*core.MediaInfo, error) {
			if src.ReleaseID() == "broken" {
				return nil, fmt.Errorf("read header: %w", core.ErrArticleMissing)
			}
			return nativeInfo(src), nil
		}, nil))
	ps := h.prepare(owner, movieKey, "missing")
	if ps.Resolution.Release.ID != "good" {
		t.Fatalf("played %s", ps.Resolution.Release.ID)
	}
	if h.resolver.bad["broken"] == "" {
		t.Fatal("release with missing header articles was not marked bad")
	}
	if h.tr.probes != 0 {
		t.Fatalf("ffprobe re-read a source the native probe found broken (%d runs)", h.tr.probes)
	}
}

func TestNativeTrailerFillsOnlyUnknownFacts(t *testing.T) {
	filled := make(chan struct{})
	h := newHarness(t, []*fakeRelease{mkv("rel", 10000, time.Minute)}, []string{"rel"}, withNativeProbe(
		func(_ context.Context, src core.MediaSource) (*core.MediaInfo, error) {
			info := nativeInfo(src)
			info.Streams[0].BitrateKbps = 20000
			return info, nil
		},
		func(_ context.Context, _ core.MediaSource, info *core.MediaInfo) bool {
			defer close(filled)
			info.Chapters = []core.Chapter{{ID: 1, Title: "Credits", Start: 50 * time.Second, End: time.Minute}}
			info.Streams[0].BitrateKbps = 1 // already known: must not change
			info.Streams[1].BitrateKbps = 640
			info.Streams[0].Codec = "h264" // identity is never taken from the trailer
			return true
		}))
	ps := h.prepare(owner, movieKey, "trailer")
	select {
	case <-filled:
	case <-time.After(5 * time.Second):
		t.Fatal("trailer fill never ran")
	}
	var info core.MediaInfo
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		info = h.m.lookup(ps.ID).current().infoSnapshot()
		if len(info.Chapters) == 1 {
			break
		}
	}
	if len(info.Chapters) != 1 || info.Chapters[0].Title != "Credits" {
		t.Fatalf("chapters = %+v", info.Chapters)
	}
	if info.Streams[0].BitrateKbps != 20000 || info.Streams[1].BitrateKbps != 640 || info.Streams[0].Codec != "hevc" {
		t.Fatalf("streams = %+v", info.Streams)
	}
}
