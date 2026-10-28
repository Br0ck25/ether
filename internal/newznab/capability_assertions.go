package newznab

import "github.com/braedonsaunders/ether/internal/core"

// Assert extension contracts at the producer so a removed method cannot
// silently make downstream consumers select their fallback.
var (
	_ core.StreamingIndexer       = (*Manager)(nil)
	_ core.RescueIndexer          = (*Manager)(nil)
	_ core.StreamingRescueIndexer = (*Manager)(nil)
)
