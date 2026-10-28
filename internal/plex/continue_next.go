package plex

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Reading a row never changes watch state. The completed episode supplies row
// recency; the next episode retains its own real progress and viewed count.
func (s *Server) appendNextContinue(ctx context.Context, pr *Request, titles []core.Title, state map[core.RatingKey]core.WatchStatus, limit int) []core.Title {
	history, err := s.d.State.History(ctx, pr.User.ID, s.now().Add(-90*24*time.Hour), 200)
	if err != nil {
		return titles
	}
	seen := map[int64]bool{}
	for _, t := range titles {
		if t.Type == core.TypeEpisode {
			seen[t.Key.TMDB()] = true
		}
	}
	var recent []core.HistoryEntry
	var keys, shows []core.RatingKey
	for _, h := range history {
		if !h.Completed || h.Key.Kind() != core.KindEpisode || seen[h.Key.TMDB()] {
			continue
		}
		seen[h.Key.TMDB()] = true
		recent = append(recent, h)
		keys = append(keys, h.Key)
		shows = append(shows, core.ShowKey(h.Key.TMDB()))
		if len(recent) >= min(max(limit, 1), 50) {
			break
		}
	}
	statuses, err := s.d.State.GetMany(ctx, pr.User.ID, keys)
	if err != nil {
		return titles
	}
	grouped := map[core.RatingKey]bool{}
	if s.d.EpisodeOrder != nil {
		grouped, err = s.d.EpisodeOrder.Grouped(ctx, pr.User, shows)
		if err != nil {
			return titles
		}
	}
	for _, h := range recent {
		if ctx.Err() != nil {
			break
		}
		if statuses[h.Key].ViewCount == 0 {
			continue
		} // subsequently marked unwatched
		current := h.Key
		var ordered []core.Title
		if grouped[core.ShowKey(current.TMDB())] {
			eps, e := s.metadataGrandchildren(ctx, pr, core.ShowKey(current.TMDB()))
			if e != nil {
				continue
			}
			for i := range eps {
				if eps[i].Key == current {
					ordered = eps[i+1:]
					break
				}
			}
		}
		for n := 0; n < 32; n++ {
			var next *core.Title
			if grouped[core.ShowKey(current.TMDB())] {
				if n >= len(ordered) {
					break
				}
				next = &ordered[n]
			} else {
				next, err = s.d.Catalog.NextEpisode(ctx, current)
				if err != nil || next == nil || next.Key == current {
					break
				}
			}
			current = next.Key
			if next.Season == 0 {
				continue
			}
			if !next.Released.IsZero() && next.Released.After(s.now()) {
				break
			}
			if !s.visibleTitle(ctx, pr.User, next) || s.sameDayUnavailable(ctx, pr, next) {
				break
			}
			st, e := s.d.State.Get(ctx, pr.User.ID, next.Key)
			if e != nil {
				break
			}
			if st.ViewCount > 0 && st.ViewOffset <= 0 {
				continue
			}
			st.Key = next.Key
			if st.LastViewedAt.Before(h.ViewedAt) {
				st.LastViewedAt = h.ViewedAt
			}
			state[next.Key] = st
			titles = append(titles, *next)
			break
		}
	}
	return titles
}
