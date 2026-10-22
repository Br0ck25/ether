package stream

import "github.com/braedonsaunders/ether/internal/core"

// Assert extension contracts at the producer so a removed method cannot
// silently make downstream consumers select their fallback.
var (
	_ core.RepairReporter    = (*source)(nil)
	_ core.DurablePrefetcher = (*Engine)(nil)
	_ core.ContextReaderAt   = (*source)(nil)
	_ core.MetadataReaderAt  = (*source)(nil)
	_ core.PrefetchReaderAt  = (*source)(nil)
	_ core.DamageReporter    = (*source)(nil)
)
