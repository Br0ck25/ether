package debrid

import "github.com/braedonsaunders/ether/internal/core"

// Assert extension contracts at the producer so a removed method cannot
// silently make downstream consumers select their fallback.
var (
	_ core.RescueIndexer     = (*Indexer)(nil)
	_ core.DurablePrefetcher = (*Engine)(nil)
	_ core.ContextReaderAt   = (*httpSource)(nil)
	_ core.MetadataReaderAt  = (*httpSource)(nil)
	_ core.PrefetchReaderAt  = (*httpSource)(nil)
	_ core.DirectPlayRetrier = (*httpSource)(nil)
)
