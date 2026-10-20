package transcode

import (
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

func decisionAudioChannels(d core.Decision, src *core.StreamInfo) int {
	if d.AudioChannels > 0 {
		return d.AudioChannels
	}
	return outputChannels(d.AudioCodec, src)
}

func decisionAudioRate(d core.Decision) int {
	if d.AudioSampleRate > 0 {
		return d.AudioSampleRate
	}
	return 48000
}

// preserveAudio keeps the configuration held by an existing decoder. Equal
// codec names do not make AAC packets interchangeable: channel layout and
// sample rate also live in the MP4 AudioSpecificConfig. Re-encode a changed
// input to the output already accepted by the client.
func preserveAudio(d core.Decision, old, next *core.StreamInfo) (core.Decision, string) {
	if old == nil || next == nil {
		return d, ""
	}
	codec := d.AudioCodec
	channels, rate := decisionAudioChannels(d, old), decisionAudioRate(d)
	if d.AudioCopy {
		codec = old.Codec
		if old.Channels > 0 {
			channels = old.Channels
		}
		if old.SampleRate > 0 {
			rate = old.SampleRate
		}
	}
	d.AudioChannels, d.AudioSampleRate = channels, rate
	if d.AudioCopy && (next.Codec != codec || next.Channels != channels || next.SampleRate != rate) {
		d.AudioCopy, d.AudioCodec = false, codec
		return d, fmt.Sprintf("audio %s %dch/%dHz re-encoded to %s %dch/%dHz", next.Codec, next.Channels, next.SampleRate, codec, channels, rate)
	}
	return d, ""
}
