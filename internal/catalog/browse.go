package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Browse queries the browsable mirror entirely in SQL.
func (c *Catalog) Browse(ctx context.Context, q core.BrowseQuery) (core.Page, error) {
	where, args, err := c.browseWhere(q.Filter)
	if err != nil {
		return core.Page{}, err
	}
	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_titles t WHERE `+where, args...).Scan(&total); err != nil {
		return core.Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	order := browseOrder(q)
	query := `SELECT ` + prefixedCols("t") + ` FROM catalog_titles t WHERE ` + where + ` ORDER BY ` + order + `, t.key ASC LIMIT ? OFFSET ?`
	items, err := c.queryTitles(ctx, query, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return core.Page{}, err
	}
	if items == nil {
		items = []core.Title{}
	}
	return core.Page{Items: items, Total: total}, nil
}

func prefixedCols(prefix string) string {
	parts := strings.Split(titleCols, ",")
	for i, p := range parts {
		parts[i] = prefix + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ",")
}

func browseOrder(q core.BrowseQuery) string {
	col := "t.popularity"
	switch q.Sort {
	case core.SortTitle:
		col = "t.sort_title"
	case core.SortReleased:
		col = "t.released_ts"
	case core.SortRating:
		col = "t.rating"
	case core.SortAdded:
		col = "t.added_at"
	case core.SortPopularity, "":
		col = "t.popularity"
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	// Zero-value query: most popular first is the only sensible default.
	if q.Sort == "" && !q.Desc {
		dir = "DESC"
	}
	return col + " " + dir
}

func (c *Catalog) browseWhere(f core.Filter) (string, []any, error) {
	var conds []string
	var args []any
	conds = append(conds, "t.browsable=1", "t.adult=0")
	switch f.Type {
	case core.TypeMovie, core.TypeShow:
		conds = append(conds, "t.type=?")
		args = append(args, string(f.Type))
	case "":
		conds = append(conds, "t.type IN ('movie','show')")
	default:
		return "", nil, fmt.Errorf("catalog: browse: unsupported type %q", f.Type)
	}
	if len(f.Genres) > 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_genres g WHERE g.key=t.key AND g.genre IN (`+placeholders(len(f.Genres))+`))`)
		for _, g := range f.Genres {
			args = append(args, g)
		}
	}
	if f.YearFrom > 0 {
		conds = append(conds, "t.year>=?")
		args = append(args, f.YearFrom)
	}
	if f.YearTo > 0 {
		conds = append(conds, "t.year<=? AND t.year>0")
		args = append(args, f.YearTo)
	}
	if f.MinRating > 0 {
		conds = append(conds, "t.rating>=?")
		args = append(args, f.MinRating)
	}
	if f.MaxRuntime > 0 {
		conds = append(conds, "t.runtime_sec>0 AND t.runtime_sec<=?")
		args = append(args, int64(f.MaxRuntime.Seconds()))
	}
	if f.MinRuntime > 0 {
		conds = append(conds, "t.runtime_sec>=?")
		args = append(args, int64(f.MinRuntime.Seconds()))
	}
	if len(f.Languages) > 0 {
		conds = append(conds, "t.language IN ("+placeholders(len(f.Languages))+")")
		for _, l := range f.Languages {
			args = append(args, l)
		}
	}
	if len(f.Countries) > 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_countries co WHERE co.key=t.key AND co.country IN (`+placeholders(len(f.Countries))+`))`)
		for _, v := range f.Countries {
			args = append(args, v)
		}
	}
	if len(f.ContentRatings) > 0 {
		conds = append(conds, "t.content_rating IN ("+placeholders(len(f.ContentRatings))+")")
		for _, v := range f.ContentRatings {
			args = append(args, v)
		}
	}
	if f.MaxContentRating != "" {
		if rank := RatingRank(f.MaxContentRating); rank > 0 {
			conds = append(conds, "t.content_rating_rank>0 AND t.content_rating_rank<=?")
			args = append(args, rank)
		} else {
			conds = append(conds, "0=1")
		}
	}
	if f.Collection > 0 {
		conds = append(conds, "t.collection_id=?")
		args = append(args, f.Collection)
	}
	if f.Person > 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_credits cr WHERE cr.key=t.key AND cr.person_tmdb=?)`)
		args = append(args, f.Person)
	}
	if f.Studio != "" {
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_studios s WHERE s.key=t.key AND s.studio=?)`)
		args = append(args, f.Studio)
	}
	if f.Query != "" {
		conds = append(conds, "(t.title LIKE ? ESCAPE '\\' OR t.original_title LIKE ? ESCAPE '\\')")
		like := "%" + escapeLike(f.Query) + "%"
		args = append(args, like, like)
	}
	conds = append(conds, "t.released_ts>0 AND t.released_ts<=?")
	args = append(args, c.now().Unix())
	return strings.Join(conds, " AND "), args, nil
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Facet returns value counts over the browsable set.
func (c *Catalog) Facet(ctx context.Context, t core.MediaType, facet string) ([]core.FacetValue, error) {
	typeCond := "t.type IN ('movie','show')"
	var args []any
	if t == core.TypeMovie || t == core.TypeShow {
		typeCond = "t.type=?"
		args = append(args, string(t))
	}
	base := "t.browsable=1 AND t.adult=0 AND t.released_ts>0 AND t.released_ts<=? AND " + typeCond
	args = append([]any{c.now().Unix()}, args...)
	var q string
	switch facet {
	case "genre":
		q = `SELECT g.genre, COUNT(*) FROM catalog_genres g JOIN catalog_titles t ON t.key=g.key WHERE ` + base + ` GROUP BY g.genre ORDER BY COUNT(*) DESC, g.genre ASC`
	case "studio":
		q = `SELECT s.studio, COUNT(*) FROM catalog_studios s JOIN catalog_titles t ON t.key=s.key WHERE ` + base + ` GROUP BY s.studio ORDER BY COUNT(*) DESC, s.studio ASC LIMIT 200`
	case "country":
		q = `SELECT co.country, COUNT(*) FROM catalog_countries co JOIN catalog_titles t ON t.key=co.key WHERE ` + base + ` GROUP BY co.country ORDER BY COUNT(*) DESC, co.country ASC LIMIT 200`
	case "year":
		q = `SELECT CAST(t.year AS TEXT), COUNT(*) FROM catalog_titles t WHERE ` + base + ` AND t.year>0 GROUP BY t.year ORDER BY t.year DESC`
	case "decade":
		q = `SELECT CAST((t.year/10)*10 AS TEXT), COUNT(*) FROM catalog_titles t WHERE ` + base + ` AND t.year>0 GROUP BY (t.year/10)*10 ORDER BY (t.year/10)*10 DESC`
	case "contentRating":
		q = `SELECT t.content_rating, COUNT(*) FROM catalog_titles t WHERE ` + base + ` AND t.content_rating<>'' GROUP BY t.content_rating ORDER BY t.content_rating_rank ASC, t.content_rating ASC`
	default:
		return nil, fmt.Errorf("catalog: unknown facet %q", facet)
	}
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.FacetValue
	for rows.Next() {
		var v core.FacetValue
		if err := rows.Scan(&v.Key, &v.Count); err != nil {
			return nil, err
		}
		v.Title = v.Key
		if facet == "decade" {
			v.Title = v.Key + "s"
		}
		out = append(out, v)
	}
	if out == nil {
		out = []core.FacetValue{}
	}
	return out, rows.Err()
}
