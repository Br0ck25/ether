package newznab

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("newznab", 1, `
CREATE TABLE newznab_budget (
  indexer_id TEXT NOT NULL,
  day TEXT NOT NULL,
  hits INTEGER NOT NULL DEFAULT 0,
  grabs INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (indexer_id, day)
);
CREATE TABLE newznab_cache (
  cache_key TEXT PRIMARY KEY,
  indexer_id TEXT NOT NULL,
  results TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX newznab_cache_expires ON newznab_cache(expires_at);
CREATE TABLE newznab_caps (
  indexer_id TEXT PRIMARY KEY,
  caps TEXT NOT NULL,
  fetched_at INTEGER NOT NULL
);`)
}

// Remaining reports one indexer's budget state for today (UTC).
// A remaining value of -1 means unlimited.
type Remaining struct {
	Indexer        string `json:"indexer"`
	Day            string `json:"day"`
	HitsUsed       int    `json:"hitsUsed"`
	HitsLimit      int    `json:"hitsLimit"`
	HitsRemaining  int    `json:"hitsRemaining"`
	GrabsUsed      int    `json:"grabsUsed"`
	GrabsLimit     int    `json:"grabsLimit"`
	GrabsRemaining int    `json:"grabsRemaining"`
}

func (c *Client) indexerID() string {
	if c.cfg.ID != "" {
		return c.cfg.ID
	}
	return c.Name()
}

func (c *Client) today() string {
	return c.now().UTC().Format("2006-01-02")
}

type memBudget struct {
	day         string
	hits, grabs int
}

// counts returns today's (hits, grabs).
func (c *Client) counts(ctx context.Context) (int, int, error) {
	day := c.today()
	if c.db == nil {
		c.budgetMu.Lock()
		defer c.budgetMu.Unlock()
		if c.mem.day != day {
			return 0, 0, nil
		}
		return c.mem.hits, c.mem.grabs, nil
	}
	var hits, grabs int
	err := c.db.QueryRowContext(ctx,
		`SELECT hits, grabs FROM newznab_budget WHERE indexer_id=? AND day=?`, c.indexerID(), day).
		Scan(&hits, &grabs)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return hits, grabs, err
}

// reserve checks and increments one budget counter ("hit" or "grab").
func (c *Client) reserve(ctx context.Context, kind string) error {
	limit := 0
	if kind == "hit" {
		limit = c.cfg.DailyLimit
	} else {
		limit = c.cfg.DailyGrabs
	}
	day := c.today()
	if c.db == nil {
		c.budgetMu.Lock()
		defer c.budgetMu.Unlock()
		if c.mem.day != day {
			c.mem = memBudget{day: day}
		}
		used := c.mem.hits
		if kind == "grab" {
			used = c.mem.grabs
		}
		if limit > 0 && used >= limit {
			return fmt.Errorf("indexer %q %s budget: %w", c.Name(), kind, core.ErrBudgetExhausted)
		}
		if kind == "hit" {
			c.mem.hits++
		} else {
			c.mem.grabs++
		}
		return nil
	}
	col := "hits"
	if kind == "grab" {
		col = "grabs"
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO newznab_budget(indexer_id, day) VALUES (?,?) ON CONFLICT(indexer_id, day) DO NOTHING`,
		c.indexerID(), day); err != nil {
		return err
	}
	// One conditional increment checks and reserves atomically, so nodes
	// sharing the database can never overspend the budget together.
	q := `UPDATE newznab_budget SET ` + col + `=` + col + `+1 WHERE indexer_id=? AND day=?`
	args := []any{c.indexerID(), day}
	if limit > 0 {
		q += ` AND ` + col + `<?`
		args = append(args, limit)
	}
	res, err := c.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("indexer %q %s budget: %w", c.Name(), kind, core.ErrBudgetExhausted)
	}
	return nil
}

// Remaining returns today's budget state.
func (c *Client) Remaining(ctx context.Context) (Remaining, error) {
	hits, grabs, err := c.counts(ctx)
	r := Remaining{
		Indexer: c.Name(), Day: c.today(),
		HitsUsed: hits, HitsLimit: c.cfg.DailyLimit, HitsRemaining: -1,
		GrabsUsed: grabs, GrabsLimit: c.cfg.DailyGrabs, GrabsRemaining: -1,
	}
	if c.cfg.DailyLimit > 0 {
		r.HitsRemaining = max(0, c.cfg.DailyLimit-hits)
	}
	if c.cfg.DailyGrabs > 0 {
		r.GrabsRemaining = max(0, c.cfg.DailyGrabs-grabs)
	}
	return r, err
}

// Exhausted reports whether either daily budget is spent.
func (c *Client) Exhausted(ctx context.Context) bool {
	r, err := c.Remaining(ctx)
	if err != nil {
		return false
	}
	return r.HitsRemaining == 0 || r.GrabsRemaining == 0
}

// --- result cache ---

type memCacheEntry struct {
	results []core.Release
	expires time.Time
}

func (c *Client) ttl() time.Duration {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cacheTTL
}

func (c *Client) cacheGet(ctx context.Context, key string) ([]core.Release, bool) {
	if c.ttl() <= 0 {
		return nil, false
	}
	now := c.now()
	if c.db == nil {
		c.cacheMu.RLock()
		defer c.cacheMu.RUnlock()
		e, ok := c.memCache[key]
		if !ok || now.After(e.expires) {
			return nil, false
		}
		return append([]core.Release(nil), e.results...), true
	}
	var raw string
	var exp int64
	err := c.db.QueryRowContext(ctx, `SELECT results, expires_at FROM newznab_cache WHERE cache_key=?`, key).Scan(&raw, &exp)
	if err != nil || now.Unix() > exp {
		return nil, false
	}
	var out []core.Release
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil, false
	}
	return out, true
}

func (c *Client) cachePut(ctx context.Context, key string, results []core.Release) {
	ttl := c.ttl()
	if ttl <= 0 {
		return
	}
	exp := c.now().Add(ttl)
	if c.db == nil {
		c.cacheMu.Lock()
		defer c.cacheMu.Unlock()
		if c.memCache == nil {
			c.memCache = map[string]memCacheEntry{}
		}
		c.memCache[key] = memCacheEntry{results: append([]core.Release(nil), results...), expires: exp}
		return
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return
	}
	_, _ = c.db.ExecContext(ctx,
		`INSERT INTO newznab_cache(cache_key, indexer_id, results, expires_at) VALUES (?,?,?,?)
		 ON CONFLICT(cache_key) DO UPDATE SET results=excluded.results, expires_at=excluded.expires_at`,
		key, c.indexerID(), string(raw), exp.Unix())
	_, _ = c.db.ExecContext(ctx, `DELETE FROM newznab_cache WHERE expires_at < ?`, c.now().Unix())
}

// --- caps cache ---

func (c *Client) capsFromDB(ctx context.Context) (*Caps, bool) {
	if c.db == nil {
		return nil, false
	}
	var raw string
	var at int64
	if err := c.db.QueryRowContext(ctx, `SELECT caps, fetched_at FROM newznab_caps WHERE indexer_id=?`, c.indexerID()).Scan(&raw, &at); err != nil {
		return nil, false
	}
	if c.now().Sub(time.Unix(at, 0)) > 24*time.Hour {
		return nil, false
	}
	var caps Caps
	if json.Unmarshal([]byte(raw), &caps) != nil {
		return nil, false
	}
	return &caps, true
}

func (c *Client) capsToDB(ctx context.Context, caps *Caps) {
	if c.db == nil || caps == nil {
		return
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		return
	}
	_, _ = c.db.ExecContext(ctx,
		`INSERT INTO newznab_caps(indexer_id, caps, fetched_at) VALUES (?,?,?)
		 ON CONFLICT(indexer_id) DO UPDATE SET caps=excluded.caps, fetched_at=excluded.fetched_at`,
		c.indexerID(), string(raw), c.now().Unix())
}
