package plex

import (
	"context"
	"sync"

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

// showEpisodes memoises shows' episodes for one response, so a show page
// counts its leaves, groups its seasons and picks On Deck from one load
// rather than one per feature and per season.
type showEpisodes map[core.RatingKey]showEpisodeList

type showEpisodeList struct {
	eps []core.Title
	err error
}

// episodesOfShow is metadataGrandchildren through memo (nil: not memoised).
func (s *Server) episodesOfShow(ctx context.Context, pr *Request, memo showEpisodes, show core.RatingKey) ([]core.Title, error) {
	if l, ok := memo[show]; ok {
		return l.eps, l.err
	}
	eps, err := s.metadataGrandchildren(ctx, pr, show)
	if memo != nil {
		memo[show] = showEpisodeList{eps, err}
	}
	return eps, err
}

// presentedSeasons groups a show's episodes by the season the viewer sees
// them in: their episode-order group, else their own season. Each group is
// what metadataChildren returns for that season.
func (s *Server) presentedSeasons(ctx context.Context, pr *Request, show core.RatingKey, eps []core.Title) map[int][]core.Title {
	grouped := false
	if s.d.EpisodeOrder != nil {
		g, err := s.d.EpisodeOrder.Grouped(ctx, pr.User, []core.RatingKey{show})
		// If preferences cannot be read, ask for each episode's position.
		grouped = err != nil || g[show]
	}
	out := map[int][]core.Title{}
	for _, e := range eps {
		n := e.Season
		if grouped {
			if season, _, _, ok := s.d.EpisodeOrder.Position(ctx, pr.User, e.Key); ok {
				n = season
			}
		}
		out[n] = append(out[n], e)
	}
	return out
}

// episodeOrderView is a list's episode-order presentation, resolved before
// its items render. grouped is nil on detail pages (and if preferences cannot
// be read), where every title consults the service itself.
type episodeOrderView struct {
	grouped  map[core.RatingKey]bool // shows with an alternate episode group
	children map[core.RatingKey]int  // visible children of those shows
}

// episodeOrderList resolves a list's episode orders in one preference query.
// Only shows with an alternate group load their layouts, concurrently; the
// rest keep the catalogue's season counts, so a row of shows never loads
// seasons tile by tile (each possibly a TMDB fetch). Loading a grouped show's
// children also caches the layout its episodes' positions come from.
func (s *Server) episodeOrderList(ctx context.Context, pr *Request, ts []core.Title, detail bool) episodeOrderView {
	var v episodeOrderView
	if s.d.EpisodeOrder == nil || detail {
		return v
	}
	var shows []core.RatingKey
	seen := map[core.RatingKey]bool{}
	for i := range ts {
		if ts[i].Type != core.TypeShow && ts[i].Type != core.TypeEpisode || ts[i].Key.TMDB() <= 0 {
			continue
		}
		if k := core.ShowKey(ts[i].Key.TMDB()); !seen[k] {
			seen[k] = true
			shows = append(shows, k)
		}
	}
	grouped, err := s.d.EpisodeOrder.Grouped(ctx, pr.User, shows)
	if err != nil {
		s.log.Warn("load episode orders", "err", err)
		return v
	}
	v.grouped, v.children = grouped, map[core.RatingKey]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for k := range grouped {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A cancelled request starts no further layout loads.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if kids, e := s.metadataChildren(ctx, pr, k); e == nil {
				n := len(s.visibleTitles(pr.User, kids))
				mu.Lock()
				v.children[k] = n
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return v
}

// applyEpisodeOrder runs after canonical media rendering: filenames, playback,
// GUIDs and the resolver retain canonical identity while Plex sees the chosen
// group tree and display numbering.
func (s *Server) applyEpisodeOrder(ctx context.Context, pr *Request, t *core.Title, m *model.Metadata, v episodeOrderView) {
	if s.d.EpisodeOrder == nil {
		return
	}
	if t.Type == core.TypeShow {
		if v.grouped != nil {
			if n, ok := v.children[t.Key]; ok {
				m.ChildCount = model.Ptr(n)
			}
			return
		}
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
	if t.Type != core.TypeEpisode || v.grouped != nil && !v.grouped[core.ShowKey(t.Key.TMDB())] {
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
