package resolver

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// sourcePreparer is optional: regular Usenet-only indexers need no extra
// work. Debrid indexers can learn cache availability before ranked health
// checks spend their entire budget on unknown torrent descriptors.
type sourcePreparer interface {
	PrepareSources(context.Context, []core.Release) []core.Release
}

func (s *Service) prepareSources(ctx context.Context, cands []candidate, title *core.Title, tr *Trace, scopes ...*candidateChecks) []candidate {
	p, ok := s.indexer.(sourcePreparer)
	if !ok || len(cands) < 2 {
		return cands
	}
	if cands[0].verified && (s.resolverCfg().SourcePreference != PreferDebrid || protocolOf(cands[0].rel) == core.ProtocolTorrent) {
		return cands
	}
	if s.knownCached(cands[0].rel) {
		// Ranking already chose a cached torrent in the accepted quality
		// tier. Validate it now instead of delaying Play to discover the
		// availability of a dozen lower-ranked descriptors. Validation can
		// still reject a stale cache answer and try the normal alternates.
		return cands
	}
	var releases []core.Release
	margin := s.resolverCfg().SourceScoreMargin
	if margin <= 0 {
		margin = defaultSourceScoreMargin
	}
	lead := cands[0]
	for _, c := range cands {
		// Discovery can only promote an eligible peer of the leading tier.
		// Lower tiers remain fallbacks and resolve on demand if reached.
		if c.rel.Protocol == core.ProtocolTorrent && c.bandwidth >= lead.bandwidth && qualityTier(c.rel.Info) == qualityTier(lead.rel.Info) && c.rel.Score >= lead.rel.Score-margin {
			releases = append(releases, c.rel)
			if len(releases) == 12 {
				break
			}
		}
	}
	if len(releases) == 0 {
		return cands
	}
	if len(scopes) > 0 && scopes[0] != nil && !s.resolverCfg().OptimisticStart {
		// This is the same independent Usenet hedge that ranked validation
		// would run next. Start its grab/health check while torrent hashes
		// and cache status are discovered, and reuse its exact result later.
		for i, c := range cands {
			if c.rel.Protocol == core.ProtocolUsenet && c.bandwidth >= lead.bandwidth && qualityTier(c.rel.Info) == qualityTier(lead.rel.Info) && c.rel.Score >= lead.rel.Score-margin {
				cands[i].prepared = scopes[0].start(ctx, s, c.rel)
				break
			}
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	prepared := p.PrepareSources(pctx, releases)
	cancel()
	byID := make(map[string]core.Release, len(prepared))
	for _, r := range prepared {
		byID[r.ID] = r
	}
	for i := range cands {
		if r, ok := byID[cands[i].rel.ID]; ok {
			cands[i].rel.InfoHash, cands[i].rel.MagnetURL = r.InfoHash, r.MagnetURL
		}
	}
	return s.routeSources(cands, title, tr)
}
