package newznab

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

type recentFeedMode struct {
	identity   string
	unfiltered bool
	checked    time.Time
}

func recentCategories(cfg config.Indexer) []int {
	movies, tv := cfg.MovieCategories, cfg.TVCategories
	if len(movies) == 0 {
		movies = []int{2000}
	}
	if len(tv) == 0 {
		tv = []int{5000}
	}
	return append(append([]int{}, movies...), tv...)
}

// Some Newznab implementations return an empty success for comma-separated
// categories even though their single-category and unrestricted feeds work.
// On an empty first page, probe once without cat (one ordinary budgeted hit).
// Remember a working unrestricted mode, and filter categories at ingestion;
// independent movie/TV offsets must never be merged into a fabricated cursor.
func (c *Client) recent(ctx context.Context, offset, limit int) ([]core.Release, error) {
	c.feedMu.Lock()
	defer c.feedMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := c.Config()
	identity := searchIdentity(cfg)
	if c.feedMode.identity != identity {
		mode := recentFeedMode{identity: identity}
		if c.db != nil {
			var unfiltered int
			var checked int64
			err := c.db.QueryRowContext(ctx, `SELECT unfiltered,checked_at FROM newznab_feed_modes WHERE indexer_id=? AND identity=?`, c.indexerID(), identity).Scan(&unfiltered, &checked)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				mode.unfiltered = unfiltered != 0
				mode.checked = time.Unix(checked, 0)
			}
		}
		c.feedMode = mode
	}
	q := url.Values{"t": {"search"}, "extended": {"1"}, "offset": {strconv.Itoa(max(0, offset))}, "limit": {strconv.Itoa(min(100, max(1, limit)))}}
	if !c.feedMode.unfiltered {
		q.Set("cat", joinCats(recentCategories(cfg)))
	}
	rs, err := c.doSearch(ctx, q)
	if err != nil || len(rs) > 0 || offset != 0 || c.feedMode.unfiltered || c.now().Sub(c.feedMode.checked) < 6*time.Hour {
		return rs, err
	}
	q.Del("cat")
	rs, err = c.doSearch(ctx, q)
	if err != nil {
		return nil, err
	} // don't call a denied/failed probe an empty feed
	mode := recentFeedMode{identity: identity, unfiltered: len(rs) > 0, checked: c.now()}
	if c.db != nil {
		flag := 0
		if mode.unfiltered {
			flag = 1
		}
		_, err = c.db.ExecContext(ctx, `INSERT INTO newznab_feed_modes(indexer_id,identity,unfiltered,checked_at) VALUES(?,?,?,?) ON CONFLICT(indexer_id) DO UPDATE SET identity=excluded.identity,unfiltered=excluded.unfiltered,checked_at=excluded.checked_at`, c.indexerID(), identity, flag, mode.checked.Unix())
		if err != nil {
			return nil, err
		}
	}
	c.feedMode = mode
	return rs, nil
}
