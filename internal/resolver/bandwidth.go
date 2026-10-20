package resolver

import "github.com/braedonsaunders/ether/internal/core"

// Bandwidth fit precedes remembered availability and soft quality preferences.
// Reserve 10% above the file average for variable-rate peaks. Unknown size or
// duration is not a rejection; the probe and transcode decision still apply.
func sourceBandwidthFit(r core.Release, caps core.ClientCaps, title *core.Title) int {
	if caps.MaxBitrateKbps <= 0 || r.Size <= 0 || title == nil || title.Runtime <= 0 {
		return 0
	}
	average := float64(r.Size) * 8 / title.Runtime.Seconds() / 1000
	if average*1.1 <= float64(caps.MaxBitrateKbps) {
		return 1
	}
	return -1
}
