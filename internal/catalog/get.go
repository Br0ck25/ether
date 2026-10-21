package catalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
	// A season none of whose episodes has aired is not listed (seasonsOfShow)
	// and must not open directly either.
	if err == nil && t.Type == core.TypeSeason && !listableSeason(t, c.now()) {
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
			if uerr := c.upsertMany(ctx, titles[1:], upsertOpts{airDates: true}); uerr != nil {
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
	// Mirrored, not aired: a fresh season whose episodes are all still
	// to air must not refetch on every read.
	if st, lerr := c.loadByKey(ctx, key); lerr == nil && c.fresh(st) {
		if ok, qerr := c.seasonMirrored(ctx, showID, season); qerr == nil && ok {
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
	if err := c.upsertMany(ctx, eps, upsertOpts{airDates: true}); err != nil {
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

// seasonsOfShow lists a show's seasons that have aired: their own air date
// has passed and at least one episode has aired (queryTitles drops
// unreleased rows and applies aired counts).
func (c *Catalog) seasonsOfShow(ctx context.Context, showID int64) ([]core.Title, error) {
	// show_key, not show_tmdb: only show_key is indexed, and a show's
	// pages read its seasons and episodes many times.
	all, err := c.queryTitles(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE type='season' AND show_key=? ORDER BY season ASC`, int64(core.ShowKey(showID)))
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for i := range all {
		if listableSeason(&all[i], c.now()) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// episodesOfSeason lists a season's aired episodes; queryTitles drops those
// with unknown or future air dates.
func (c *Catalog) episodesOfSeason(ctx context.Context, showID int64, season int) ([]core.Title, error) {
	return c.queryTitles(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE type='episode' AND show_key=? AND season=? ORDER BY episode ASC`, int64(core.ShowKey(showID)), season)
}

// seasonMirrored reports whether any of a season's episodes, aired or not,
// are in the mirror.
func (c *Catalog) seasonMirrored(ctx context.Context, showID int64, season int) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_titles WHERE type='episode' AND show_key=? AND season=?`, int64(core.ShowKey(showID)), season).Scan(&n)
	return n > 0, err
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
	if len(seasons) == 0 {
		return []core.Title{}, nil
	}
	if err := c.loadSeasons(ctx, key.TMDB(), seasons); err != nil {
		return nil, err
	}
	numbers := make([]any, 0, len(seasons)+1)
	numbers = append(numbers, int64(key))
	for _, s := range seasons {
		numbers = append(numbers, s.Season)
	}
	out, err := c.queryTitles(ctx, `SELECT `+titleCols+` FROM catalog_titles WHERE type='episode' AND show_key=? AND season IN (`+placeholders(len(seasons))+`) ORDER BY season ASC, episode ASC`, numbers...)
	if out == nil && err == nil {
		out = []core.Title{}
	}
	return out, err
}

// seasonLoads bounds a show's concurrent season fetches.
const seasonLoads = 6

// loadSeasons brings a show's listed seasons into the mirror for a whole-show
// read. A season is fetched unless a season fetch has mirrored it: its season
// record is detailed and it has episodes. A mirrored season is refetched when
// stale only if it can still change: it is the latest season, it has episodes
// yet to air, or the show's refreshed summary counts more episodes than the
// mirror holds (a former latest season's final episodes). Otherwise it stays
// as mirrored until its own page or episodes are opened (fetchSeason), so a
// long-running show does not refetch every past season once its details go
// stale. Fetches run concurrently.
func (c *Catalog) loadSeasons(ctx context.Context, showID int64, seasons []core.Title) error {
	type mirrored struct{ episodes, pending int }
	have := map[int]mirrored{}
	rows, err := c.db.QueryContext(ctx, `SELECT season,COUNT(*),COUNT(CASE WHEN released_ts<=0 OR released_ts>? THEN 1 END)
		FROM catalog_titles WHERE type='episode' AND show_key=? GROUP BY season`, c.now().Unix(), int64(core.ShowKey(showID)))
	if err != nil {
		return err
	}
	for rows.Next() {
		var season int
		var m mirrored
		if err := rows.Scan(&season, &m.episodes, &m.pending); err != nil {
			rows.Close()
			return err
		}
		have[season] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	type record struct {
		detailed, fresh bool
		episodes        int // the latest season summary's or fetch's count
	}
	records := map[int]record{}
	rows, err = c.db.QueryContext(ctx, `SELECT season,detail,detail_at,child_count FROM catalog_titles WHERE type='season' AND show_key=?`, int64(core.ShowKey(showID)))
	if err != nil {
		return err
	}
	for rows.Next() {
		var season, detail, child int
		var detailAt int64
		if err := rows.Scan(&season, &detail, &detailAt, &child); err != nil {
			rows.Close()
			return err
		}
		st := storedTitle{detail: detail != 0}
		if detailAt != 0 {
			st.detailAt = time.Unix(detailAt, 0).UTC()
		}
		records[season] = record{detailed: st.detail && detailAt != 0, fresh: c.fresh(&st), episodes: child}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	latest := 0
	for _, s := range seasons {
		latest = max(latest, s.Season)
	}
	var load []int
	for _, s := range seasons {
		m, ok := have[s.Season]
		r := records[s.Season]
		switch {
		case !ok || !r.detailed:
		case r.fresh:
			continue
		case s.Season != latest && m.pending == 0 && r.episodes <= m.episodes:
			continue
		}
		load = append(load, s.Season)
	}
	if len(load) == 0 {
		return nil
	}
	// Each fetch loads the show; make sure it is mirrored once, first.
	if _, err := c.ensureShow(ctx, showID); err != nil {
		return err
	}
	errs := make([]error, len(load))
	sem := make(chan struct{}, seasonLoads)
	var wg sync.WaitGroup
	for i, season := range load {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			_, errs[i] = c.fetchSeason(ctx, showID, season)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
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
