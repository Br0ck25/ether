package newznab

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("newznab", 2, `
CREATE TABLE newznab_rate_limit (
  indexer_id TEXT PRIMARY KEY,
  retry_at INTEGER NOT NULL
);`)
}

type rateLimitState struct {
	mu     sync.Mutex
	loaded bool
	until  time.Time
}

// There is no universal Newznab account-quota discovery endpoint. Let the
// service enforce its actual allowance; after a refusal, honor Retry-After.
// When no reset time is supplied, make one new attempt after a modest pause,
// rather than assuming a daily reset time in the server's timezone.
const defaultRateLimitPause = 15 * time.Minute

// maxRateLimitPause bounds a service-requested pause: daily allowances
// reset within a day, and a bogus Retry-After (a proxy, a misconfigured
// service) must not silently disable an indexer for months.
const maxRateLimitPause = 24 * time.Hour

func retryTime(header string, now time.Time) time.Time {
	at := now.Add(defaultRateLimitPause)
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
		at = now.Add(max(time.Second, time.Duration(seconds)*time.Second))
	} else if t, err := http.ParseTime(header); err == nil && t.After(now) {
		at = t
	}
	if limit := now.Add(maxRateLimitPause); at.After(limit) {
		at = limit
	}
	return at
}

func (c *Client) rateLimitUntil(ctx context.Context) (time.Time, error) {
	c.rate.mu.Lock()
	defer c.rate.mu.Unlock()
	// Without a pause in force, re-read the shared row: another node of a
	// cluster may have been throttled since this one last looked.
	if c.db != nil && (!c.rate.loaded || !c.now().Before(c.rate.until)) {
		var at int64
		err := c.db.QueryRowContext(ctx, `SELECT retry_at FROM newznab_rate_limit WHERE indexer_id=?`, c.indexerID()).Scan(&at)
		if err != nil && err != sql.ErrNoRows {
			return time.Time{}, err
		}
		if err == nil && time.Unix(at, 0).After(c.rate.until) {
			c.rate.until = time.Unix(at, 0)
		}
	}
	c.rate.loaded = true
	return c.rate.until, nil
}

func (c *Client) recordRateLimit(ctx context.Context, retryAfter string, cause error) error {
	until := retryTime(retryAfter, c.now())
	c.rate.mu.Lock()
	defer c.rate.mu.Unlock()
	if c.rate.until.After(until) {
		until = c.rate.until
	}
	c.rate.loaded, c.rate.until = true, until
	if c.db != nil {
		_, err := c.db.ExecContext(ctx, `INSERT INTO newznab_rate_limit(indexer_id, retry_at) VALUES (?,?)
ON CONFLICT(indexer_id) DO UPDATE SET retry_at=CASE WHEN excluded.retry_at > newznab_rate_limit.retry_at THEN excluded.retry_at ELSE newznab_rate_limit.retry_at END`, c.indexerID(), until.Unix())
		if err != nil {
			c.log.Warn("could not persist indexer pause", "indexer", c.Name(), "err", err)
		}
	}
	c.log.Info("indexer paused by service; will retry automatically", "indexer", c.Name(), "retryAt", until.UTC())
	return cause
}
