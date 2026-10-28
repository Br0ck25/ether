package transcode

import (
	"strconv"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestFailoverPreservesAudioDecoderConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		oldChannels, oldRate, newChannels, newRate int
	}{
		{"stereo-to-surround", 2, 48000, 6, 48000},
		{"sample-rate-change", 2, 44100, 2, 48000},
		{"surround-to-stereo", 6, 48000, 2, 48000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "aac", Channels: tc.oldChannels, SampleRate: tc.oldRate}}}
			next := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "aac", Channels: tc.newChannels, SampleRate: tc.newRate}}}
			d := adaptDecision(core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true, AudioStream: 0}, old, next, ContainerFMP4)
			if d.AudioCopy {
				t.Fatal("changed AAC configuration was copied into the existing decoder")
			}
			args := buildArgs(baseSpec(next, d))
			mustContain(t, args, "-ac "+strconv.Itoa(tc.oldChannels), "-ar "+strconv.Itoa(tc.oldRate))
		})
	}
}

func TestFailoverRetainsAudioTargetAcrossSeveralSources(t *testing.T) {
	stereo := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "aac", Channels: 2, SampleRate: 44100}}}
	surround := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "aac", Channels: 6, SampleRate: 48000}}}
	mono := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "ac3", Channels: 1, SampleRate: 48000}}}
	d := core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true, AudioStream: 0}
	d = adaptDecision(d, stereo, surround, ContainerFMP4)
	d = adaptDecision(d, surround, mono, ContainerFMP4)
	mustContain(t, buildArgs(baseSpec(mono, d)), "-c:a aac", "-ac 2", "-ar 44100")
	if reps := dashReps(mono, d); len(reps) != 1 || reps[0].Channels != 2 || reps[0].SampleRate != 44100 {
		t.Fatalf("manifest lost the negotiated audio format: %+v", reps)
	}
}

func TestFailoverCopiesUnchangedAudio(t *testing.T) {
	info := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "aac", Channels: 6, SampleRate: 48000}}}
	d := adaptDecision(core.Decision{Kind: core.DecisionRemux, AudioCopy: true, AudioStream: 0}, info, info, ContainerFMP4)
	if !d.AudioCopy {
		t.Fatal("unchanged audio should still copy")
	}
}
