package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// rateLimiter is a token bucket.
type rateLimiter struct {
	tokens chan struct{}
	stop   chan struct{}
}

func newRateLimiter(perSec float64, burst int) *rateLimiter {
	if burst <= 0 {
		burst = 1
	}
	if perSec <= 0 {
		perSec = 40
	}
	r := &rateLimiter{tokens: make(chan struct{}, burst), stop: make(chan struct{})}
	for i := 0; i < burst; i++ {
		r.tokens <- struct{}{}
	}
	interval := time.Duration(float64(time.Second) / perSec)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				select {
				case r.tokens <- struct{}{}:
				default:
				}
			}
		}
	}()
	return r
}

func (r *rateLimiter) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.tokens:
		return nil
	}
}

// Close stops the rate limiter goroutine.
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
	if q == nil {
		q = url.Values{}
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
	if e, ok := c.cache[cacheKey]; ok && time.Now().Before(e.expires) {
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
				return nil, f.err
			}
			return append([]byte(nil), f.body...), nil
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[cacheKey] = f
	var etag string
	if e, ok := c.cache[cacheKey]; ok {
		etag = e.etag
	}
	c.mu.Unlock()

	body, newETag, notModified, err := c.doWithRetry(ctx, full, etag, cfg)

	c.mu.Lock()
	delete(c.flights, cacheKey)
	if err == nil && c.generation == cfg.generation {
		if notModified {
			if e, ok := c.cache[cacheKey]; ok {
				body = e.body
				e.expires = time.Now().Add(c.opts.cacheTTL)
				c.cache[cacheKey] = e
			}
		} else {
			c.cache[cacheKey] = cacheEntry{body: append([]byte(nil), body...), etag: newETag, expires: time.Now().Add(c.opts.cacheTTL)}
		}
	}
	f.body, f.err = body, err
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
			lastErr = err
			if ctx.Err() != nil {
				return nil, "", false, ctx.Err()
			}
		} else {
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if rerr != nil {
				lastErr = rerr
			} else if resp.StatusCode == http.StatusNotModified {
				return nil, etag, true, nil
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
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
