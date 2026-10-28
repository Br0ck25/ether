package migrate

import (
	"context"
	"errors"

	"github.com/braedonsaunders/ether/internal/core"
)

func (s *Service) watchlistTitles(ctx context.Context, items []Item, only bool) (map[core.RatingKey]core.Title, error) {
	titles := map[core.RatingKey]core.Title{}
	for _, item := range items {
		if !only && !item.Watchlist {
			continue
		}
		if _, ok := titles[item.Key]; ok {
			continue
		}
		if s.catalog == nil {
			return nil, errors.New("catalog unavailable")
		}
		title, err := s.catalog.Get(ctx, item.Key)
		if err != nil {
			return nil, err
		}
		if title == nil || title.Key != item.Key {
			return nil, core.ErrNotFound
		}
		titles[item.Key] = *title
	}
	return titles, nil
}
