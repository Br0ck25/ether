package resolver

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
)

// FeedCandidate applies the existing quality, identity and block policies without
// searching. Broad capabilities mean the default owner's profile determines
// eligibility; this is source evidence, never a claim about every device.
func (s *Service) FeedCandidate(ctx context.Context, t *core.Title, r core.Release) (core.Release, string, error) {
	req := core.ResolveRequest{Title: t, Client: core.ClientCaps{HDR10: true, DolbyVision: true}}
	p := s.ProfileFor(req.Client, nil)
	trace := Trace{}
	candidates, _, err := s.rankCandidates(ctx, req, searchRequestFor(t), p, []core.Release{r}, &trace, "")
	if err != nil {
		return r, "", err
	}
	if len(candidates) == 0 {
		reason := "not accepted by the current quality policy"
		if len(trace.Candidates) > 0 {
			reason = trace.Candidates[0].Reason
		}
		return r, reason, nil
	}
	return candidates[0].rel, "", nil
}

// VerifyFeedCandidate validates exactly the discovered release. It never invokes
// Search or rescue searches and never writes a title-wide negative result.
func (s *Service) VerifyFeedCandidate(ctx context.Context, t *core.Title, r core.Release) error {
	r, reason, err := s.FeedCandidate(ctx, t, r)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("%w: %s", core.ErrReleaseRejected, reason)
	}
	if s.health == nil {
		return core.ErrSourceUnavailable
	}
	payload, err := s.indexer.FetchNZB(ctx, r)
	if err != nil {
		return err
	}
	ok, err := core.CheckReleaseHealth(ctx, s.health, r.ID, payload)
	if err != nil {
		return err
	}
	if !ok {
		return core.ErrSourceUnavailable
	}
	s.markVerified(ctx, r.ID)
	s.rememberReleases(t, []core.Release{r})
	s.setAvailability(ctx, int64(t.Key), true, s.searchTTL())
	return nil
}
