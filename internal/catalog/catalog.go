// Package catalog implements core.Catalog: a SQLite mirror of a filtered
// TMDB slice plus on-demand fetches for anything else.
package catalog

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

func init() {
	store.Register("catalog", 1, `
CREATE TABLE catalog_titles (
  key INTEGER PRIMARY KEY,
  type TEXT NOT NULL,
  tmdb_id INTEGER NOT NULL DEFAULT 0,
  imdb TEXT NOT NULL DEFAULT '',
  tvdb INTEGER NOT NULL DEFAULT 0,
  show_key INTEGER NOT NULL DEFAULT 0,
  show_tmdb INTEGER NOT NULL DEFAULT 0,
  show_title TEXT NOT NULL DEFAULT '',
  season_title TEXT NOT NULL DEFAULT '',
  season INTEGER NOT NULL DEFAULT 0,
  episode INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  original_title TEXT NOT NULL DEFAULT '',
  sort_title TEXT NOT NULL DEFAULT '',
  tagline TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  year INTEGER NOT NULL DEFAULT 0,
  released_ts INTEGER NOT NULL DEFAULT 0,
  runtime_sec INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT '',
  language TEXT NOT NULL DEFAULT '',
  content_rating TEXT NOT NULL DEFAULT '',
  content_rating_rank INTEGER NOT NULL DEFAULT 0,
  rating REAL NOT NULL DEFAULT 0,
  vote_count INTEGER NOT NULL DEFAULT 0,
  popularity REAL NOT NULL DEFAULT 0,
  poster TEXT NOT NULL DEFAULT '',
  backdrop TEXT NOT NULL DEFAULT '',
  still TEXT NOT NULL DEFAULT '',
  logo TEXT NOT NULL DEFAULT '',
  collection_id INTEGER NOT NULL DEFAULT 0,
  collection_name TEXT NOT NULL DEFAULT '',
  child_count INTEGER NOT NULL DEFAULT 0,
  leaf_count INTEGER NOT NULL DEFAULT 0,
  browsable INTEGER NOT NULL DEFAULT 0,
  added_at INTEGER NOT NULL DEFAULT 0,
  fetched_at INTEGER NOT NULL DEFAULT 0,
  detail INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX catalog_titles_type_browse_pop ON catalog_titles(type, browsable, popularity DESC);
CREATE INDEX catalog_titles_type_browse_rating ON catalog_titles(type, browsable, rating DESC);
CREATE INDEX catalog_titles_type_browse_released ON catalog_titles(type, browsable, released_ts DESC);
CREATE INDEX catalog_titles_type_browse_added ON catalog_titles(type, browsable, added_at DESC);
CREATE INDEX catalog_titles_sort ON catalog_titles(sort_title);
CREATE INDEX catalog_titles_tmdb ON catalog_titles(tmdb_id, type);
CREATE INDEX catalog_titles_imdb ON catalog_titles(imdb);
CREATE INDEX catalog_titles_tvdb ON catalog_titles(tvdb);
CREATE INDEX catalog_titles_show ON catalog_titles(show_key, season, episode);
CREATE INDEX catalog_titles_collection ON catalog_titles(collection_id);
CREATE INDEX catalog_titles_year ON catalog_titles(year);
CREATE TABLE catalog_genres (key INTEGER NOT NULL, genre TEXT NOT NULL, PRIMARY KEY(key, genre));
CREATE INDEX catalog_genres_genre ON catalog_genres(genre, key);
CREATE TABLE catalog_studios (key INTEGER NOT NULL, studio TEXT NOT NULL, PRIMARY KEY(key, studio));
CREATE INDEX catalog_studios_studio ON catalog_studios(studio, key);
CREATE TABLE catalog_countries (key INTEGER NOT NULL, country TEXT NOT NULL, PRIMARY KEY(key, country));
CREATE INDEX catalog_countries_country ON catalog_countries(country, key);
CREATE TABLE catalog_credits (
  key INTEGER NOT NULL, person_tmdb INTEGER NOT NULL, name TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'cast', ord INTEGER NOT NULL DEFAULT 0,
  thumb TEXT NOT NULL DEFAULT '', PRIMARY KEY(key, person_tmdb, kind, role));
CREATE INDEX catalog_credits_person ON catalog_credits(person_tmdb, key);
CREATE TABLE catalog_collections (
  tmdb_id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '',
  poster TEXT NOT NULL DEFAULT '', backdrop TEXT NOT NULL DEFAULT '', fetched_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE catalog_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL DEFAULT '');
`)
	store.Register("catalog", 2, `
CREATE TABLE catalog_keywords (key INTEGER NOT NULL, keyword TEXT NOT NULL, PRIMARY KEY(key, keyword));
CREATE INDEX catalog_keywords_keyword ON catalog_keywords(keyword, key);
`)
	store.Register("catalog", 3, `ALTER TABLE catalog_titles ADD COLUMN adult INTEGER NOT NULL DEFAULT 0;`)
}

// TMDB is the subset of the TMDB client the catalogue needs.
// *tmdb.Client satisfies it.
type TMDB interface {
	MovieDetails(ctx context.Context, id int64) (*tmdb.MovieDetails, error)
	TVDetails(ctx context.Context, id int64) (*tmdb.TVDetails, error)
	SeasonDetails(ctx context.Context, tvID int64, season int) (*tmdb.SeasonDetails, error)
	EpisodeExternalIDs(ctx context.Context, tvID int64, season, episode int) (*tmdb.ExternalIDs, error)
	CollectionDetails(ctx context.Context, id int64) (*tmdb.CollectionDetails, error)
	DiscoverMovies(ctx context.Context, p tmdb.DiscoverParams) (*tmdb.Paged[tmdb.Movie], error)
	DiscoverTV(ctx context.Context, p tmdb.DiscoverParams) (*tmdb.Paged[tmdb.TVShow], error)
	SearchMovies(ctx context.Context, q string, page int) (*tmdb.Paged[tmdb.Movie], error)
	SearchTV(ctx context.Context, q string, page int) (*tmdb.Paged[tmdb.TVShow], error)
	SearchMulti(ctx context.Context, q string, page int) (*tmdb.Paged[tmdb.MultiResult], error)
	TrendingMovies(ctx context.Context, window string, page int) (*tmdb.Paged[tmdb.Movie], error)
	TrendingTV(ctx context.Context, window string, page int) (*tmdb.Paged[tmdb.TVShow], error)
	TrendingAll(ctx context.Context, window string, page int) (*tmdb.Paged[tmdb.MultiResult], error)
	MovieList(ctx context.Context, list string, page int) (*tmdb.Paged[tmdb.Movie], error)
	TVList(ctx context.Context, list string, page int) (*tmdb.Paged[tmdb.TVShow], error)
	MovieSimilar(ctx context.Context, id int64, page int) (*tmdb.Paged[tmdb.Movie], error)
	MovieRecommendations(ctx context.Context, id int64, page int) (*tmdb.Paged[tmdb.Movie], error)
	TVSimilar(ctx context.Context, id int64, page int) (*tmdb.Paged[tmdb.TVShow], error)
	TVRecommendations(ctx context.Context, id int64, page int) (*tmdb.Paged[tmdb.TVShow], error)
	FindByExternalID(ctx context.Context, source, id string) (*tmdb.FindResult, error)
	Genres(ctx context.Context, mediaType string) ([]tmdb.Genre, error)
}

var _ TMDB = (*tmdb.Client)(nil)
var _ core.Catalog = (*Catalog)(nil)

// Catalog implements core.Catalog.
type Catalog struct {
	db     *sql.DB
	client TMDB
	cfgMu  sync.RWMutex
	cfg    config.Catalog
	log    *slog.Logger
	now    func() time.Time

	staleAfter time.Duration
	listTTL    time.Duration
	crowdTTL   time.Duration // co-engagement lists (crowd.go)

	genreMu sync.Mutex
	genres  map[string]map[int]string // mediaType -> id -> name

	listMu    sync.Mutex
	listCache map[string]listEntry

	syncMu     sync.Mutex
	syncing    atomic.Bool
	progress   SyncProgress
	progressMu sync.RWMutex
	syncYears  [2]int // 0,0 = 1900..now+2
}

type listEntry struct {
	items   []core.Title
	expires time.Time
}

// Option customises a Catalog.
type Option func(*Catalog)

func WithNow(fn func() time.Time) Option { return func(c *Catalog) { c.now = fn } }
func WithStaleAfter(d time.Duration) Option {
	return func(c *Catalog) { c.staleAfter = d }
}
func WithListTTL(d time.Duration) Option { return func(c *Catalog) { c.listTTL = d } }

// WithSyncYears bounds the year slices used when discover exceeds 500 pages.
func WithSyncYears(from, to int) Option { return func(c *Catalog) { c.syncYears = [2]int{from, to} } }

// New creates a catalogue. client may be nil (mirror-only / offline mode).
func New(db *sql.DB, client TMDB, cfg config.Catalog, log *slog.Logger, opts ...Option) (*Catalog, error) {
	if db == nil {
		return nil, errNilDB
	}
	if log == nil {
		log = slog.Default()
	}
	stale := cfg.RefreshInterval.D()
	if stale <= 0 {
		stale = 24 * time.Hour
	}
	c := &Catalog{db: db, client: client, cfg: cfg, log: log, now: time.Now,
		staleAfter: stale, listTTL: 15 * time.Minute,
		genres: map[string]map[int]string{}, listCache: map[string]listEntry{}}
	for _, fn := range opts {
		fn(c)
	}
	return c, nil
}

var errNilDB = &simpleError{"catalog: nil *sql.DB"}

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }

// UpdateConfig swaps the catalogue config (wire to config.Store.Subscribe).
func (c *Catalog) UpdateConfig(cfg config.Catalog) {
	c.cfgMu.Lock()
	c.cfg = cfg
	c.cfgMu.Unlock()
}

func (c *Catalog) config() config.Catalog {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

// MeetsThreshold reports whether a title qualifies for the browsable set under
// cfg: popularity >= MinPopularity OR votes >= MinVotes, and released.
func MeetsThreshold(cfg config.Catalog, t core.Title, now time.Time) bool {
	if t.Type != core.TypeMovie && t.Type != core.TypeShow {
		return false
	}
	if !t.IsReleased(now) {
		// A title with no date is treated as unreleased.
		return false
	}
	if cfg.MinPopularity <= 0 && cfg.MinVotes <= 0 {
		return true
	}
	popOK := cfg.MinPopularity > 0 && t.Popularity >= cfg.MinPopularity
	voteOK := cfg.MinVotes > 0 && t.VoteCount >= cfg.MinVotes
	if cfg.MinPopularity > 0 && cfg.MinVotes > 0 {
		return popOK || voteOK
	}
	if cfg.MinPopularity > 0 {
		return popOK
	}
	return voteOK
}

func (c *Catalog) meetsThreshold(t core.Title) bool {
	return MeetsThreshold(c.config(), t, c.now())
}
