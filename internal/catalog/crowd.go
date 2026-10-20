package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// Crowd co-engagement lists. TMDB's /recommendations for a title is what
// people who engaged with it also engaged with: public collaborative
// filtering over millions of users. Personalisation aggregates these lists
// over a viewer's favourites (internal/personalize, crowd.go), so they are
// persisted here and reused for days: they change slowly, and a viewer's
// favourites recur from build to build. Lists also arrive for free with
// every title detail fetch (append_to_response), which is harvested.

func init() {
	store.Register("catalog", 4, `CREATE TABLE catalog_crowd (
  key INTEGER PRIMARY KEY,
  fetched_at INTEGER NOT NULL,
  pages INTEGER NOT NULL,
  recs TEXT NOT NULL
);`)
}

// DefaultCrowdTTL is how long a stored co-engagement list is fresh.
const DefaultCrowdTTL = 72 * time.Hour

// maxCrowdPages bounds the pages fetched per title (20 results each).
const maxCrowdPages = 2

// WithCrowdTTL overrides how long co-engagement lists stay fresh.
func WithCrowdTTL(d time.Duration) Option { return func(c *Catalog) { c.crowdTTL = d } }

// crowdKey maps seasons and episodes onto their show; films and shows map
// to themselves. Other kinds have no list.
func crowdKey(key core.RatingKey) (core.RatingKey, bool) {
	switch key.Kind() {
	case core.KindMovie, core.KindShow:
		return key, true
	case core.KindSeason, core.KindEpisode:
		return core.ShowKey(key.TMDB()), true
	}
	return 0, false
}

// CrowdRecommendations returns TMDB's crowd co-engagement list for a title
// (/recommendations only: never metadata similarity), best first, from up
// to pages pages (1–2). A stored list younger than the crowd TTL with
// enough pages is served from the local mirror. Otherwise, with fetch set,
// TMDB is asked and fetched reports it (callers budget those calls);
// without fetch, or when TMDB fails, a stale list is served when there is
// one and nil otherwise. Titles not yet released are dropped.
func (c *Catalog) CrowdRecommendations(ctx context.Context, key core.RatingKey, pages int, fetch bool) (titles []core.Title, fetched bool, err error) {
	ck, ok := crowdKey(key)
	if !ok {
		return nil, false, core.ErrNotFound
	}
	pages = max(1, min(pages, maxCrowdPages))
	keys, at, have, err := c.loadCrowd(ctx, ck, pages)
	if err != nil {
		return nil, false, err
	}
	if have && c.now().Sub(at) < c.crowdTTLOrDefault() {
		out, err := c.titlesByKeys(ctx, keys)
		return out, false, err
	}
	if !fetch || c.client == nil {
		if keys == nil {
			return nil, false, nil
		}
		out, err := c.titlesByKeys(ctx, keys)
		return out, false, err
	}
	fresh, ferr := c.fetchCrowd(ctx, ck, pages)
	if ferr != nil {
		if keys != nil {
			out, err := c.titlesByKeys(ctx, keys)
			return out, true, err
		}
		return nil, true, ferr
	}
	if err := c.storeCrowd(ctx, ck, fresh, pages); err != nil {
		return nil, true, err
	}
	out, err := c.titlesByKeys(ctx, keyList(fresh))
	return out, true, err
}

func (c *Catalog) crowdTTLOrDefault() time.Duration {
	if c.crowdTTL > 0 {
		return c.crowdTTL
	}
	return DefaultCrowdTTL
}

// loadCrowd reads a stored list. have reports a list with at least pages
// pages; keys is any stored list (for stale serving).
func (c *Catalog) loadCrowd(ctx context.Context, key core.RatingKey, pages int) (keys []core.RatingKey, at time.Time, have bool, err error) {
	var fetchedAt int64
	var stored int
	var recs string
	err = c.db.QueryRowContext(ctx, `SELECT fetched_at, pages, recs FROM catalog_crowd WHERE key=?`, int64(key)).Scan(&fetchedAt, &stored, &recs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	keys = []core.RatingKey{}
	for _, s := range strings.Split(recs, ",") {
		if n, perr := strconv.ParseInt(s, 10, 64); perr == nil && n != 0 {
			keys = append(keys, core.RatingKey(n))
		}
	}
	return keys, time.Unix(fetchedAt, 0), stored >= pages, nil
}

// fetchCrowd asks TMDB for up to pages pages of recommendations.
func (c *Catalog) fetchCrowd(ctx context.Context, key core.RatingKey, pages int) ([]core.Title, error) {
	id := key.TMDB()
	var out []core.Title
	for p := 1; p <= pages; p++ {
		total := 0
		switch key.Kind() {
		case core.KindMovie:
			res, err := c.client.MovieRecommendations(ctx, id, p)
			if err != nil {
				if p > 1 {
					return out, nil // page 1 is the list that matters
				}
				return nil, err
			}
			for _, m := range res.Results {
				out = append(out, c.movieToTitle(ctx, m))
			}
			total = res.TotalPages
		default:
			res, err := c.client.TVRecommendations(ctx, id, p)
			if err != nil {
				if p > 1 {
					return out, nil
				}
				return nil, err
			}
			for _, s := range res.Results {
				out = append(out, c.tvToTitle(ctx, s))
			}
			total = res.TotalPages
		}
		if p >= total {
			break
		}
	}
	return out, nil
}

// storeCrowd mirrors a fetched list's titles and stores the list.
func (c *Catalog) storeCrowd(ctx context.Context, key core.RatingKey, titles []core.Title, pages int) error {
	seen := map[core.RatingKey]bool{key: true}
	kept := titles[:0:0]
	for _, t := range titles {
		if t.Key == 0 || t.Adult || seen[t.Key] {
			continue
		}
		seen[t.Key] = true
		kept = append(kept, t)
	}
	var browsable, mirrorOnly []core.Title
	for _, t := range kept {
		if c.meetsThreshold(t) {
			browsable = append(browsable, t)
		} else {
			mirrorOnly = append(mirrorOnly, t)
		}
	}
	if err := c.upsertMany(ctx, browsable, upsertOpts{browsable: true}); err != nil {
		return err
	}
	if err := c.upsertMany(ctx, mirrorOnly, upsertOpts{}); err != nil {
		return err
	}
	parts := make([]string, len(kept))
	for i, t := range kept {
		parts[i] = strconv.FormatInt(int64(t.Key), 10)
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO catalog_crowd(key, fetched_at, pages, recs) VALUES(?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET fetched_at=excluded.fetched_at, pages=excluded.pages, recs=excluded.recs`,
		int64(key), c.now().Unix(), pages, strings.Join(parts, ","))
	return err
}

// harvestCrowd keeps the first page of recommendations a detail fetch
// carried, unless a fresh list with more pages is already stored. Errors
// are ignored: this is a free bonus, never a reason for Get to fail.
func (c *Catalog) harvestCrowd(ctx context.Context, key core.RatingKey, titles []core.Title) {
	if _, at, have, err := c.loadCrowd(ctx, key, maxCrowdPages); err != nil || have && c.now().Sub(at) < c.crowdTTLOrDefault() {
		return
	}
	if err := c.storeCrowd(ctx, key, titles, 1); err != nil {
		c.log.Debug("catalog: storing harvested recommendations failed", "key", key, "err", err)
	}
}

func (c *Catalog) harvestMovieCrowd(ctx context.Context, key core.RatingKey, p *tmdb.Paged[tmdb.Movie]) {
	if p == nil {
		return
	}
	titles := make([]core.Title, 0, len(p.Results))
	for _, m := range p.Results {
		titles = append(titles, c.movieToTitle(ctx, m))
	}
	c.harvestCrowd(ctx, key, titles)
}

func (c *Catalog) harvestTVCrowd(ctx context.Context, key core.RatingKey, p *tmdb.Paged[tmdb.TVShow]) {
	if p == nil {
		return
	}
	titles := make([]core.Title, 0, len(p.Results))
	for _, s := range p.Results {
		titles = append(titles, c.tvToTitle(ctx, s))
	}
	c.harvestCrowd(ctx, key, titles)
}

func keyList(titles []core.Title) []core.RatingKey {
	out := make([]core.RatingKey, 0, len(titles))
	seen := map[core.RatingKey]bool{}
	for _, t := range titles {
		if t.Key != 0 && !t.Adult && !seen[t.Key] {
			seen[t.Key] = true
			out = append(out, t.Key)
		}
	}
	return out
}

// titlesByKeys loads mirrored titles in the given order (released, not
// adult), in batches.
func (c *Catalog) titlesByKeys(ctx context.Context, keys []core.RatingKey) ([]core.Title, error) {
	out := make([]core.Title, 0, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	byKey := make(map[core.RatingKey]core.Title, len(keys))
	const batch = 200
	for i := 0; i < len(keys); i += batch {
		part := keys[i:min(i+batch, len(keys))]
		args := make([]any, len(part))
		for j, k := range part {
			args[j] = int64(k)
		}
		ts, err := c.queryTitles(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE key IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			byKey[t.Key] = t
		}
	}
	for _, k := range keys {
		if t, ok := byKey[k]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}
