package plex

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

func (s *Server) metadataTitle(ctx context.Context, pr *Request, k core.RatingKey) (*core.Title, error) {
	if s.d.EpisodeOrder != nil {
		return s.d.EpisodeOrder.Get(ctx, pr.User, k)
	}
	return s.d.Catalog.Get(ctx, k)
}
func (s *Server) metadataChildren(ctx context.Context, pr *Request, k core.RatingKey) ([]core.Title, error) {
	if s.d.EpisodeOrder != nil {
		return s.d.EpisodeOrder.Children(ctx, pr.User, k)
	}
	return s.d.Catalog.Children(ctx, k)
}
func (s *Server) metadataGrandchildren(ctx context.Context, pr *Request, k core.RatingKey) ([]core.Title, error) {
	if s.d.EpisodeOrder != nil {
		return s.d.EpisodeOrder.Grandchildren(ctx, pr.User, k)
	}
	return s.d.Catalog.Grandchildren(ctx, k)
}

// applyEpisodeOrder runs after canonical media rendering: filenames, playback,
// GUIDs and the resolver retain canonical identity while Plex sees the chosen
// group tree and display numbering.
func (s *Server) applyEpisodeOrder(ctx context.Context, pr *Request, t *core.Title, m *model.Metadata) {
	if s.d.EpisodeOrder == nil {
		return
	}
	if t.Type == core.TypeShow {
		if kids, e := s.metadataChildren(ctx, pr, t.Key); e == nil {
			m.ChildCount = model.Ptr(len(s.visibleTitles(pr.User, kids)))
		}
		return
	}
	if t.Type == core.TypeSeason {
		if t.Season >= 9000 {
			m.GUID = "local://episode-order/" + t.Key.String()
			m.Index = model.Ptr(t.Season - 8999)
		}
		return
	}
	if t.Type != core.TypeEpisode {
		return
	}
	season, index, name, ok := s.d.EpisodeOrder.Position(ctx, pr.User, t.Key)
	if !ok {
		return
	}
	parent := core.SeasonKey(t.Key.TMDB(), season)
	m.Index = model.Ptr(index)
	m.ParentIndex = model.Ptr(season)
	if season >= 9000 {
		m.ParentIndex = model.Ptr(season - 8999)
	}
	m.ParentTitle = name
	m.ParentRatingKey = parent.String()
	m.ParentKey = "/library/metadata/" + parent.String()
	m.ParentThumb = "/library/metadata/" + core.ShowKey(t.Key.TMDB()).String() + "/thumb"
	// A group is local presentation metadata, not an invented TMDB season.
	m.ParentGUID = "local://episode-order/" + parent.String()
}
