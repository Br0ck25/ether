package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/overlays"
	"github.com/braedonsaunders/ether/internal/personalize"
	"github.com/braedonsaunders/ether/internal/plex"
	"github.com/braedonsaunders/ether/internal/ratings"
	"github.com/braedonsaunders/ether/internal/watchlist"
	"net/http"
	"strings"
	"time"
)

func (a *App) buildDiscovery() {
	a.ratings = ratings.New(a.db, ratings.Options{})
	a.watchlist = watchlist.New(a.db, watchlist.Options{Catalog: a.catalog, Resolver: a.resolver, Engine: a.media, Trakt: a.trakt, Users: a.users.Users, Allowed: a.safety.Allowed, Notifier: a.notifications, Tokens: a.discoveryToken, PrewarmBytes: a.cfg.Get().Cache.PrefetchBytes,
		RecoverImported: func(ctx context.Context, viewer int64) error {
			return a.migration.RecoverWatchlist(ctx, viewer)
		}, ResumeSchedule: a.clustered()}) // a new leader continues the schedule
	a.discoveryPersonal = personalize.NewDiscovery(a.db, a.catalog, a.state, a.safety.Allowed)
	a.overlays = overlays.New(a.db, overlays.Options{Images: a.images, Allowed: a.safety.Allowed, Ratings: a.ratings, Facts: a.overlayFacts, Variants: a.artworkVariants, Taste: func(ctx context.Context, u *core.User) []string {
		p, err := a.personalize.Taste(ctx, u)
		if err != nil {
			return nil
		}
		var tags []string
		for _, g := range p.Genres {
			if g.Weight > 0 {
				tags = append(tags, strings.ToLower(g.Name))
			}
		}
		for _, person := range p.People {
			if person.Weight > 0 {
				tags = append(tags, strings.ToLower(person.Name))
			}
		}
		return tags
	}})
	a.plexDiscovery = &plex.DiscoveryFeatures{Watchlist: a.watchlist, Ratings: a.ratings, Overlays: a.overlays, Personal: a.discoveryPersonal, LLM: a.llm, Localize: a.tmdb.Localized}
}
func (a *App) overlayFacts(ctx context.Context, u *core.User, t *core.Title) overlays.Facts {
	f := overlays.Facts{Returning: strings.EqualFold(t.Status, "Returning Series")}
	if expiry, err := a.collections.LastChance(ctx, u, t.Key); err == nil {
		f.LastChance = expiry
	}
	if t.Type == core.TypeShow && u != nil {
		episodes, err := a.catalog.RecentEpisodes(ctx, t.Key, time.Now().Add(-30*24*time.Hour))
		if err == nil {
			keys := make([]core.RatingKey, len(episodes))
			for i, ep := range episodes {
				keys[i] = ep.Key
			}
			if watched, err := a.state.GetMany(ctx, u.ID, keys); err == nil {
				for _, ep := range episodes {
					if watched[ep.Key].ViewCount == 0 {
						f.NewEpisode = true
						if ep.Episode == 1 {
							f.NewSeason = true
						}
					}
				}
			}
		}
	}
	for _, s := range a.playback.Sessions() {
		if s.Key == t.Key {
			for _, stream := range s.Info.Streams {
				if stream.Type == "video" {
					f.Height = stream.Height
					f.HDR = stream.ColorTransfer == "smpte2084" || stream.ColorTransfer == "arib-std-b67"
					f.DolbyVision = stream.DolbyVision
				}
				if strings.Contains(strings.ToLower(stream.Profile+" "+stream.Title), "atmos") {
					f.Atmos = true
				}
			}
		}
	}
	return f
}
func (a *App) discoveryHandlers(m map[string]http.Handler) {
	m["discovery"] = a.watchlist.Router()
	m["discovery/ratings"] = a.ratings.Router(a.catalog)
	m["discovery/overlays"] = a.overlays.Router(a.catalog, func(r *http.Request, id int64) (*core.User, error) {
		u, err := a.users.User(r.Context(), id)
		if err != nil {
			return nil, err
		}
		return a.safety.UserPolicy(u), nil
	})
}

func (a *App) discoveryToken(ctx context.Context, id int64) (string, error) {
	u, err := a.users.User(ctx, id)
	if err != nil {
		return "", err
	}
	if u.IsOwner {
		return a.cfg.Get().Plex.Token, nil
	}
	users, err := a.users.AllUsers(ctx)
	if err != nil {
		return "", err
	}
	for _, record := range users {
		if record.ID == id && record.Active && record.Home && record.Restricted {
			return a.plextv.HomeUserToken(ctx, a.cfg.Get().Plex.Token, u.PlexID, "")
		}
	}
	return "", core.ErrUnauthorized
}
func (a *App) artworkVariants(ctx context.Context, t *core.Title, lang string) (overlays.Artwork, error) {
	images, err := a.tmdb.ArtworkVariants(ctx, t, lang)
	if err != nil {
		return overlays.Artwork{}, err
	}
	out := overlays.Artwork{}
	for _, img := range images.Posters {
		out.Posters = append(out.Posters, overlays.Variant{Path: img.FilePath, Language: img.ISO639})
	}
	for _, img := range images.Backdrops {
		out.Backdrops = append(out.Backdrops, overlays.Variant{Path: img.FilePath, Language: img.ISO639})
	}
	return out, nil
}
