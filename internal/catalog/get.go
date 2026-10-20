package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

func (c *Catalog) fresh(st *storedTitle) bool {
	// detailAt, not fetchedAt: summary upserts from sync, lists and search
	// refresh fetchedAt and would otherwise keep popular titles' details
	// (seasons, credits, release dates) from ever being refetched.
	return st != nil && st.detail && !st.detailAt.IsZero() && c.now().Sub(st.detailAt) < c.staleAfter
}

// Get returns a title from the mirror, fetching on demand when missing/stale.
func (c *Catalog) Get(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	t, err := c.get(ctx, key)
	if err == nil && (t.Adult || !t.IsReleased(c.now())) {
		return nil, core.ErrNotFound
	}
	return t, err
}

func (c *Catalog) get(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	switch key.Kind() {
	case core.KindMovie:
		return c.getMovie(ctx, key.TMDB())
	case core.KindShow:
		return c.getShow(ctx, key.TMDB())
	case core.KindSeason:
		return c.getSeason(ctx, key)
	case core.KindEpisode:
		return c.getEpisode(ctx, key)
	default:
		return nil, fmt.Errorf("%w: key %s", core.ErrNotFound, key)
	}
}

func (c *Catalog) getMovie(ctx context.Context, id int64) (*core.Title, error) {
	key := core.MovieKey(id)
	st, err := c.loadByKey(ctx, key)
	if err == nil && c.fresh(st) {
		return &st.t, nil
	}
	if c.client != nil {
		d, ferr := c.client.MovieDetails(ctx, id)
		if ferr != nil {
			if err == nil {
				return &st.t, nil // serve stale on fetch failure
			}
			return nil, ferr
		}
		t := c.movieDetailsToTitle(ctx, d)
		if uerr := c.upsert(ctx, t, upsertOpts{browsable: c.meetsThreshold(t), detail: true}); uerr != nil {
			return nil, uerr
		}
		c.harvestMovieCrowd(ctx, key, d.Recommendations)
		st2, lerr := c.loadByKey(ctx, key)
		if lerr != nil {
			return &t, nil
		}
		return &st2.t, nil
	}
	if err != nil {
		return nil, err
	}
	return &st.t, nil
}

func (c *Catalog) getShow(ctx context.Context, id int64) (*core.Title, error) {
	key := core.ShowKey(id)
	st, err := c.loadByKey(ctx, key)
	if err == nil && c.fresh(st) {
		return &st.t, nil
	}
	if c.client != nil {
		d, ferr := c.client.TVDetails(ctx, id)
		if ferr != nil {
			if err == nil {
				return &st.t, nil
			}
			return nil, ferr
		}
		t := c.tvDetailsToTitle(ctx, d)
		titles := []core.Title{t}
		for _, s := range d.Seasons {
			if !episodeKeyFits(s.SeasonNumber, 0) {
				continue // its episodes have no rating keys (year-numbered seasons)
			}
			titles = append(titles, seasonToTitle(t, s))
		}
		// Upsert show as detail; seasons as non-detail summaries in same call would
		// mark them detail too, so split.
		if uerr := c.upsert(ctx, t, upsertOpts{browsable: c.meetsThreshold(t), detail: true}); uerr != nil {
			return nil, uerr
		}
		if len(titles) > 1 {
			if uerr := c.upsertMany(ctx, titles[1:], upsertOpts{}); uerr != nil {
				return nil, uerr
			}
		}
		c.harvestTVCrowd(ctx, key, d.Recommendations)
		st2, lerr := c.loadByKey(ctx, key)
		if lerr != nil {
			return &t, nil
		}
		return &st2.t, nil
	}
	if err != nil {
		return nil, err
	}
	return &st.t, nil
}

// ensureShow returns the show title, fetching if needed.
func (c *Catalog) ensureShow(ctx context.Context, showID int64) (*core.Title, error) {
	return c.getShow(ctx, showID)
}

func (c *Catalog) getSeason(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	_, err := c.fetchSeason(ctx, key.TMDB(), key.Season())
	if err != nil {
		// Fall back to stored season summary.
		if st, lerr := c.loadByKey(ctx, key); lerr == nil {
			return &st.t, nil
		}
		return nil, err
	}
	st, err := c.loadByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	return &st.t, nil
}

// fetchSeason ensures season+episodes are in the mirror, fetching TMDB when
// missing or stale. Returns the season title.
func (c *Catalog) fetchSeason(ctx context.Context, showID int64, season int) (*core.Title, error) {
	key := core.SeasonKey(showID, season)
	show, err := c.ensureShow(ctx, showID)
	if err != nil {
		return nil, err
	}
	// If season row is fresh-detailed and episodes exist, skip network.
	if st, lerr := c.loadByKey(ctx, key); lerr == nil && c.fresh(st) {
		eps, qerr := c.episodesOfSeason(ctx, showID, season)
		if qerr == nil && len(eps) > 0 {
			return &st.t, nil
		}
	}
	if c.client == nil {
		st, lerr := c.loadByKey(ctx, key)
		if lerr != nil {
			return nil, lerr
		}
		return &st.t, nil
	}
	sd, err := c.client.SeasonDetails(ctx, showID, season)
	if err != nil {
		if st, lerr := c.loadByKey(ctx, key); lerr == nil {
			return &st.t, nil
		}
		return nil, err
	}
	seasonName := sd.Name
	if seasonName == "" {
		seasonName = "Season " + itoa(season)
	}
	seasonT := seasonToTitle(*show, tmdbSeasonSummary(sd))
	eps := make([]core.Title, 0, len(sd.Episodes))
	for _, e := range sd.Episodes {
		if !episodeKeyFits(e.SeasonNumber, e.EpisodeNumber) {
			// Its rating key would decode as another title's episode.
			continue
		}
		eps = append(eps, episodeToTitle(*show, seasonName, e))
	}
	seasonT.ChildCount = len(eps)
	if err := c.upsert(ctx, seasonT, upsertOpts{detail: true}); err != nil {
		return nil, err
	}
	// Episodes from a season payload lack their own external ids, so they are
	// not marked detail; Get(episode) enriches and marks them.
	if err := c.upsertMany(ctx, eps, upsertOpts{}); err != nil {
		return nil, err
	}
	return &seasonT, nil
}

// episodeKeyFits reports whether an episode's numbers fit its rating key
// (core.EpisodeKey: season < 1000, episode < 10000).
func episodeKeyFits(season, episode int) bool {
	return season >= 0 && season < 1000 && episode >= 0 && episode < 10_000
}

func tmdbSeasonSummary(sd *tmdb.SeasonDetails) tmdb.SeasonSummary {
	return tmdb.SeasonSummary{ID: sd.ID, Name: sd.Name, Overview: sd.Overview, SeasonNumber: sd.SeasonNumber,
		EpisodeCount: len(sd.Episodes), AirDate: sd.AirDate, PosterPath: sd.PosterPath, VoteAverage: sd.VoteAverage}
}

func (c *Catalog) getEpisode(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	if st, err := c.loadByKey(ctx, key); err == nil && c.fresh(st) {
		return &st.t, nil
	}
	if _, err := c.fetchSeason(ctx, key.TMDB(), key.Season()); err != nil {
		if st, lerr := c.loadByKey(ctx, key); lerr == nil {
			return &st.t, nil
		}
		return nil, err
	}
	st, err := c.loadByKey(ctx, key)
	if err != nil {
		return nil, core.ErrNotFound
	}
	// Best-effort episode external ids (its own IMDb id is useful to indexers).
	if c.client != nil && st.t.IDs.IMDB == "" {
		if ext, ferr := c.client.EpisodeExternalIDs(ctx, key.TMDB(), key.Season(), key.Episode()); ferr == nil && ext != nil && (ext.IMDBID != "" || ext.TVDBID != 0) {
			t := st.t
			t.IDs.IMDB = ext.IMDBID
			if ext.TVDBID != 0 {
				t.IDs.TVDB = ext.TVDBID
			}
			// Nil facets so the upsert keeps the stored rows. Keywords
			// especially: the loaded episode carries its show's (inherited
			// on read), which must not be saved as the episode's own.
			t.Cast, t.Directors, t.Writers = nil, nil, nil
			t.Genres, t.Studios, t.Countries, t.Keywords = nil, nil, nil, nil
			_ = c.upsert(ctx, t, upsertOpts{detail: true})
			st.t.IDs.IMDB = ext.IMDBID
			if ext.TVDBID != 0 {
				st.t.IDs.TVDB = ext.TVDBID
			}
		}
	}
	return &st.t, nil
}

func (c *Catalog) seasonsOfShow(ctx context.Context, showID int64) ([]core.Title, error) {
	return c.queryTitles(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE type='season' AND show_tmdb=? ORDER BY season ASC`, showID)
}
func (c *Catalog) episodesOfSeason(ctx context.Context, showID int64, season int) ([]core.Title, error) {
	return c.queryTitles(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE type='episode' AND show_tmdb=? AND season=? ORDER BY episode ASC`, showID, season)
}

// Children: seasons of a show, episodes of a season.
func (c *Catalog) Children(ctx context.Context, key core.RatingKey) ([]core.Title, error) {
	if _, err := c.Get(ctx, key); err != nil {
		return nil, err
	}
	switch key.Kind() {
	case core.KindShow:
		if _, err := c.ensureShow(ctx, key.TMDB()); err != nil {
			return nil, err
		}
		out, err := c.seasonsOfShow(ctx, key.TMDB())
		if out == nil && err == nil {
			out = []core.Title{}
		}
		return out, err
	case core.KindSeason:
		if _, err := c.fetchSeason(ctx, key.TMDB(), key.Season()); err != nil {
			return nil, err
		}
		out, err := c.episodesOfSeason(ctx, key.TMDB(), key.Season())
		if out == nil && err == nil {
			out = []core.Title{}
		}
		return out, err
	case core.KindMovie, core.KindEpisode:
		return []core.Title{}, nil
	default:
		return nil, fmt.Errorf("%w: key %s", core.ErrNotFound, key)
	}
}

// Grandchildren: all episodes of a show.
func (c *Catalog) Grandchildren(ctx context.Context, key core.RatingKey) ([]core.Title, error) {
	if key.Kind() != core.KindShow {
		if key.Kind() == core.KindSeason || key.Kind() == core.KindMovie || key.Kind() == core.KindEpisode {
			return []core.Title{}, nil
		}
		return nil, fmt.Errorf("%w: key %s", core.ErrNotFound, key)
	}
	seasons, err := c.Children(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []core.Title
	for _, s := range seasons {
		if _, err := c.fetchSeason(ctx, key.TMDB(), s.Season); err != nil {
			return nil, err
		}
		eps, err := c.episodesOfSeason(ctx, key.TMDB(), s.Season)
		if err != nil {
			return nil, err
		}
		out = append(out, eps...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	if out == nil {
		out = []core.Title{}
	}
	return out, nil
}

// NextEpisode returns the next regular episode (specials, season 0, are skipped
// as a target; calling it on a special jumps to S1E1-order sequence).
func (c *Catalog) NextEpisode(ctx context.Context, episode core.RatingKey) (*core.Title, error) {
	if episode.Kind() != core.KindEpisode {
		return nil, core.ErrNotFound
	}
	showID, curSeason, curEp := episode.TMDB(), episode.Season(), episode.Episode()
	if _, err := c.ensureShow(ctx, showID); err != nil {
		return nil, err
	}
	// Same season, higher episode number (only for regular seasons).
	if curSeason > 0 {
		if _, err := c.fetchSeason(ctx, showID, curSeason); err == nil {
			eps, err := c.episodesOfSeason(ctx, showID, curSeason)
			if err != nil {
				return nil, err
			}
			for _, e := range eps {
				if e.Episode > curEp {
					ep := e
					return &ep, nil
				}
			}
		}
	}
	seasons, err := c.seasonsOfShow(ctx, showID)
	if err != nil {
		return nil, err
	}
	for _, s := range seasons {
		if s.Season <= curSeason || s.Season == 0 {
			continue
		}
		if _, err := c.fetchSeason(ctx, showID, s.Season); err != nil {
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			return nil, err
		}
		eps, err := c.episodesOfSeason(ctx, showID, s.Season)
		if err != nil {
			return nil, err
		}
		for _, e := range eps {
			if e.Episode > 0 {
				ep := e
				return &ep, nil
			}
		}
	}
	return nil, core.ErrNotFound
}
