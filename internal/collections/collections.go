// Package collections implements core.Collections: TMDB franchise
// collections, live trending/list collections, and owner/user smart
// collections persisted as core.Filter+sort and evaluated via Catalog.Browse.
package collections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ether-media/ether/internal/catalog"
	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/store"
)

func init() {
	store.Register("collections", 1, `
CREATE TABLE collections_smart (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL DEFAULT '',
  filter_json TEXT NOT NULL DEFAULT '{}',
  sort TEXT NOT NULL DEFAULT 'popularity',
  owner_user_id INTEGER NOT NULL DEFAULT 0,
  poster TEXT NOT NULL DEFAULT '',
  backdrop TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX collections_smart_owner ON collections_smart(owner_user_id, type);
`)
}

// Builtin (live) collection payload ids live far above any AUTOINCREMENT smart id.
const builtinBase int64 = 900_000

type builtin struct {
	id    int64 // payload under SmartKey
	slug  string
	title string
	list  string // catalog.List name
	typ   core.MediaType
	kind  string
}

var builtins = []builtin{
	{builtinBase + 1, "trending-week-movie", "Trending Movies This Week", "trending_week", core.TypeMovie, "trending"},
	{builtinBase + 2, "trending-week-show", "Trending Shows This Week", "trending_week", core.TypeShow, "trending"},
	{builtinBase + 3, "trending-day-movie", "Trending Movies Today", "trending_day", core.TypeMovie, "trending"},
	{builtinBase + 4, "trending-day-show", "Trending Shows Today", "trending_day", core.TypeShow, "trending"},
	{builtinBase + 5, "top-rated-movie", "Top Rated Movies", "top_rated", core.TypeMovie, "trending"},
	{builtinBase + 6, "top-rated-show", "Top Rated Shows", "top_rated", core.TypeShow, "trending"},
	{builtinBase + 7, "popular-movie", "Popular Movies", "popular", core.TypeMovie, "trending"},
	{builtinBase + 8, "popular-show", "Popular Shows", "popular", core.TypeShow, "trending"},
	{builtinBase + 9, "now-playing-movie", "Now Playing", "now_playing", core.TypeMovie, "trending"},
	{builtinBase + 10, "upcoming-movie", "Upcoming Movies", "upcoming", core.TypeMovie, "trending"},
	{builtinBase + 11, "airing-today-show", "Airing Today", "airing_today", core.TypeShow, "trending"},
	{builtinBase + 12, "on-the-air-show", "On The Air", "on_the_air", core.TypeShow, "trending"},
	{builtinBase + 13, "new-releases-movie", "New Releases", "now_playing", core.TypeMovie, "trending"},
}

func builtinByKey(key core.RatingKey) (builtin, bool) {
	if key.Kind() != core.KindSmart {
		return builtin{}, false
	}
	id := key.TMDB() // payload for non-season/episode kinds
	for _, b := range builtins {
		if b.id == id {
			return b, true
		}
	}
	return builtin{}, false
}

// BuiltinKey returns the SmartKey for a builtin slug (e.g. "trending-week-movie"), or 0.
func BuiltinKey(slug string) core.RatingKey {
	for _, b := range builtins {
		if b.slug == slug {
			return core.SmartKey(b.id)
		}
	}
	return 0
}

var _ core.Collections = (*Service)(nil)

// Service implements core.Collections.
type Service struct {
	db  *sql.DB
	cat core.Catalog
	log *slog.Logger
	now func() time.Time
}

// Option customises a Service.
type Option func(*Service)

func WithNow(fn func() time.Time) Option { return func(s *Service) { s.now = fn } }

// New creates the collections service and seeds default smart collections
// when no server-wide smart collection exists yet.
func New(db *sql.DB, cat core.Catalog, log *slog.Logger, opts ...Option) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("collections: nil *sql.DB")
	}
	if cat == nil {
		return nil, fmt.Errorf("collections: nil Catalog")
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Service{db: db, cat: cat, log: log, now: time.Now}
	for _, fn := range opts {
		fn(s)
	}
	if err := s.seedDefaults(context.Background()); err != nil {
		return nil, fmt.Errorf("collections: seed: %w", err)
	}
	return s, nil
}

func (s *Service) seedDefaults(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections_smart WHERE owner_user_id=0`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, d := range defaultSmart() {
		if _, err := s.createSmart(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

func defaultSmart() []core.Collection {
	return []core.Collection{
		{Title: "80s Sci-Fi Under 2 Hours", Kind: "smart", Type: core.TypeMovie, Sort: core.SortPopularity,
			Filter: &core.Filter{Type: core.TypeMovie, Genres: []string{"Science Fiction"}, YearFrom: 1980, YearTo: 1989, MaxRuntime: 2 * time.Hour}},
		{Title: "Critically Acclaimed Movies", Kind: "smart", Type: core.TypeMovie, Sort: core.SortRating,
			Filter: &core.Filter{Type: core.TypeMovie, MinRating: 8.0}},
		{Title: "Critically Acclaimed Shows", Kind: "smart", Type: core.TypeShow, Sort: core.SortRating,
			Filter: &core.Filter{Type: core.TypeShow, MinRating: 8.0}},
		{Title: "Recently Released Movies", Kind: "smart", Type: core.TypeMovie, Sort: core.SortReleased,
			Filter: &core.Filter{Type: core.TypeMovie, YearFrom: time.Now().Year() - 1}},
		{Title: "Recently Released Shows", Kind: "smart", Type: core.TypeShow, Sort: core.SortReleased,
			Filter: &core.Filter{Type: core.TypeShow, YearFrom: time.Now().Year() - 1}},
	}
}

func maxRating(user *core.User) string {
	if user == nil {
		return ""
	}
	return user.MaxContentRating
}

// stricterRating returns whichever of a/b has the lower rank (empty = unrestricted).
func stricterRating(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if catalog.RatingRank(b) < catalog.RatingRank(a) && catalog.RatingRank(b) > 0 {
		return b
	}
	return a
}

func filterByUserRating(items []core.Title, user *core.User) []core.Title {
	max := maxRating(user)
	if max == "" {
		return items
	}
	out := make([]core.Title, 0, len(items))
	for _, t := range items {
		if !catalog.RatingExceeds(t.ContentRating, max) {
			out = append(out, t)
		}
	}
	return out
}

func pageOf(items []core.Title, offset, limit int) core.Page {
	total := len(items)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if offset > total {
		return core.Page{Items: []core.Title{}, Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return core.Page{Items: items[offset:end], Total: total}
}

// List returns franchise + live + smart collections visible to user.
func (s *Service) List(ctx context.Context, user *core.User, t core.MediaType) ([]core.Collection, error) {
	var out []core.Collection
	// Live collections.
	for _, b := range builtins {
		if t != "" && b.typ != t {
			continue
		}
		out = append(out, core.Collection{Key: core.SmartKey(b.id), Title: b.title, Kind: b.kind, Type: b.typ})
	}
	// Smart (server-wide + this user's).
	smart, err := s.listSmart(ctx, user, t)
	if err != nil {
		return nil, err
	}
	out = append(out, smart...)
	// Franchises (movies only).
	if t == "" || t == core.TypeMovie {
		fr, err := s.listFranchises(ctx)
		if err == nil {
			out = append(out, fr...)
		} else {
			s.log.Debug("collections: franchise list unavailable", "err", err)
		}
	}
	// Child counts (best effort).
	for i := range out {
		if out[i].ChildCount > 0 {
			continue
		}
		if page, err := s.Items(ctx, user, out[i].Key, 0, 1); err == nil {
			out[i].ChildCount = page.Total
			if len(page.Items) > 0 && out[i].PosterPath == "" {
				out[i].PosterPath = page.Items[0].PosterPath
				out[i].BackdropPath = page.Items[0].BackdropPath
			}
		}
	}
	if out == nil {
		out = []core.Collection{}
	}
	return out, nil
}

func (s *Service) listFranchises(ctx context.Context) ([]core.Collection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.collection_id,
		       COALESCE(NULLIF(MAX(t.collection_name),''), MAX(cc.name), ''),
		       COUNT(*),
		       COALESCE(MAX(cc.poster),''), COALESCE(MAX(cc.backdrop),''),
		       COALESCE(MAX(cc.summary),'')
		FROM catalog_titles t
		LEFT JOIN catalog_collections cc ON cc.tmdb_id = t.collection_id
		WHERE t.type='movie' AND t.browsable=1 AND t.collection_id>0
		GROUP BY t.collection_id HAVING COUNT(*)>=2
		ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Collection
	for rows.Next() {
		var id int64
		var name, poster, backdrop, summary string
		var n int
		if err := rows.Scan(&id, &name, &n, &poster, &backdrop, &summary); err != nil {
			return nil, err
		}
		if name == "" {
			name = fmt.Sprintf("Collection %d", id)
		}
		out = append(out, core.Collection{Key: core.CollectionKey(id), Title: name, Summary: summary,
			Kind: "franchise", Type: core.TypeMovie, PosterPath: poster, BackdropPath: backdrop, ChildCount: n})
	}
	return out, rows.Err()
}

func (s *Service) listSmart(ctx context.Context, user *core.User, t core.MediaType) ([]core.Collection, error) {
	q := `SELECT id,title,summary,type,filter_json,sort,owner_user_id,poster,backdrop FROM collections_smart WHERE (owner_user_id=0 OR owner_user_id=?)`
	args := []any{userID(user)}
	if t != "" {
		q += ` AND (type=? OR type='')`
		args = append(args, string(t))
	}
	q += ` ORDER BY owner_user_id ASC, title ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Collection
	for rows.Next() {
		coll, err := scanSmart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *coll)
	}
	return out, rows.Err()
}

func userID(u *core.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func scanSmart(s interface{ Scan(...any) error }) (*core.Collection, error) {
	var id, owner int64
	var title, summary, typ, filterJSON, sortOrder, poster, backdrop string
	if err := s.Scan(&id, &title, &summary, &typ, &filterJSON, &sortOrder, &owner, &poster, &backdrop); err != nil {
		return nil, err
	}
	c := &core.Collection{Key: core.SmartKey(id), Title: title, Summary: summary, Kind: "smart",
		Type: core.MediaType(typ), Sort: core.SortOrder(sortOrder), OwnerUserID: owner,
		PosterPath: poster, BackdropPath: backdrop}
	var f core.Filter
	if filterJSON != "" {
		if err := json.Unmarshal([]byte(filterJSON), &f); err != nil {
			return nil, fmt.Errorf("collections: smart %d filter: %w", id, err)
		}
		c.Filter = &f
	}
	if c.Sort == "" {
		c.Sort = core.SortPopularity
	}
	return c, nil
}

func (s *Service) getSmart(ctx context.Context, id int64) (*core.Collection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,title,summary,type,filter_json,sort,owner_user_id,poster,backdrop FROM collections_smart WHERE id=?`, id)
	c, err := scanSmart(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	return c, err
}

// Get returns one collection by key, enforcing smart-collection ownership.
func (s *Service) Get(ctx context.Context, user *core.User, key core.RatingKey) (*core.Collection, error) {
	switch key.Kind() {
	case core.KindCollection:
		id := key.TMDB()
		name, items, err := s.cat.Collection(ctx, id)
		if err != nil {
			// Fall back to a shell from the mirror so the key still resolves.
			for _, fr := range mustFranchises(ctx, s) {
				if fr.Key == key {
					c := fr
					return &c, nil
				}
			}
			return nil, err
		}
		c := &core.Collection{Key: key, Title: name, Kind: "franchise", Type: core.TypeMovie,
			ChildCount: len(filterByUserRating(items, user))}
		if len(items) > 0 {
			c.PosterPath = items[0].PosterPath
			c.BackdropPath = items[0].BackdropPath
		}
		return c, nil
	case core.KindSmart:
		if b, ok := builtinByKey(key); ok {
			c := &core.Collection{Key: key, Title: b.title, Kind: b.kind, Type: b.typ}
			if page, err := s.Items(ctx, user, key, 0, 1); err == nil {
				c.ChildCount = page.Total
				if len(page.Items) > 0 {
					c.PosterPath = page.Items[0].PosterPath
					c.BackdropPath = page.Items[0].BackdropPath
				}
			}
			return c, nil
		}
		c, err := s.getSmart(ctx, key.TMDB())
		if err != nil {
			return nil, err
		}
		if c.OwnerUserID != 0 && c.OwnerUserID != userID(user) {
			return nil, core.ErrNotFound
		}
		if page, err := s.Items(ctx, user, key, 0, 1); err == nil {
			c.ChildCount = page.Total
		}
		return c, nil
	default:
		return nil, core.ErrNotFound
	}
}

func mustFranchises(ctx context.Context, s *Service) []core.Collection {
	fr, _ := s.listFranchises(ctx)
	return fr
}

// Items returns a page of a collection's titles with per-user rating limits applied.
func (s *Service) Items(ctx context.Context, user *core.User, key core.RatingKey, offset, limit int) (core.Page, error) {
	if limit <= 0 {
		limit = 50
	}
	switch key.Kind() {
	case core.KindCollection:
		_, items, err := s.cat.Collection(ctx, key.TMDB())
		if err != nil {
			return core.Page{}, err
		}
		items = filterByUserRating(items, user)
		sort.Slice(items, func(i, j int) bool { return items[i].Released.Before(items[j].Released) })
		return pageOf(items, offset, limit), nil
	case core.KindSmart:
		if b, ok := builtinByKey(key); ok {
			// Fetch enough to cover the page (plus headroom for rating filtering).
			want := offset + limit
			if maxRating(user) != "" {
				want = want*2 + 10
			}
			items, err := s.cat.List(ctx, b.list, b.typ, want)
			if err != nil {
				return core.Page{}, err
			}
			items = filterByUserRating(items, user)
			return pageOf(items, offset, limit), nil
		}
		c, err := s.getSmart(ctx, key.TMDB())
		if err != nil {
			return core.Page{}, err
		}
		if c.OwnerUserID != 0 && c.OwnerUserID != userID(user) {
			return core.Page{}, core.ErrNotFound
		}
		f := core.Filter{}
		if c.Filter != nil {
			f = *c.Filter
		}
		if f.Type == "" {
			f.Type = c.Type
		}
		f.MaxContentRating = stricterRating(f.MaxContentRating, maxRating(user))
		sortOrder := c.Sort
		if sortOrder == "" {
			sortOrder = core.SortPopularity
		}
		desc := sortOrder != core.SortTitle // popularity/rating/released/added read high-first
		return s.cat.Browse(ctx, core.BrowseQuery{Filter: f, Sort: sortOrder, Desc: desc, Offset: offset, Limit: limit})
	default:
		return core.Page{}, core.ErrNotFound
	}
}

// CreateSmart persists a smart collection. OwnerUserID 0 = server-wide.
func (s *Service) CreateSmart(ctx context.Context, c core.Collection) (*core.Collection, error) {
	return s.createSmart(ctx, c)
}

func (s *Service) createSmart(ctx context.Context, c core.Collection) (*core.Collection, error) {
	if strings.TrimSpace(c.Title) == "" {
		return nil, fmt.Errorf("collections: smart collection needs a title")
	}
	if c.Filter == nil {
		return nil, fmt.Errorf("collections: smart collection needs a filter")
	}
	fj, err := json.Marshal(c.Filter)
	if err != nil {
		return nil, err
	}
	sortOrder := c.Sort
	if sortOrder == "" {
		sortOrder = core.SortPopularity
	}
	typ := c.Type
	if typ == "" {
		typ = c.Filter.Type
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO collections_smart(title,summary,type,filter_json,sort,owner_user_id,poster,backdrop,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		c.Title, c.Summary, string(typ), string(fj), string(sortOrder), c.OwnerUserID, c.PosterPath, c.BackdropPath, s.now().Unix())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	out := c
	out.Key = core.SmartKey(id)
	out.Kind = "smart"
	out.Type = typ
	out.Sort = sortOrder
	// Copy filter so callers can't mutate stored state via the pointer.
	f := *c.Filter
	out.Filter = &f
	return &out, nil
}

// DeleteSmart removes a persisted smart collection. Builtins/franchises cannot be deleted.
func (s *Service) DeleteSmart(ctx context.Context, key core.RatingKey) error {
	if key.Kind() != core.KindSmart {
		return core.ErrNotFound
	}
	if _, ok := builtinByKey(key); ok {
		return core.ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM collections_smart WHERE id=?`, key.TMDB())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}
