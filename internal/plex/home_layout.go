package plex

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// Final placement preserves each producer's actual Plex metadata and tune URLs.
// Only the order/visibility of rows is returned by the layout engine.
func (s *Server) finalizeHomeRows(ctx context.Context, pr *Request, hc core.HubContext, section string, rows []model.Hub) []model.Hub {
	engine, ok := s.d.Recommender.(core.HomeLayoutSource)
	if !ok {
		return rows
	}
	raw := make([]core.Hub, 0, len(rows))
	byID := map[string]model.Hub{}
	for _, row := range rows {
		raw = append(raw, core.Hub{ID: row.HubIdentifier, Title: row.Title, Type: core.MediaType(row.Type), Context: row.Context, ItemCount: max(row.Size, len(row.Metadata)+len(row.Directory))})
		byID[row.HubIdentifier] = row
	}
	ordered, err := engine.FinalizeHome(ctx, pr.User, hc, section, raw)
	if err != nil {
		s.log.Warn("Home layout unavailable", "error", err)
		return rows
	}
	out := make([]model.Hub, 0, len(ordered))
	for _, row := range ordered {
		if rendered, ok := byID[row.ID]; ok {
			out = append(out, rendered)
		}
	}
	return out
}
