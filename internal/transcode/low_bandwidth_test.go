package transcode

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestConstrainedConnectionBudgetIncludesSurroundAudio(t *testing.T) {
	info := mustParse(t, "mkv_hevc_hdr10.json")
	for _, limit := range []int{800, 1500, 3000, 8000} {
		caps := appleTV
		caps.MaxBitrateKbps = limit
		d := Decide(info, caps, allow(), Policy{HEVCEncoder: true, HEVCHardware: true})
		if d.Kind != core.DecisionTranscode || d.VideoCopy || d.BitrateKbps > limit {
			t.Fatalf("%d kbps cannot carry decision: %+v", limit, d)
		}
		if limit <= 1500 && (d.AudioCopy || d.AudioCodec != "aac") {
			t.Fatalf("surround crowded out low-bitrate video: %+v", d)
		}
	}
}
