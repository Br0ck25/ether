package resolver

import (
	"context"
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

// rescueCandidates is the single discovery/ranking path for both empty-search
// and failed-health rescues. It never reconsiders already observed releases.
func (s *Service) rescueCandidates(ctx context.Context, req core.ResolveRequest, search core.SearchRequest, profile core.QualityProfile, seen []core.Release, trace *Trace, pinID string) ([]candidate, []core.Release, bool, error) {
	extra := novelReleases(seen, s.searchRescue(ctx, search))
	if len(extra) == 0 {
		return nil, seen, false, nil
	}
	s.rememberReleases(req.Title, extra)
	candidates, rejected, err := s.rankCandidates(ctx, req, search, profile, extra, trace, pinID)
	return candidates, append(seen, extra...), rejected, err
}

func (s *Service) acceptResolution(ctx context.Context, key core.RatingKey, candidates []candidate, index int, nzb []byte, trace *Trace) *core.Resolution {
	s.setAvailability(ctx, int64(key), true, s.searchTTL())
	trace.Winner = candidates[index].rel.ID
	return s.resolution(key, candidates, index, nzb, trace)
}

type preferredOutcome struct {
	candidates []candidate
	resolution *core.Resolution
	attempted  bool
	transient  error
}

// resolvePreferred handles the explicit viewer choice or owner's pin before
// ordinary source preparation. Transient errors survive only as a fallback if
// the remaining candidates also fail.
func (s *Service) resolvePreferred(ctx context.Context, req core.ResolveRequest, pin *Pin, candidates []candidate, trace *Trace, race int) (preferredOutcome, error) {
	out := preferredOutcome{candidates: candidates}
	pinID := ""
	if pin != nil {
		pinID = pin.Release.ID
	}
	prefer := req.Prefer
	if prefer == "" {
		prefer = pinID
	}
	if prefer == "" {
		return out, nil
	}
	index := -1
	for i := range candidates {
		if candidates[i].rel.ID == prefer {
			index = i
			break
		}
	}
	note := func(status PinStatus, reason, display string) {
		if pin != nil {
			s.notePin(ctx, req.Title.Key, pinID, status, reason)
			trace.PinStatus = display
		}
	}
	if index < 0 {
		reason := candidateReason(trace, pinID)
		note(PinRejected, reason, "rejected: "+reason)
		s.log.Info("preferred release not among candidates; using normal ranking", "title", req.Title.Key, "prefer", prefer)
		return out, nil
	}
	out.attempted = true
	winner, nzb, err := s.validateCandidates(ctx, []candidate{candidates[index]}, trace, race)
	switch {
	case err != nil:
		note(PinUnavailable, err.Error(), "unavailable: "+err.Error())
		if ctx.Err() != nil || len(candidates) == 1 {
			trace.Err = err.Error()
			return out, err
		}
		out.transient = err
	case winner >= 0:
		note(PinHealthy, "", "used")
		out.resolution = s.acceptResolution(ctx, req.Title.Key, candidates, index, nzb, trace)
		return out, nil
	default:
		reason := candidateReason(trace, pinID)
		note(PinUnhealthy, reason, "unhealthy: "+reason)
	}
	s.log.Info("preferred release unavailable; falling back to normal ranking", "title", req.Title.Key, "prefer", prefer, "err", err)
	out.candidates = append(candidates[:index], candidates[index+1:]...)
	if len(out.candidates) == 0 {
		trace.Err = "preferred release unhealthy and no other candidates"
		return out, fmt.Errorf("%q: %w", req.Title.Title, core.ErrNotAvailable)
	}
	return out, nil
}
