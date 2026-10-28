package transcode

import "github.com/braedonsaunders/ether/internal/core"

// Assert extension contracts at the producer so a removed method cannot
// silently make downstream consumers select their fallback.
var (
	_ core.PlaybackPosition  = (*Session)(nil)
	_ core.TrickplayCanceler = (*Thumbnails)(nil)
	_ core.ProcessIdentity   = (*execProcess)(nil)
	_ core.ProcessCPUTime    = (*execProcess)(nil)
)
