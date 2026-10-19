package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const titleCols = `key,type,tmdb_id,imdb,tvdb,show_key,show_tmdb,show_title,season_title,season,episode,
title,original_title,sort_title,tagline,summary,year,released_ts,runtime_sec,status,language,
content_rating,rating,vote_count,popularity,poster,backdrop,still,logo,collection_id,collection_name,
child_count,leaf_count,browsable,added_at,fetched_at,detail,adult`

type storedTitle struct {
	t         core.Title
	browsable bool
	addedAt   time.Time
	fetchedAt time.Time
	detail    bool
}

func (c *Catalog) loadByKey(ctx context.Context, key core.RatingKey) (*storedTitle, error) {
	row := c.db.QueryRowContext(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE key=?`, int64(key))
	st, err := scanTitle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	items := []core.Title{st.t}
	if err := c.hydrateSlice(ctx, items); err != nil {
		return nil, err
	}
	st.t = items[0]
	return st, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanTitle(s scanner) (*storedTitle, error) {
	var st storedTitle
	var key, showKey int64
	var typ, imdb, showTitle, seasonTitle, title, orig, sortT, tagline, summary, status, lang, crating string
	var tmdbID, tvdb, showTMDB int64
	var season, episode, year, voteCount, collID, child, leaf, browsable, detail, adult int
	var releasedTS, runtimeSec, addedAt, fetchedAt int64
	var rating, pop float64
	var collName, poster, backdrop, still, logo string
	err := s.Scan(&key, &typ, &tmdbID, &imdb, &tvdb, &showKey, &showTMDB, &showTitle, &seasonTitle, &season, &episode,
		&title, &orig, &sortT, &tagline, &summary, &year, &releasedTS, &runtimeSec, &status, &lang,
		&crating, &rating, &voteCount, &pop, &poster, &backdrop, &still, &logo, &collID, &collName,
		&child, &leaf, &browsable, &addedAt, &fetchedAt, &detail, &adult)
	if err != nil {
		return nil, err
	}
	t := core.Title{
		Key: core.RatingKey(key), Type: core.MediaType(typ),
		IDs:     core.IDs{TMDB: tmdbID, IMDB: imdb, TVDB: tvdb},
		ShowKey: core.RatingKey(showKey), ShowTitle: showTitle, SeasonTitle: seasonTitle,
		Season: season, Episode: episode, Title: title, OriginalTitle: orig, SortTitle: sortT,
		Tagline: tagline, Summary: summary, Year: year, Runtime: time.Duration(runtimeSec) * time.Second,
		Status: status, Language: lang, ContentRating: crating, Adult: adult != 0, Rating: rating, VoteCount: voteCount,
		Popularity: pop, PosterPath: poster, BackdropPath: backdrop, StillPath: still, LogoPath: logo,
		CollectionID: int64(collID), CollectionName: collName, ChildCount: child, LeafCount: leaf,
	}
	if showTMDB != 0 {
		t.ShowIDs.TMDB = showTMDB
	}
	if releasedTS != 0 {
		t.Released = time.Unix(releasedTS, 0).UTC()
	}
	if fetchedAt != 0 {
		t.UpdatedAt = time.Unix(fetchedAt, 0).UTC()
	}
	st.t = t
	st.browsable = browsable != 0
	st.detail = detail != 0
	if addedAt != 0 {
		st.addedAt = time.Unix(addedAt, 0).UTC()
	}
	if fetchedAt != 0 {
		st.fetchedAt = time.Unix(fetchedAt, 0).UTC()
	}
	// Fill ShowIDs imdb/tvdb lazily from the show row when needed by caller.
	return &st, nil
}

// hydrateSlice fills Genres/Studios/Countries/Cast/Directors/Writers/ShowIDs in place.
func (c *Catalog) hydrateSlice(ctx context.Context, items []core.Title) error {
	if len(items) == 0 {
		return nil
	}
	idx := map[core.RatingKey]int{}
	for i := range items {
		idx[items[i].Key] = i
	}
	args := keyArgs(items)
	ph := placeholders(len(items))
	// genres
	if err := c.scanPairs(ctx, `SELECT key,genre FROM catalog_genres WHERE key IN (`+ph+`) ORDER BY genre`, args, func(i int, v string) {
		items[i].Genres = append(items[i].Genres, v)
	}, idx); err != nil {
		return err
	}
	if err := c.scanPairs(ctx, `SELECT key,keyword FROM catalog_keywords WHERE key IN (`+ph+`) ORDER BY keyword`, args, func(i int, v string) {
		items[i].Keywords = append(items[i].Keywords, v)
	}, idx); err != nil {
		return err
	}
	if err := c.scanPairs(ctx, `SELECT key,studio FROM catalog_studios WHERE key IN (`+ph+`) ORDER BY studio`, args, func(i int, v string) {
		items[i].Studios = append(items[i].Studios, v)
	}, idx); err != nil {
		return err
	}
	if err := c.scanPairs(ctx, `SELECT key,country FROM catalog_countries WHERE key IN (`+ph+`) ORDER BY country`, args, func(i int, v string) {
		items[i].Countries = append(items[i].Countries, v)
	}, idx); err != nil {
		return err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT key,person_tmdb,name,role,kind,thumb FROM catalog_credits WHERE key IN (`+ph+`) ORDER BY kind,ord`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, pid int64
		var name, role, kind, thumb string
		if err := rows.Scan(&k, &pid, &name, &role, &kind, &thumb); err != nil {
			return err
		}
		i, ok := idx[core.RatingKey(k)]
		if !ok {
			continue
		}
		p := core.Person{TMDB: pid, Name: name, Role: role, ThumbPath: thumb}
		switch kind {
		case "director":
			items[i].Directors = append(items[i].Directors, p)
		case "writer":
			items[i].Writers = append(items[i].Writers, p)
		default:
			items[i].Cast = append(items[i].Cast, p)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// ShowIDs for seasons/episodes.
	for i := range items {
		if items[i].ShowKey != 0 && (items[i].ShowIDs.IMDB == "" || items[i].ShowIDs.TVDB == 0) {
			var imdb string
			var tvdb int64
			_ = c.db.QueryRowContext(ctx, `SELECT imdb,tvdb FROM catalog_titles WHERE key=?`, int64(items[i].ShowKey)).Scan(&imdb, &tvdb)
			if items[i].ShowIDs.IMDB == "" {
				items[i].ShowIDs.IMDB = imdb
			}
			if items[i].ShowIDs.TVDB == 0 {
				items[i].ShowIDs.TVDB = tvdb
			}
		}
	}
	return nil
}

func (c *Catalog) scanPairs(ctx context.Context, q string, args []any, apply func(i int, v string), idx map[core.RatingKey]int) error {
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k int64
		var v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if i, ok := idx[core.RatingKey(k)]; ok {
			apply(i, v)
		}
	}
	return rows.Err()
}

func keyArgs(items []core.Title) []any {
	args := make([]any, len(items))
	for i := range items {
		args[i] = int64(items[i].Key)
	}
	return args
}
func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	s := "?"
	for i := 1; i < n; i++ {
		s += ",?"
	}
	return s
}

type upsertOpts struct {
	browsable bool // request browsable (added_at set if newly browsable)
	detail    bool
}

// UpsertTitle stores a title (exported for tests/seeders and other components that
// need to insert known titles). Browsable/added_at are preserved once set.
func (c *Catalog) UpsertTitle(ctx context.Context, t core.Title, browsable bool) error {
	return c.upsert(ctx, t, upsertOpts{browsable: browsable})
}

func (c *Catalog) upsert(ctx context.Context, t core.Title, o upsertOpts) error {
	return c.upsertMany(ctx, []core.Title{t}, o)
}

func (c *Catalog) upsertMany(ctx context.Context, titles []core.Title, o upsertOpts) error {
	if len(titles) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := c.now().Unix()
	for _, t := range titles {
		if err := upsertOne(ctx, tx, t, o, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertOne(ctx context.Context, tx *sql.Tx, t core.Title, o upsertOpts, now int64) error {
	if t.Key == 0 {
		return fmt.Errorf("catalog: upsert title with zero key")
	}
	if t.SortTitle == "" {
		t.SortTitle = sortTitle(t.Title)
	}
	showTMDB := t.ShowIDs.TMDB
	if showTMDB == 0 && t.ShowKey != 0 {
		showTMDB = t.ShowKey.TMDB()
	}
	// For episodes/seasons, IDs.TMDB is their own TMDB id. show_tmdb stored separately.
	var releasedTS int64
	if !t.Released.IsZero() {
		releasedTS = t.Released.Unix()
		if t.Year == 0 {
			t.Year = t.Released.Year()
		}
	}
	browsableInt := 0
	if o.browsable {
		browsableInt = 1
	}
	detailInt := 0
	if o.detail {
		detailInt = 1
	}
	// Keep richer existing fields when a summary upsert would blank them:
	// handled per-field with CASE in ON CONFLICT for a few detail-only fields.
	_, err := tx.ExecContext(ctx, `
INSERT INTO catalog_titles (key,type,tmdb_id,imdb,tvdb,show_key,show_tmdb,show_title,season_title,season,episode,
 title,original_title,sort_title,tagline,summary,year,released_ts,runtime_sec,status,language,
 content_rating,content_rating_rank,rating,vote_count,popularity,poster,backdrop,still,logo,
 collection_id,collection_name,child_count,leaf_count,browsable,added_at,fetched_at,detail,adult)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET
 type=excluded.type, tmdb_id=excluded.tmdb_id, adult=excluded.adult,
 imdb=CASE WHEN excluded.imdb<>'' THEN excluded.imdb ELSE catalog_titles.imdb END,
 tvdb=CASE WHEN excluded.tvdb<>0 THEN excluded.tvdb ELSE catalog_titles.tvdb END,
 show_key=excluded.show_key, show_tmdb=excluded.show_tmdb, show_title=excluded.show_title,
 season_title=excluded.season_title, season=excluded.season, episode=excluded.episode,
 title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE catalog_titles.title END,
 original_title=CASE WHEN excluded.original_title<>'' THEN excluded.original_title ELSE catalog_titles.original_title END,
 sort_title=CASE WHEN excluded.sort_title<>'' THEN excluded.sort_title ELSE catalog_titles.sort_title END,
 tagline=CASE WHEN excluded.tagline<>'' THEN excluded.tagline ELSE catalog_titles.tagline END,
 summary=CASE WHEN excluded.summary<>'' THEN excluded.summary ELSE catalog_titles.summary END,
 year=CASE WHEN excluded.year<>0 THEN excluded.year ELSE catalog_titles.year END,
 released_ts=CASE WHEN excluded.detail=1 OR (catalog_titles.detail=0 AND excluded.released_ts<>0) THEN excluded.released_ts ELSE catalog_titles.released_ts END,
 runtime_sec=CASE WHEN excluded.runtime_sec<>0 THEN excluded.runtime_sec ELSE catalog_titles.runtime_sec END,
 status=CASE WHEN excluded.status<>'' THEN excluded.status ELSE catalog_titles.status END,
 language=CASE WHEN excluded.language<>'' THEN excluded.language ELSE catalog_titles.language END,
 content_rating=CASE WHEN excluded.content_rating<>'' THEN excluded.content_rating ELSE catalog_titles.content_rating END,
 content_rating_rank=CASE WHEN excluded.content_rating<>'' THEN excluded.content_rating_rank ELSE catalog_titles.content_rating_rank END,
 rating=excluded.rating, vote_count=excluded.vote_count, popularity=excluded.popularity,
 poster=CASE WHEN excluded.poster<>'' THEN excluded.poster ELSE catalog_titles.poster END,
 backdrop=CASE WHEN excluded.backdrop<>'' THEN excluded.backdrop ELSE catalog_titles.backdrop END,
 still=CASE WHEN excluded.still<>'' THEN excluded.still ELSE catalog_titles.still END,
 logo=CASE WHEN excluded.logo<>'' THEN excluded.logo ELSE catalog_titles.logo END,
 collection_id=CASE WHEN excluded.collection_id<>0 THEN excluded.collection_id ELSE catalog_titles.collection_id END,
 collection_name=CASE WHEN excluded.collection_name<>'' THEN excluded.collection_name ELSE catalog_titles.collection_name END,
 child_count=CASE WHEN excluded.child_count<>0 THEN excluded.child_count ELSE catalog_titles.child_count END,
 leaf_count=CASE WHEN excluded.leaf_count<>0 THEN excluded.leaf_count ELSE catalog_titles.leaf_count END,
 browsable=CASE WHEN excluded.browsable>catalog_titles.browsable THEN excluded.browsable ELSE catalog_titles.browsable END,
 added_at=CASE WHEN catalog_titles.added_at<>0 THEN catalog_titles.added_at
               WHEN excluded.browsable=1 THEN excluded.added_at ELSE 0 END,
 fetched_at=excluded.fetched_at,
 detail=CASE WHEN excluded.detail>catalog_titles.detail THEN excluded.detail ELSE catalog_titles.detail END`,
		int64(t.Key), string(t.Type), t.IDs.TMDB, t.IDs.IMDB, t.IDs.TVDB, int64(t.ShowKey), showTMDB, t.ShowTitle, t.SeasonTitle, t.Season, t.Episode,
		t.Title, t.OriginalTitle, t.SortTitle, t.Tagline, t.Summary, t.Year, releasedTS, int64(t.Runtime.Seconds()), t.Status, t.Language,
		t.ContentRating, RatingRank(t.ContentRating), t.Rating, t.VoteCount, t.Popularity, t.PosterPath, t.BackdropPath, t.StillPath, t.LogoPath,
		t.CollectionID, t.CollectionName, t.ChildCount, t.LeafCount, browsableInt, addedAtFor(browsableInt, now), now, detailInt, t.Adult)
	if err != nil {
		return fmt.Errorf("catalog: upsert %d: %w", t.Key, err)
	}
	// Replace join rows only when the incoming title carries that facet, so
	// summary upserts don't wipe detail-fetched genres/credits.
	if t.Genres != nil {
		if err := replacePairs(ctx, tx, "catalog_genres", "genre", int64(t.Key), t.Genres); err != nil {
			return err
		}
	}
	if t.Keywords != nil {
		if err := replacePairs(ctx, tx, "catalog_keywords", "keyword", int64(t.Key), t.Keywords); err != nil {
			return err
		}
	}
	if t.Studios != nil {
		if err := replacePairs(ctx, tx, "catalog_studios", "studio", int64(t.Key), t.Studios); err != nil {
			return err
		}
	}
	if t.Countries != nil {
		if err := replacePairs(ctx, tx, "catalog_countries", "country", int64(t.Key), t.Countries); err != nil {
			return err
		}
	}
	if t.Cast != nil || t.Directors != nil || t.Writers != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_credits WHERE key=?`, int64(t.Key)); err != nil {
			return err
		}
		insert := func(ps []core.Person, kind string) error {
			for i, p := range ps {
				if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_credits(key,person_tmdb,name,role,kind,ord,thumb) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
					int64(t.Key), p.TMDB, p.Name, p.Role, kind, i, p.ThumbPath); err != nil {
					return err
				}
			}
			return nil
		}
		if err := insert(t.Cast, "cast"); err != nil {
			return err
		}
		if err := insert(t.Directors, "director"); err != nil {
			return err
		}
		if err := insert(t.Writers, "writer"); err != nil {
			return err
		}
	}
	return nil
}

func addedAtFor(browsable int, now int64) int64 {
	if browsable != 0 {
		return now
	}
	return 0
}

func replacePairs(ctx context.Context, tx *sql.Tx, table, col string, key int64, vals []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE key=?`, key); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(key,`+col+`) VALUES(?,?) ON CONFLICT DO NOTHING`, key, v); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) queryTitles(ctx context.Context, q string, args ...any) ([]core.Title, error) {
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Title
	for rows.Next() {
		st, err := scanTitle(rows)
		if err != nil {
			return nil, err
		}
		if !st.t.Adult && st.t.IsReleased(c.now()) {
			out = append(out, st.t)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := c.hydrateSlice(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Catalog) getMeta(ctx context.Context, k string) string {
	var v string
	_ = c.db.QueryRowContext(ctx, `SELECT v FROM catalog_meta WHERE k=?`, k).Scan(&v)
	return v
}
func (c *Catalog) setMeta(ctx context.Context, k, v string) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO catalog_meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}
