package catalog

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/tmdb"
)

// SyncProgress describes a running or last-finished catalogue sync.
type SyncProgress struct {
	Running     bool      `json:"running"`
	Phase       string    `json:"phase,omitempty"` // e.g. "movie/popularity", "tv/votes/1994"
	PagesDone   int       `json:"pagesDone"`
	TitlesSeen  int       `json:"titlesSeen"`
	TitlesAdded int       `json:"titlesAdded"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}

// Stats is the admin-UI snapshot of the mirror.
type Stats struct {
	Movies          int          `json:"movies"`
	Shows           int          `json:"shows"`
	Seasons         int          `json:"seasons"`
	Episodes        int          `json:"episodes"`
	BrowsableMovies int          `json:"browsableMovies"`
	BrowsableShows  int          `json:"browsableShows"`
	Collections     int          `json:"collections"`
	LastSync        time.Time    `json:"lastSync,omitempty"`
	Sync            SyncProgress `json:"sync"`
}

// Stats returns mirror counts, last sync time and current sync progress.
func (c *Catalog) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	count := func(q string, dst *int, args ...any) error {
		return c.db.QueryRowContext(ctx, q, args...).Scan(dst)
	}
	if err := count(`SELECT COUNT(*) FROM catalog_titles WHERE type='movie'`, &s.Movies); err != nil {
		return s, err
	}
	if err := count(`SELECT COUNT(*) FROM catalog_titles WHERE type='show'`, &s.Shows); err != nil {
		return s, err
	}
	if err := count(`SELECT COUNT(*) FROM catalog_titles WHERE type='season'`, &s.Seasons); err != nil {
		return s, err
	}
	if err := count(`SELECT COUNT(*) FROM catalog_titles WHERE type='episode'`, &s.Episodes); err != nil {
		return s, err
	}
	if err := count(`SELECT COUNT(*) FROM catalog_titles WHERE type='movie' AND browsable=1`, &s.BrowsableMovies); err != nil {
		return s, err
	}
	if err := count(`SELECT COUNT(*) FROM catalog_titles WHERE type='show' AND browsable=1`, &s.BrowsableShows); err != nil {
		return s, err
	}
	if err := count(`SELECT COUNT(*) FROM catalog_collections`, &s.Collections); err != nil {
		return s, err
	}
	if v := c.getMeta(ctx, "last_sync"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.LastSync = time.Unix(n, 0).UTC()
		}
	}
	c.progressMu.RLock()
	s.Sync = c.progress
	c.progressMu.RUnlock()
	s.Sync.Running = c.syncing.Load()
	return s, nil
}

func (c *Catalog) setProgress(fn func(*SyncProgress)) {
	c.progressMu.Lock()
	fn(&c.progress)
	c.progressMu.Unlock()
}

// SyncProgress returns the current sync progress snapshot.
func (c *Catalog) SyncProgress() SyncProgress {
	c.progressMu.RLock()
	defer c.progressMu.RUnlock()
	p := c.progress
	p.Running = c.syncing.Load()
	return p
}

// Run performs an initial sync and then refreshes on RefreshInterval until ctx ends.
// Intended to be run in its own goroutine by the app wiring.
func (c *Catalog) Run(ctx context.Context) {
	if err := c.Sync(ctx); err != nil && ctx.Err() == nil {
		c.log.Error("catalog: initial sync", "err", err)
	}
	for {
		interval := c.config().RefreshInterval.D()
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if err := c.Sync(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("catalog: refresh sync", "err", err)
		}
	}
}

// Sync (re)builds the browsable set from TMDB discover. A second call while
// one is running returns ErrSyncInProgress; use SyncProgress to poll.
var ErrSyncInProgress = fmt.Errorf("catalog: sync already in progress")

func (c *Catalog) Sync(ctx context.Context) error {
	if c.client == nil {
		return fmt.Errorf("catalog: sync: no TMDB client")
	}
	if !c.syncing.CompareAndSwap(false, true) {
		return ErrSyncInProgress
	}
	defer c.syncing.Store(false)
	c.setProgress(func(p *SyncProgress) {
		*p = SyncProgress{Running: true, StartedAt: c.now()}
	})
	err := c.syncLocked(ctx)
	c.setProgress(func(p *SyncProgress) {
		p.Running = false
		p.FinishedAt = c.now()
		p.Phase = ""
		if err != nil {
			p.LastError = err.Error()
		}
	})
	if err == nil {
		_ = c.setMeta(ctx, "last_sync", strconv.FormatInt(c.now().Unix(), 10))
	}
	return err
}

func (c *Catalog) syncLocked(ctx context.Context) error {
	for _, tv := range []bool{false, true} {
		// Pass 1: popularity-sorted; stop once popularity drops below threshold
		// AND the item also fails the vote bar (sorted desc, so all later fail pop).
		if err := c.syncPass(ctx, tv, "popularity"); err != nil {
			return err
		}
		// Pass 2: vote-count with server-side vote_count.gte catches long-tail
		// classics whose current popularity is low.
		if c.config().MinVotes > 0 {
			if err := c.syncPass(ctx, tv, "votes"); err != nil {
				return err
			}
		}
	}
	// Demote browsable titles that no longer meet the (possibly raised) threshold?
	// Deliberately not done: searched titles joined on demand and owners raising
	// the bar expect new syncs to stop adding, not to hide known titles. The
	// browsable flag is only ever added by sync/search.
	return nil
}

func (c *Catalog) syncPass(ctx context.Context, tv bool, pass string) error {
	cfg := c.config()
	typ := "movie"
	sortBy := "popularity.desc"
	if tv {
		typ = "tv"
	}
	params := tmdb.DiscoverParams{SortBy: sortBy, IncludeAdult: cfg.IncludeAdult}
	if pass == "votes" {
		params.SortBy = "vote_count.desc"
		params.VoteCountGTE = cfg.MinVotes
	}
	// Probe page 1 unsliced; if TMDB's 500-page cap would bite, slice by year.
	totalPages, err := c.syncSlice(ctx, tv, params, pass, 0, true)
	if err != nil {
		return err
	}
	c.setProgress(func(p *SyncProgress) { p.Phase = typ + "/" + pass })
	// The probe only read total_pages (the real fetch hits the TMDB cache).
	if totalPages <= 500 {
		_, err := c.syncSlice(ctx, tv, params, pass, 0, false)
		return err
	}
	from, to := c.syncYears[0], c.syncYears[1]
	if from <= 0 {
		from = 1900
	}
	if to <= 0 {
		to = c.now().Year() + 2
	}
	for year := to; year >= from; year-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := params
		p.Year = year
		c.setProgress(func(pr *SyncProgress) { pr.Phase = fmt.Sprintf("%s/%s/%d", typ, pass, year) })
		if _, err := c.syncSlice(ctx, tv, p, pass, year, false); err != nil {
			return err
		}
	}
	return nil
}

// syncSlice pages one discover slice. In probe mode it fetches only page 1 and
// returns total_pages without storing anything.
func (c *Catalog) syncSlice(ctx context.Context, tv bool, params tmdb.DiscoverParams, pass string, year int, probe bool) (int, error) {
	cfg := c.config()
	for page := 1; page <= 500; page++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		p := params
		p.Page = page
		var titles []core.Title
		totalPages := 0
		if tv {
			res, err := c.client.DiscoverTV(ctx, p)
			if err != nil {
				return 0, err
			}
			totalPages = res.TotalPages
			if probe {
				return totalPages, nil
			}
			for _, s := range res.Results {
				titles = append(titles, c.tvToTitle(ctx, s))
			}
		} else {
			res, err := c.client.DiscoverMovies(ctx, p)
			if err != nil {
				return 0, err
			}
			totalPages = res.TotalPages
			if probe {
				return totalPages, nil
			}
			for _, m := range res.Results {
				titles = append(titles, c.movieToTitle(ctx, m))
			}
		}
		added := 0
		stop := false
		for _, t := range titles {
			if pass == "popularity" && cfg.MinPopularity > 0 && t.Popularity < cfg.MinPopularity && (cfg.MinVotes <= 0 || t.VoteCount < cfg.MinVotes) {
				// Popularity-sorted desc: nothing later on this slice qualifies on pop,
				// but vote-qualifiers are still possible deeper. Only stop early when
				// there is no vote pass (MinVotes unset) — otherwise finish the slice
				// cheaply is wrong for huge slices, so stop and let the votes pass catch them.
				stop = true
			}
			if c.meetsThreshold(t) {
				if err := c.upsert(ctx, t, upsertOpts{browsable: true}); err != nil {
					return totalPages, err
				}
				added++
			}
		}
		c.setProgress(func(pr *SyncProgress) {
			pr.PagesDone++
			pr.TitlesSeen += len(titles)
			pr.TitlesAdded += added
		})
		if stop || page >= totalPages || len(titles) == 0 {
			return totalPages, nil
		}
	}
	return 500, nil
}
