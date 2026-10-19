package newznab

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

const (
	defaultMaxNZBBytes = 50 << 20
	defaultSearchLimit = 100
	defaultHTTPTimeout = 30 * time.Second
	defaultCacheTTL    = 6 * time.Hour
)

// Options tunes a Client or Manager. Zero values get sensible defaults.
type Options struct {
	HTTPClient     *http.Client
	Logger         *slog.Logger
	SearchCacheTTL time.Duration // <0 disables cache, 0 = default 6h
	NZBDir         string        // directory for downloaded NZBs; "" keeps them in memory only
	MaxNZBBytes    int64
	Now            func() time.Time // injected clock (tests)
	MaxParallel    int              // Manager fan-out; 0 = 4
}

// Client is one configured Newznab indexer. It implements core.Indexer.
type Client struct {
	cfg      config.Indexer
	db       *sql.DB
	http     *http.Client
	log      *slog.Logger
	cacheTTL time.Duration
	nzbDir   string
	maxNZB   int64
	now      func() time.Time

	budgetMu sync.Mutex
	mem      memBudget

	cacheMu  sync.RWMutex
	memCache map[string]memCacheEntry

	capsMu sync.Mutex
	caps   *Caps
	capsAt time.Time

	cfgMu sync.RWMutex
}

var _ core.Indexer = (*Client)(nil)

// New builds a Client for one indexer. db may be nil (budgets and cache
// then live in memory).
func New(cfg config.Indexer, db *sql.DB, opts Options) *Client {
	c := &Client{cfg: cfg, db: db, nzbDir: opts.NZBDir}
	c.http = opts.HTTPClient
	if c.http == nil {
		c.http = &http.Client{Timeout: defaultHTTPTimeout}
	}
	c.log = opts.Logger
	if c.log == nil {
		c.log = slog.Default()
	}
	c.now = opts.Now
	if c.now == nil {
		c.now = time.Now
	}
	switch {
	case opts.SearchCacheTTL < 0:
		c.cacheTTL = 0
	case opts.SearchCacheTTL == 0:
		c.cacheTTL = defaultCacheTTL
	default:
		c.cacheTTL = opts.SearchCacheTTL
	}
	c.maxNZB = opts.MaxNZBBytes
	if c.maxNZB <= 0 {
		c.maxNZB = defaultMaxNZBBytes
	}
	return c
}

// NewClient is an alias for New.
func NewClient(cfg config.Indexer, db *sql.DB, opts Options) *Client { return New(cfg, db, opts) }

// Config returns the indexer's current config.
func (c *Client) Config() config.Indexer {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

// UpdateConfig swaps the indexer config (called on config reload).
func (c *Client) UpdateConfig(cfg config.Indexer) {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	c.cfg = cfg
}

// SetCacheTTL changes the result-cache TTL (called on config reload).
// A negative value disables the cache.
func (c *Client) SetCacheTTL(d time.Duration) {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	if d < 0 {
		c.cacheTTL = 0
	} else {
		c.cacheTTL = d
	}
}

// Name implements core.Indexer.
func (c *Client) Name() string {
	cfg := c.Config()
	if cfg.Name != "" {
		return cfg.Name
	}
	if cfg.ID != "" {
		return cfg.ID
	}
	if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return cfg.URL
}

func (c *Client) apiURL(params url.Values) (string, error) {
	cfg := c.Config()
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		return "", fmt.Errorf("newznab: indexer %q has no URL", c.Name())
	}
	if !strings.HasSuffix(base, "/api") {
		base += "/api"
	}
	q := url.Values{}
	for k, vs := range params {
		q[k] = vs
	}
	if cfg.APIKey != "" && q.Get("apikey") == "" {
		q.Set("apikey", cfg.APIKey)
	}
	return base + "?" + q.Encode(), nil
}

// get performs a GET and returns the body, mapping HTTP failures to
// typed API errors. Responses are size-limited.
func (c *Client) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Ether/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("newznab: response exceeds %d bytes", limit)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, apiError(429, "too many requests")
	}
	if resp.StatusCode != http.StatusOK {
		// Indexers often still return a Newznab error document.
		if _, perr := parseFeed(body, c.Name()); perr != nil {
			var ae *APIError
			if errors.As(perr, &ae) {
				return nil, perr
			}
		}
		return nil, apiError(resp.StatusCode, resp.Status)
	}
	return body, nil
}

// Caps returns the indexer's capabilities, cached for 24h.
func (c *Client) Caps(ctx context.Context) (*Caps, error) {
	c.capsMu.Lock()
	if c.caps != nil && c.now().Sub(c.capsAt) < 24*time.Hour {
		defer c.capsMu.Unlock()
		return c.caps, nil
	}
	c.capsMu.Unlock()
	if caps, ok := c.capsFromDB(ctx); ok {
		c.capsMu.Lock()
		c.caps, c.capsAt = caps, c.now()
		c.capsMu.Unlock()
		return caps, nil
	}
	if err := c.reserve(ctx, "hit"); err != nil {
		return nil, err
	}
	u, err := c.apiURL(url.Values{"t": {"caps"}})
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, u, 5<<20)
	if err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] != '{' {
		// Error documents reach here with HTTP 200.
		if _, perr := parseFeed(body, c.Name()); perr != nil {
			return nil, perr
		}
	}
	caps, err := parseCaps(body)
	if err != nil {
		return nil, fmt.Errorf("newznab: parse caps: %w", err)
	}
	c.capsMu.Lock()
	c.caps, c.capsAt = caps, c.now()
	c.capsMu.Unlock()
	c.capsToDB(ctx, caps)
	return caps, nil
}

func cacheKey(c *Client, req core.SearchRequest) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s|%s|%d|%s|%d|%s|%d|%d|%d",
		c.indexerID(), req.Type, req.IDs.TMDB, req.IDs.IMDB, req.IDs.TVDB,
		strings.ToLower(strings.TrimSpace(req.Title)), req.Year, req.Season, req.Episode)
	return hex.EncodeToString(h.Sum(nil))
}

func joinCats(cats []int) string {
	parts := make([]string, len(cats))
	for i, n := range cats {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// searchAttempts builds the ordered fallback queries for a request.
func (c *Client) searchAttempts(req core.SearchRequest, caps *Caps) []url.Values {
	cfg := c.Config()
	isTV := req.Type == core.TypeEpisode || req.Season > 0 || (req.Type == core.TypeShow)
	base := func() url.Values {
		v := url.Values{"extended": {"1"}, "limit": {strconv.Itoa(defaultSearchLimit)}}
		if isTV {
			cats := cfg.TVCategories
			if len(cats) == 0 {
				cats = []int{5000}
			}
			v.Set("cat", joinCats(cats))
		} else {
			cats := cfg.MovieCategories
			if len(cats) == 0 {
				cats = []int{2000}
			}
			v.Set("cat", joinCats(cats))
		}
		return v
	}
	imdb := strings.TrimPrefix(req.IDs.IMDB, "tt")
	var attempts []url.Values
	if isTV {
		mk := func(idKey, idVal string) url.Values {
			v := base()
			v.Set("t", "tvsearch")
			if idKey != "" {
				v.Set(idKey, idVal)
			}
			if req.Season > 0 {
				v.Set("season", strconv.Itoa(req.Season))
			}
			if req.Episode > 0 {
				v.Set("ep", strconv.Itoa(req.Episode))
			}
			return v
		}
		if req.IDs.TVDB > 0 && (caps == nil || (!caps.TVAvail || hasParam(caps.TVParams, "tvdbid"))) {
			attempts = append(attempts, mk("tvdbid", strconv.FormatInt(req.IDs.TVDB, 10)))
		}
		if req.IDs.TMDB > 0 && (caps == nil || (!caps.TVAvail || hasParam(caps.TVParams, "tmdbid"))) {
			attempts = append(attempts, mk("tmdbid", strconv.FormatInt(req.IDs.TMDB, 10)))
		}
		if imdb != "" && (caps == nil || (!caps.TVAvail || hasParam(caps.TVParams, "imdbid"))) {
			attempts = append(attempts, mk("imdbid", imdb))
		}
		if req.Title != "" {
			q := req.Title
			if req.Season > 0 && req.Episode > 0 {
				q = fmt.Sprintf("%s S%02dE%02d", req.Title, req.Season, req.Episode)
			}
			v := mk("", "")
			v.Set("q", q)
			attempts = append(attempts, v)
			v2 := base()
			v2.Set("t", "search")
			v2.Set("q", q)
			attempts = append(attempts, v2)
		}
		return attempts
	}
	if imdb != "" && (caps == nil || (!caps.MovieAvail || hasParam(caps.MovieParams, "imdbid"))) {
		v := base()
		v.Set("t", "movie")
		v.Set("imdbid", imdb)
		attempts = append(attempts, v)
	}
	if req.IDs.TMDB > 0 && (caps == nil || (!caps.MovieAvail || hasParam(caps.MovieParams, "tmdbid"))) {
		v := base()
		v.Set("t", "movie")
		v.Set("tmdbid", strconv.FormatInt(req.IDs.TMDB, 10))
		attempts = append(attempts, v)
	}
	if req.Title != "" {
		q := req.Title
		if req.Year > 0 {
			q = fmt.Sprintf("%s %d", req.Title, req.Year)
		}
		v := base()
		v.Set("t", "search")
		v.Set("q", q)
		attempts = append(attempts, v)
	}
	return attempts
}

func (c *Client) doSearch(ctx context.Context, params url.Values) ([]core.Release, error) {
	if err := c.reserve(ctx, "hit"); err != nil {
		return nil, err
	}
	u, err := c.apiURL(params)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, u, 20<<20)
	if err != nil {
		return nil, err
	}
	return parseFeed(body, c.Name())
}

// Search implements core.Indexer.
func (c *Client) Search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	if cfg := c.Config(); !cfg.Enabled {
		return nil, fmt.Errorf("indexer %q is disabled", c.Name())
	}
	key := cacheKey(c, req)
	if cached, ok := c.cacheGet(ctx, key); ok {
		return cached, nil
	}
	// Exhausted limits prohibit new requests, not reuse of already fetched
	// results and NZBs. The manager must reach this cache before this check.
	if c.Exhausted(ctx) {
		return nil, fmt.Errorf("indexer %q: %w", c.Name(), core.ErrBudgetExhausted)
	}
	caps, _ := c.Caps(ctx) // best effort; failures just mean no param gating
	attempts := c.searchAttempts(req, caps)
	if len(attempts) == 0 {
		return nil, fmt.Errorf("newznab: search request has no usable identifiers")
	}
	var firstErr error
	for _, params := range attempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		results, err := c.doSearch(ctx, params)
		if err != nil {
			if errors.Is(err, core.ErrBudgetExhausted) {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			c.log.Debug("newznab search attempt failed", "indexer", c.Name(), "t", params.Get("t"), "err", err)
			continue
		}
		if len(results) > 0 {
			c.cachePut(ctx, key, results)
			return results, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	c.cachePut(ctx, key, nil)
	return nil, nil
}

// nzbDiskPath returns the on-disk NZB path for a release id, or "".
func (c *Client) nzbDiskPath(releaseID string) string {
	if c.nzbDir == "" || releaseID == "" {
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, releaseID)
	return filepath.Join(c.nzbDir, safe+".nzb")
}

// FetchNZB implements core.Indexer.
func (c *Client) FetchNZB(ctx context.Context, r core.Release) ([]byte, error) {
	if path := c.nzbDiskPath(r.ID); path != "" {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	if err := c.reserve(ctx, "grab"); err != nil {
		return nil, err
	}
	rawURL := r.NZBURL
	if rawURL == "" {
		if r.GUID == "" {
			return nil, fmt.Errorf("newznab: release %q has no NZB URL or GUID", r.Name)
		}
		u, err := c.apiURL(url.Values{"t": {"get"}, "id": {r.GUID}})
		if err != nil {
			return nil, err
		}
		rawURL = u
	} else if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		base, err := url.Parse(strings.TrimRight(c.Config().URL, "/") + "/")
		if err != nil {
			return nil, err
		}
		ref, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		rawURL = base.ResolveReference(ref).String()
	}
	body, err := c.get(ctx, rawURL, c.maxNZB)
	if err != nil {
		return nil, err
	}
	// Gzipped NZB payload (magic bytes) — Go's transport only undoes
	// Content-Encoding, not a stored .nzb.gz body.
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("newznab: gunzip NZB: %w", err)
		}
		body, err = io.ReadAll(io.LimitReader(gz, c.maxNZB+1))
		gz.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > c.maxNZB {
			return nil, fmt.Errorf("newznab: NZB exceeds %d bytes", c.maxNZB)
		}
	}
	trimmed := bytes.TrimSpace(body)
	if !bytes.Contains(bytes.ToLower(trimmed[:min(len(trimmed), 2048)]), []byte("<nzb")) {
		if _, perr := parseFeed(body, c.Name()); perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("newznab: response for %q is not an NZB", r.Name)
	}
	if path := c.nzbDiskPath(r.ID); path != "" {
		if err := os.MkdirAll(c.nzbDir, 0o755); err == nil {
			tmp := path + ".tmp"
			if os.WriteFile(tmp, body, 0o644) == nil {
				_ = os.Rename(tmp, path)
			}
		}
	}
	return body, nil
}

// Test verifies an indexer config for the setup wizard: caps plus one
// search, with human-readable errors.
func Test(ctx context.Context, cfg config.Indexer) error {
	return TestWithOptions(ctx, cfg, Options{})
}

// TestWithOptions is Test with injectable transport/clock (tests).
func TestWithOptions(ctx context.Context, cfg config.Indexer, opts Options) error {
	if cfg.URL == "" {
		return fmt.Errorf("indexer %q: URL is empty", cfg.Name)
	}
	// Test always runs, even for a not-yet-enabled config.
	testCfg := cfg
	testCfg.Enabled = true
	c := New(testCfg, nil, opts)
	name := cfg.Name
	if name == "" {
		name = c.Name()
	}
	if _, err := c.Caps(ctx); err != nil {
		return humanError(name, err)
	}
	params := url.Values{"t": {"search"}, "q": {"test"}, "limit": {"1"}, "extended": {"1"}}
	if _, err := c.doSearch(ctx, params); err != nil {
		return humanError(name, err)
	}
	return nil
}
