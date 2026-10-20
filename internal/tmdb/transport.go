package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// rateLimiter is a token bucket whose rate can change at run time (a
// cluster divides TMDB's per-key budget among live nodes). Callers reserve
// a token and sleep until it is due, so concurrent waiters are served in
// order without polling.
type rateLimiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64 // may go negative: tokens reserved by waiters
	last   time.Time
	stop   chan struct{}
}

func newRateLimiter(perSec float64, burst int) *rateLimiter {
	perSec, b := limiterParams(perSec, burst)
	return &rateLimiter{rate: perSec, burst: b, tokens: b, last: time.Now(), stop: make(chan struct{})}
}

func limiterParams(perSec float64, burst int) (float64, float64) {
	if burst <= 0 {
		burst = 1
	}
	if perSec <= 0 {
		perSec = 40
	}
	return perSec, float64(burst)
}

// refillLocked adds the tokens earned since the last refill.
func (r *rateLimiter) refillLocked(now time.Time) {
	if el := now.Sub(r.last).Seconds(); el > 0 {
		r.tokens = min(r.burst, r.tokens+el*r.rate)
	}
	r.last = now
}

// setRate changes the rate and burst; tokens earned so far are kept (up
// to the new burst).
func (r *rateLimiter) setRate(perSec float64, burst int) {
	perSec, b := limiterParams(perSec, burst)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refillLocked(time.Now())
	r.rate, r.burst = perSec, b
	r.tokens = min(r.tokens, b)
}

func (r *rateLimiter) current() (float64, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rate, int(r.burst)
}

func (r *rateLimiter) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	r.refillLocked(time.Now())
	r.tokens--
	if r.tokens >= 0 {
		r.mu.Unlock()
		return nil
	}
	delay := time.Duration(-r.tokens / r.rate * float64(time.Second))
	r.mu.Unlock()
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		r.mu.Lock()
		r.tokens++ // give the reservation back
		r.mu.Unlock()
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Close releases the client (the rate limiter has no goroutine; Close is
// kept for callers that own the client).
func (c *Client) Close() {
	if c.lim != nil {
		select {
		case <-c.lim.stop:
		default:
			close(c.lim.stop)
		}
	}
}

func (c *Client) buildURL(path string, q url.Values, cfg clientSettings) string {
	// Work on a copy: the caller's values may be reused (a coalesced
	// waiter retries with them) and must never accumulate the api_key.
	if q == nil {
		q = url.Values{}
	} else {
		q = cloneValues(q)
	}
	if q.Get("language") == "" && cfg.language != "" {
		q.Set("language", cfg.language)
	}
	if !cfg.bearer && cfg.key != "" {
		q.Set("api_key", cfg.key)
	}
	u := c.opts.baseURL + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// getJSON performs a cached, coalesced GET and decodes into out.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	body, err := c.get(ctx, path, q)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("tmdb: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	cfg := c.settings()
	if cfg.key == "" {
		return nil, ErrNoKey
	}
	full := c.buildURL(path, q, cfg)
	cacheKey := strconv.FormatUint(cfg.generation, 10) + ":" + full
	// Cache hit (fresh).
	c.mu.Lock()
	if e, ok := c.cache.get(cacheKey); ok && time.Now().Before(e.expires) {
		body := append([]byte(nil), e.body...)
		c.mu.Unlock()
		return body, nil
	}
	if f, ok := c.flights[cacheKey]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.done:
			if f.err != nil {
				// The leader's own context ended its request; that says
				// nothing about this caller, which retries with its own.
				if f.abandoned && ctx.Err() == nil {
					return c.get(ctx, path, q)
				}
				return nil, f.err
			}
			return append([]byte(nil), f.body...), nil
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[cacheKey] = f
	// Keep the stale body with its ETag: the entry may be evicted while the
	// revalidation is in flight, and a 304 must still have a body to serve.
	// Cached bodies are never mutated, so sharing the slice is safe.
	var etag string
	var staleBody []byte
	if e, ok := c.cache.get(cacheKey); ok {
		etag, staleBody = e.etag, e.body
	}
	c.mu.Unlock()

	body, newETag, notModified, err := c.doWithRetry(ctx, full, etag, cfg)

	c.mu.Lock()
	delete(c.flights, cacheKey)
	if err == nil && notModified {
		body = staleBody
	}
	if err == nil && c.generation == cfg.generation {
		if notModified {
			c.cache.put(cacheKey, cacheEntry{body: staleBody, etag: etag, expires: time.Now().Add(c.opts.cacheTTL)}, time.Now())
		} else {
			c.cache.put(cacheKey, cacheEntry{body: append([]byte(nil), body...), etag: newETag, expires: time.Now().Add(c.opts.cacheTTL)}, time.Now())
		}
	}
	f.body, f.err = body, err
	f.abandoned = err != nil && ctx.Err() != nil
	close(f.done)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), body...), nil
}

func (c *Client) doWithRetry(ctx context.Context, full, etag string, cfg clientSettings) (body []byte, newETag string, notModified bool, err error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt <= c.opts.maxRetries; attempt++ {
		if err := c.lim.wait(ctx); err != nil {
			return nil, "", false, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, "", false, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.opts.userAgent)
		if cfg.bearer {
			req.Header.Set("Authorization", "Bearer "+cfg.key)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := c.opts.httpClient.Do(req)
		if err != nil {
			lastErr = redactURLError(err)
			if ctx.Err() != nil {
				return nil, "", false, ctx.Err()
			}
		} else {
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
			resp.Body.Close()
			if rerr != nil {
				lastErr = rerr
			} else if resp.StatusCode == http.StatusNotModified {
				return nil, etag, true, nil
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Validate before caching: a gateway's malformed 200 response
				// must not keep failing metadata reads for the entire cache TTL.
				if len(b) > 32<<20 || !json.Valid(b) {
					return nil, "", false, errors.New("tmdb: invalid or oversized JSON response")
				}
				return b, resp.Header.Get("ETag"), false, nil
			} else {
				he := parseHTTPError(resp.StatusCode, b)
				if resp.StatusCode == http.StatusNotFound {
					return nil, "", false, fmt.Errorf("%w: %v", core.ErrNotFound, he)
				}
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
					lastErr = he
					wait := backoff
					if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
						wait = ra
					}
					if attempt < c.opts.maxRetries {
						if err := sleepCtx(ctx, wait); err != nil {
							return nil, "", false, err
						}
						backoff *= 2
						continue
					}
					return nil, "", false, he
				}
				return nil, "", false, he
			}
		}
		if attempt < c.opts.maxRetries {
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, "", false, err
			}
			backoff *= 2
			continue
		}
	}
	if lastErr != nil {
		return nil, "", false, lastErr
	}
	return nil, "", false, fmt.Errorf("tmdb: request failed")
}

// redactURLError strips the query (which carries a v3 api_key) from the
// URL net/http embeds in transport errors, so the key never reaches logs or
// API responses.
func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	if u, perr := url.Parse(ue.URL); perr == nil {
		if q := u.Query(); q.Has("api_key") {
			q.Set("api_key", "REDACTED")
			u.RawQuery = q.Encode()
		}
		return &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
	}
	return &url.Error{Op: ue.Op, URL: "tmdb", Err: ue.Err}
}

func parseHTTPError(code int, b []byte) *HTTPError {
	var payload struct {
		StatusMessage string `json:"status_message"`
	}
	_ = json.Unmarshal(b, &payload)
	body := strings.TrimSpace(string(b))
	if len(body) > 512 {
		body = body[:512]
	}
	return &HTTPError{StatusCode: code, StatusMsg: payload.StatusMessage, Body: body}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func values(pairs ...string) url.Values {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	return q
}
func pageQ(q url.Values, page int) url.Values {
	if q == nil {
		q = url.Values{}
	}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	return q
}
