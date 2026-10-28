package plex

import (
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

func TestNativeDesktopPlaybackCapabilities(t *testing.T) {
	clients := []struct{ product, platform string }{
		{"Plex for Linux", "Linux"},
		{"Plex for Windows", "Windows"},
		{"Plex for Mac", "macOS"},
		{"Plex Desktop", "Linux"},
	}
	for _, client := range clients {
		t.Run(client.product, func(t *testing.T) {
			for _, source := range []struct {
				name, codec, transfer string
				dv                    bool
			}{
				{"HEVC Main 10", "hevc", "", false},
				{"HEVC HDR10", "hevc", "smpte2084", false},
				{"DV with HDR10 base", "hevc", "smpte2084", true},
				{"AV1", "av1", "", false},
				{"VP9", "vp9", "", false},
			} {
				t.Run(source.name, func(t *testing.T) {
					r := httptest.NewRequest("GET", "/video/:/transcode/universal/decision", nil)
					r.RemoteAddr = "192.168.1.5:1234"
					r.Header.Set("X-Plex-Product", client.product)
					r.Header.Set("X-Plex-Platform", client.platform)
					r.Header.Set("X-Plex-Model", "standalone")
					info := core.MediaInfo{Container: "mkv", BitrateKbps: 20000, Streams: []core.StreamInfo{
						{Index: 0, Type: "video", Codec: source.codec, Profile: "Main 10", Width: 3840, Height: 2160, BitDepth: 10, ColorTransfer: source.transfer, DolbyVision: source.dv, DolbyVisionProfile: 8, DolbyVisionCompatibilityID: 1},
						{Index: 1, Type: "audio", Codec: "eac3", Channels: 6},
					}}
					caps := ParseRequest(r).ClientCaps(0)
					d := transcode.Decide(info, caps, core.DecideOptions{DirectPlay: true, DirectStream: true, AudioStream: -1, SubtitleStream: -1}, transcode.Policy{})
					if d.Kind != core.DecisionDirectPlay || !d.VideoCopy || d.ToneMap {
						t.Fatalf("native desktop should retain source video: %+v", d)
					}
				})
			}
		})
	}
}

func TestNativeDesktopExplicitHDRLimit(t *testing.T) {
	info := core.MediaInfo{Container: "mkv", Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10, ColorTransfer: "smpte2084"},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2},
	}}
	for _, extra := range []string{
		"add-limitation(scope=videoCodec&scopeName=hevc&type=upperBound&name=video.bitDepth&value=8)",
		"add-limitation(scope=videoCodec&scopeName=hevc&type=notMatch&name=video.colorTrc&value=smpte2084)",
	} {
		caps := (&Request{Product: "Plex for Linux", Platform: "Linux", ProfileExtra: extra}).ClientCaps(0)
		d := transcode.Decide(info, caps, core.DecideOptions{DirectPlay: true, DirectStream: true, AudioStream: -1, SubtitleStream: -1}, transcode.Policy{})
		if caps.HDR10 || d.VideoCopy || !d.ToneMap {
			t.Fatalf("explicit SDR limitation ignored: caps=%+v decision=%+v", caps, d)
		}
	}
}
