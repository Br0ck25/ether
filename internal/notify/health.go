package notify

import (
	"context"
	"github.com/braedonsaunders/ether/internal/usenet"
)

// SampleHealth emits one alert on entering an unhealthy state, rather than one
// alert for every sample. Normal recovery allows a later outage to alert again.
func (s *Service) SampleHealth(ctx context.Context) error {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if s.providerStates == nil {
		s.providerStates = map[string]string{}
	}
	if s.opts.Providers != nil {
		for _, p := range s.opts.Providers() {
			id := p.ID
			if id == "" {
				id = p.Host
			}
			old := s.providerStates[id]
			if (p.State == usenet.StateBackoff || p.State == usenet.StateAuthFail) && p.State != old {
				name := p.Name
				if name == "" {
					name = id
				}
				if err := s.Emit(ctx, Event{Kind: "provider.outage", Title: name, Message: "Provider is unavailable or has rejected authentication. See server health for details."}); err != nil {
					return err
				}
			}
			s.providerStates[id] = p.State
		}
	}
	if s.opts.CacheUsage != nil {
		used, budget := s.opts.CacheUsage()
		full := budget > 0 && used >= budget
		if full && !s.cacheFull {
			if err := s.Emit(ctx, Event{Kind: "cache.full", Title: "Ether cache is full", Message: "The configured segment cache budget has been reached."}); err != nil {
				return err
			}
		}
		s.cacheFull = full
	}
	return nil
}
