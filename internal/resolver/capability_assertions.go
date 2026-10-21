package resolver

import "github.com/braedonsaunders/ether/internal/core"

// Assert extension contracts at the producer so a removed method cannot
// silently make downstream consumers select their fallback.
var (
	_ core.AvailabilitySource = (*Service)(nil)
)
