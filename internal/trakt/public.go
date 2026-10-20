package trakt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Public, crowd-wide lists: what Trakt's users are watching right now, the
// most watched titles this week, the most popular and the most anticipated.
// They need only the install's client id (no user sign-in), so Ether uses
// them automatically whenever the owner has set Trakt up, as a trend
// signal for recommendations (internal/personalize).

// Public list names accepted by PublicList.
const (
	PublicTrending      = "trending"       // people watching right now
	PublicWatchedWeekly = "watched_weekly" // most watched this week
	PublicPopular       = "popular"        // popular overall
	PublicAnticipated   = "anticipated"    // most added to lists ahead of release
)

// publicTTL is how long a public list is reused (stale entries are served
// while a background refresh runs).
const publicTTL = time.Hour

var publicPaths = map[string]string{
	PublicTrending:      "trending",
	PublicWatchedWeekly: "watched/weekly",
	PublicPopular:       "popular",
	PublicAnticipated:   "anticipated",
}

// PublicEnabled reports whether public lists can be fetched: Trakt is
// enabled and a client id is configured.
func (c *Client) PublicEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.Enabled && c.cfg.ClientID != ""
}

// PublicList returns one of Trakt's public lists for films or shows, best
// first, as minimal titles (key, type, title, year, ids; no catalogue
// lookups, so it is cheap). Without a configured client id it returns
// (nil, nil). Results are cached for an hour and served stale while
// refreshing.
func (c *Client) PublicList(ctx context.Context, list string, typ core.MediaType, limit int) ([]core.Title, error) {
	path, ok := publicPaths[list]
	if !ok {
		return nil, fmt.Errorf("trakt: unknown public list %q", list)
	}
	if typ != core.TypeMovie && typ != core.TypeShow {
		return nil, fmt.Errorf("trakt: public lists are for movies or shows, not %q", typ)
	}
	if !c.PublicEnabled() {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	seg := "movies"
	if typ == core.TypeShow {
		seg = "shows"
	}
	key := fmt.Sprintf("public|%s|%s|%d", list, seg, limit)
	return cachedCallTTL(c, ctx, key, publicTTL, func(ctx context.Context) ([]core.Title, error) {
		status, b, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/%s/%s?limit=%d", seg, path, limit), nil, "")
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("trakt %s %s: HTTP %d", seg, list, status)
		}
		return decodePublic(b, typ)
	})
}

// decodePublic reads a public list: either bare titles (popular) or
// wrappers carrying the title under "movie"/"show" with a count (trending,
// watched, anticipated).
func decodePublic(b []byte, typ core.MediaType) ([]core.Title, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("trakt public decode: %w", err)
	}
	out := make([]core.Title, 0, len(raw))
	seen := map[core.RatingKey]bool{}
	for _, r := range raw {
		var wrap struct {
			Movie *traktTitle `json:"movie"`
			Show  *traktTitle `json:"show"`
		}
		var tt traktTitle
		if err := json.Unmarshal(r, &wrap); err == nil && (wrap.Movie != nil || wrap.Show != nil) {
			if wrap.Movie != nil {
				tt = *wrap.Movie
			} else {
				tt = *wrap.Show
			}
		} else if err := json.Unmarshal(r, &tt); err != nil {
			continue
		}
		if tt.IDs.TMDB == 0 {
			continue
		}
		k := core.MovieKey(tt.IDs.TMDB)
		if typ == core.TypeShow {
			k = core.ShowKey(tt.IDs.TMDB)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, core.Title{Key: k, Type: typ, Title: tt.Title, Year: tt.Year,
			IDs: core.IDs{TMDB: tt.IDs.TMDB, IMDB: tt.IDs.IMDB, TVDB: tt.IDs.TVDB}})
	}
	return out, nil
}
