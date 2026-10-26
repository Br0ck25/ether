package catalog

import (
	"context"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/libraryfilter"
)

func (c *Catalog) visibilityFilter() *libraryfilter.Filter {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.visibility
}
func (c *Catalog) Visible(t *core.Title) bool { return !c.visibilityFilter().Hidden(t) }

// FilterVisibility enriches remote summaries with already-known local facets;
// there are no detail fetches or indexer calls. Unknown metadata stays unknown.
func (c *Catalog) FilterVisibility(ctx context.Context, ts []core.Title) []core.Title {
	if len(c.config().Exclusions) == 0 {
		return ts
	}
	if len(ts) == 0 {
		return ts
	}
	out := make([]core.Title, 0, len(ts))
	idx := map[core.RatingKey]int{}
	for _, t := range ts {
		if _, ok := idx[t.Key]; !ok {
			idx[t.Key] = len(out)
			out = append(out, t)
		}
	}
	ts = out
	// These are the only summary facets which may be absent after remote search.
	for _, p := range []struct {
		table, col string
		set        func(int, string)
	}{
		{"catalog_countries", "country", func(i int, v string) { out[i].Countries = append(out[i].Countries, v) }},
		{"catalog_genres", "genre", func(i int, v string) { out[i].Genres = append(out[i].Genres, v) }},
	} {
		if err := c.scanPairs(ctx, "SELECT key,"+p.col+" FROM "+p.table+" WHERE key IN ("+placeholders(len(ts))+")", keyArgs(ts), p.set, idx); err != nil {
			return nil
		}
	}
	keep := out[:0]
	f := c.visibilityFilter()
	for i := range out {
		if !f.Hidden(&out[i]) {
			keep = append(keep, out[i])
		}
	}
	return keep
}

type VisibilityPreview struct {
	Total          int          `json:"total"`
	Hidden         int          `json:"hidden"`
	Movies         int          `json:"movies"`
	Shows          int          `json:"shows"`
	UnknownCountry int          `json:"unknownCountry"`
	Items          []core.Title `json:"items"`
}

func (c *Catalog) PreviewVisibility(ctx context.Context, specs []core.LibraryExclusion) (any, error) {
	f, err := libraryfilter.Compile(specs)
	if err != nil {
		return nil, err
	}
	where, args := f.SQLHidden()
	p := VisibilityPreview{Items: []core.Title{}}
	err = c.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN NOT EXISTS(SELECT 1 FROM catalog_countries co WHERE co.key=t.key) THEN 1 ELSE 0 END),0) FROM catalog_titles t WHERE `+mirrorBrowsableWhere).Scan(&p.Total, &p.UnknownCountry)
	if err != nil {
		return nil, err
	}
	base := mirrorBrowsableWhere + " AND (" + where + ")"
	err = c.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN t.type='movie' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN t.type='show' THEN 1 ELSE 0 END),0) FROM catalog_titles t WHERE `+base, args...).Scan(&p.Hidden, &p.Movies, &p.Shows)
	if err != nil {
		return nil, err
	}
	p.Items, err = c.queryTitles(ctx, `SELECT `+prefixedCols("t")+` FROM catalog_titles t WHERE `+base+` ORDER BY t.popularity DESC,t.key LIMIT 48`, args...)
	return p, err
}
