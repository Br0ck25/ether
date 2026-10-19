package catalog

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"time"
)

// CachedTitle returns a mirrored title without contacting TMDB.
func (c *Catalog) CachedTitle(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	st, err := c.loadByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	return &st.t, nil
}

// RecentEpisodes returns only local, released episode identities for artwork
// badges. Callers apply viewer state through core.State instead of a SQL join.
func (c *Catalog) RecentEpisodes(ctx context.Context, show core.RatingKey, since time.Time) ([]core.Title, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT key,episode FROM catalog_titles WHERE show_key=? AND type='episode' AND adult=0 AND released_ts BETWEEN ? AND ?`, show, since.Unix(), c.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Title{}
	for rows.Next() {
		var t core.Title
		t.Type = core.TypeEpisode
		if err := rows.Scan(&t.Key, &t.Episode); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
