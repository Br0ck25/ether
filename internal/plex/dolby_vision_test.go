package plex

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// These technical fields match a read-only native PMS metadata response;
// zero BL compatibility and false enhancement-layer flags are meaningful.
func TestDolbyVisionDecisionCarriesConfigurationRecord(t *testing.T) {
	for _, tc := range []struct {
		name            string
		profile, compat int
		decision        core.DecisionKind
	}{
		{"profile8 direct", 8, 1, core.DecisionDirectPlay},
		{"profile5 direct", 5, 0, core.DecisionDirectPlay},
		{"profile8 copy remux", 8, 1, core.DecisionRemux},
		{"transcoded output", 8, 1, core.DecisionTranscode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			defer f.srv.Close()
			f.playback.info.Streams = append([]core.StreamInfo(nil), sampleInfo.Streams...)
			f.playback.info.Streams[0] = core.StreamInfo{Index: 0, Type: "video", Codec: "hevc", Profile: "Main 10", Level: 156, BitDepth: 10, Width: 1920, Height: 804, ColorTransfer: "smpte2084", DolbyVision: true, DolbyVisionProfile: tc.profile, DolbyVisionLevel: 3, DolbyVisionCompatibilityID: tc.compat, DolbyVisionVersion: "1.0", DolbyVisionBLPresent: model.Ptr(true), DolbyVisionELPresent: model.Ptr(false), DolbyVisionRPUPresent: model.Ptr(true)}
			copyVideo := tc.decision != core.DecisionTranscode
			f.trans.decision = core.Decision{Kind: tc.decision, VideoCopy: copyVideo, AudioCopy: true, VideoCodec: "h264", Container: "mpegts", ToneMap: !copyVideo}
			mc := decodeJSON(t, f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/1000000000000550&directStream=1", jsonAccept...))
			md := mc["Metadata"].([]any)[0].(map[string]any)
			media := md["Media"].([]any)[0].(map[string]any)
			part := media["Part"].([]any)[0].(map[string]any)
			v := part["Stream"].([]any)[0].(map[string]any)
			want := map[string]any{"DOVIPresent": true, "DOVIProfile": float64(tc.profile), "DOVILevel": float64(3), "DOVIBLCompatID": float64(tc.compat), "DOVIVersion": "1.0", "DOVIBLPresent": true, "DOVIELPresent": false, "DOVIRPUPresent": true, "level": float64(156)}
			for key, value := range want {
				got, ok := v[key]
				if !copyVideo {
					if ok {
						t.Errorf("transcoded stream retained %s=%v", key, got)
					}
					continue
				}
				if !ok || !reflect.DeepEqual(got, value) {
					t.Errorf("%s=%v present=%v, want %v", key, got, ok, value)
				}
			}
		})
	}
}

func TestUnknownDolbyVisionRecordDoesNotInventLayerFlags(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	info := core.MediaInfo{Streams: []core.StreamInfo{{Type: "video", Codec: "hevc", DolbyVision: true}}}
	got := f.srv.buildStreams(fightClub.Key, info, selection{}, &ownerUser)[0]
	if got.DOVIPresent == nil || !*got.DOVIPresent {
		t.Fatal("codec-tag DV was lost")
	}
	if got.DOVIBLCompatID != nil || got.DOVIBLPresent != nil || got.DOVIELPresent != nil || got.DOVIRPUPresent != nil || got.DOVIVersion != "" {
		t.Fatalf("invented configuration from codec tag: %+v", got)
	}
}

func TestHDR10BaseDecisionKeepsCopyAndRemovesDVSignaling(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	info := core.MediaInfo{Container: "mkv", Streams: []core.StreamInfo{{Type: "video", Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10, ColorTransfer: "smpte2084", DolbyVision: true, DolbyVisionProfile: 8, DolbyVisionLevel: 6, DolbyVisionCompatibilityID: 1, DolbyVisionVersion: "1.0", DolbyVisionBLPresent: model.Ptr(true), DolbyVisionELPresent: model.Ptr(false), DolbyVisionRPUPresent: model.Ptr(true)}}}
	ps := &core.PlaySession{Key: fightClub.Key, Info: info, Resolution: &core.Resolution{Release: core.Release{ID: "debrid-source", Protocol: core.ProtocolTorrent}}}
	dec := core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true, VideoCodec: "hevc", Container: "mkv", StripDolbyVision: true}
	p := playRequest{protocol: "hls", mde: true, opts: core.DecideOptions{DirectPlay: true, DirectStream: true}}
	mc := f.srv.decisionContainer(context.Background(), &Request{User: &ownerUser}, &fightClub, ps, dec, p)
	part := mc.Metadata[0].Media[0].Part[0]
	if part.Decision == "directplay" {
		t.Fatal("MDE disguised required base-layer remux as direct play")
	}
	v := part.Stream[0]
	if v.Decision != "copy" || v.Codec != "hevc" || v.Width != 3840 || v.Height != 2160 || v.BitDepth != 10 || v.ColorTrc != "smpte2084" {
		t.Fatalf("HDR10 base changed: %+v", v)
	}
	if v.DOVIPresent != nil || v.DOVIProfile != 0 || v.DOVILevel != 0 || v.DOVIBLCompatID != nil || v.DOVIBLPresent != nil || v.DOVIELPresent != nil || v.DOVIRPUPresent != nil || v.DOVIVersion != "" || strings.Contains(v.DisplayTitle, "DoVi") {
		t.Fatalf("output advertised removed DV data: %+v", v)
	}
}
