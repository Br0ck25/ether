package app

import (
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/overlays"
	"testing"
)

func TestOverlayQualityRejectsEstimatesAndReplacesWholeRelease(t *testing.T) {
	f := overlays.Facts{}
	info := core.MediaInfo{Estimated: true, Streams: []core.StreamInfo{{Type: "video", Height: 2160, DolbyVision: true}, {Type: "audio", Codec: "truehd", Profile: "Dolby Atmos"}}}
	applyOverlayQuality(&f, info)
	if f.Height != 0 || f.DolbyVision || f.Atmos {
		t.Fatal("release estimate advertised as verified")
	}
	info.Estimated = false
	applyOverlayQuality(&f, info)
	if f.Height != 2160 || !f.DolbyVision || !f.Atmos {
		t.Fatal("actual probe omitted")
	}
	applyOverlayQuality(&f, core.MediaInfo{Streams: []core.StreamInfo{{Type: "video", Height: 1080}}})
	if f.Height != 1080 || f.DolbyVision || f.Atmos {
		t.Fatal("facts mixed across releases")
	}
}

func TestOverlayQualityDoesNotTrustAudioTitles(t *testing.T) {
	f := overlays.Facts{}
	applyOverlayQuality(&f, core.MediaInfo{Streams: []core.StreamInfo{{Type: "video", Width: 3840, Height: 1600}, {Type: "audio", Codec: "aac", Title: "Atmospheric commentary"}}})
	if f.Atmos || f.Width != 3840 {
		t.Fatalf("misleading audio title accepted: %+v", f)
	}
}
