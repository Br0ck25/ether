package plex

import (
	"context"
	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/core"
)

// AccessPolicy adds owner-defined restrictions to the mandatory title guard.
type AccessPolicy interface {
	Allowed(*core.User, *core.Title) bool
}

func (s *Server) visibleTitles(u *core.User, ts []core.Title) []core.Title {
	out := make([]core.Title, 0, len(ts))
	for i := range ts {
		if s.titleVisible(u, &ts[i]) {
			out = append(out, ts[i])
		}
	}
	return out
}

func (s *Server) titleAllowed(u *core.User, t *core.Title) bool {
	if t == nil || t.Adult {
		return false
	}
	if u != nil && catalog.RatingExceeds(t.ContentRating, u.MaxContentRating) {
		return false
	}
	return s.d.AccessPolicy == nil || s.d.AccessPolicy.Allowed(u, t)
}

// visibleTitle also checks section and inherited show policy for artwork and
// child items whose summaries have not yet inherited the certification.
func (s *Server) visibleTitle(ctx context.Context, u *core.User, t *core.Title) bool {
	if !s.titleVisible(u, t) {
		return false
	}
	if len(s.availableForViewer(ctx, u, []core.Title{*t})) == 0 {
		return false
	}
	if t.ShowKey != 0 {
		show, err := s.d.Catalog.Get(ctx, t.ShowKey)
		if err != nil || !s.titleVisible(u, show) {
			return false
		}
	}
	return true
}

// Discovery evidence must not revoke a source that this viewer already has
// open. Membership/content guards still run independently; source failure uses
// playback's normal failover path.
func (s *Server) availableForViewer(ctx context.Context, u *core.User, ts []core.Title) []core.Title {
	if s.d.FilterAvailable == nil {
		return ts
	}
	filtered := s.d.FilterAvailable(ctx, ts)
	if len(filtered) == len(ts) || u == nil || s.d.Playback == nil {
		return filtered
	}
	keep := map[core.RatingKey]bool{}
	for _, t := range filtered {
		keep[t.Key] = true
	}
	for _, ps := range s.d.Playback.Sessions() {
		if ps != nil && ps.UserID == u.ID {
			keep[ps.Key] = true
		}
	}
	out := make([]core.Title, 0, len(ts))
	for _, t := range ts {
		if keep[t.Key] {
			out = append(out, t)
		}
	}
	return out
}
