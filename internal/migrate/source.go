package migrate

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// PMS's owner history endpoint includes every account unless explicitly scoped.
// The source's local account ID is distinct from a plex.tv account ID (the PMS
// owner, for example, is account 1). Never infer it from the destination row ID.
func (s *Service) sourceAccount(ctx context.Context, base, token string, v Viewer) (int64, error) {
	accounts, err := s.get(ctx, base, token, "/accounts")
	if err != nil {
		return 0, err
	}
	name := v.Name
	if name == "" {
		if user, err := s.viewer(ctx, v.LocalUserID); err == nil {
			name = user.Username
		}
	}
	for _, account := range accounts.Accounts {
		if account.ID <= 0 {
			continue
		}
		if v.SourceAccountID > 0 {
			if account.ID == v.SourceAccountID {
				return account.ID, nil
			}
		} else if (name != "" && strings.EqualFold(account.Name, name)) || (v.PlexID > 0 && account.ID == v.PlexID) {
			return account.ID, nil
		}
	}
	return 0, errors.New("could not identify the source Plex history account")
}

// One preview can contain thousands of episodes of the same show. Resolve each
// source show once, and reuse the identity for history entries lacking GUIDs.
type sourceMapper struct {
	service       *Service
	base, token   string
	keys, parents map[string]core.RatingKey
	missing       map[string]bool
}

func (m *sourceMapper) resolve(ctx context.Context, item media, fetched bool) (core.RatingKey, error) {
	if item.Key != "" {
		if key := m.keys[item.Key]; key != 0 {
			return key, nil
		}
		if m.missing[item.Key] {
			return 0, core.ErrNotFound
		}
	}
	var key core.RatingKey
	err := core.ErrNotFound
	if item.Type == "episode" && item.Grandparent != "" {
		show, seen := m.parents[item.Grandparent]
		if !seen {
			c, e := m.service.get(ctx, m.base, m.token, "/library/metadata/"+url.PathEscape(item.Grandparent)+"?includeGuids=1")
			if e == nil {
				for _, d := range c.Directories {
					show, _ = m.service.mapMedia(ctx, m.base, m.token, media{Type: "show", GUID: d.GUID, GUIDs: d.GUIDs})
					if show != 0 {
						break
					}
				}
			}
			m.parents[item.Grandparent] = show
		}
		if show != 0 {
			key, err = core.EpisodeKey(show.TMDB(), item.Season, item.Episode), nil
		}
	} else {
		key, err = m.service.mapMedia(ctx, m.base, m.token, item)
	}
	if err != nil && !fetched && item.Key != "" {
		c, e := m.service.get(ctx, m.base, m.token, "/library/metadata/"+url.PathEscape(item.Key)+"?includeGuids=1")
		if e == nil {
			details := append(c.Videos, c.Metadata...)
			if len(details) > 0 {
				key, err = m.resolve(ctx, details[0], true)
			}
		}
	}
	if item.Key != "" {
		if err == nil {
			m.keys[item.Key] = key
		} else {
			m.missing[item.Key] = true
		}
	}
	return key, err
}
