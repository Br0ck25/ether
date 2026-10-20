package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// Provider errors. Callers classify with errors.Is.
var (
	// ErrAuth is a rejected API key or an account that cannot use the API
	// (expired premium). A configuration problem: never a release failure.
	ErrAuth = errors.New("debrid: authentication failed")
	// ErrRateLimited is HTTP 429 or the provider's equivalent.
	ErrRateLimited = errors.New("debrid: rate limited")
	// ErrGone is a torrent, file or link the provider no longer has; the
	// payload's stable identifiers can usually re-add it.
	ErrGone = errors.New("debrid: torrent or link no longer available")
	// ErrNoProvider means no debrid provider is enabled.
	ErrNoProvider = errors.New("debrid: no debrid provider is enabled")
	// ErrUnsupported is an operation a provider does not offer.
	ErrUnsupported = errors.New("debrid: not supported by this provider")
	// errNoVideo is a torrent without a playable file (or without the
	// wanted episode): a property of the release, not of a provider.
	errNoVideo = fmt.Errorf("debrid: no playable video file: %w", core.ErrReleaseRejected)
	// errFileMissing is a payload's file absent from its torrent.
	errFileMissing = fmt.Errorf("debrid: file is no longer in the torrent: %w", ErrGone)
)

// rateLimitError carries the provider's Retry-After hint.
type rateLimitError struct{ retryAfter time.Duration }

func (e *rateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *rateLimitError) Unwrap() error { return ErrRateLimited }

// File is one file of a torrent as a provider lists it.
type File struct {
	ID   string // provider file id ("" when the provider has none)
	Path string
	Size int64
	// Link is the provider-side link for the file (Real-Debrid hoster
	// link, AllDebrid file link, Premiumize direct link).
	Link     string
	selected bool // Real-Debrid: already selected in a reused torrent
}

// AcquireRequest asks a provider to make a torrent available.
type AcquireRequest struct {
	InfoHash string
	Magnet   string
	// TorrentID reuses a torrent already on the account ("" adds anew).
	TorrentID string
	// Choose picks the wanted file; providers that select files (Real-Debrid)
	// call it before selection.
	Choose         func([]File) (File, error)
	CachedOnly     bool
	RemoveUncached bool
}

// Acquired is a torrent made available by a provider.
type Acquired struct {
	TorrentID string
	File      File
	// Added reports whether this call added the torrent to the account.
	Added bool
}

// Handle identifies a file for link generation.
type Handle struct {
	TorrentID, FileID, Path, Link, Magnet, InfoHash string
	Size                                            int64
}

// DirectLink is a time-limited HTTPS URL for a file.
type DirectLink struct {
	URL     string
	Size    int64
	Expires time.Time
}

// Account is what the admin test reports.
type Account struct {
	User    string
	Premium bool
	Expires time.Time
}

// Provider is one debrid service. Implementations must never wait for an
// actual torrent download: Acquire returns core.ErrNotCached for a torrent
// the service cannot serve instantly (removing a torrent it just added when
// req.CachedOnly and req.RemoveUncached are set).
type Provider interface {
	Kind() string
	// CheckCached reports which hashes are cached; ErrUnsupported when the
	// service has no bulk availability check.
	CheckCached(ctx context.Context, hashes []string) (map[string]bool, error)
	// Find returns the id of a torrent with this hash already on the
	// account ("" when none), so re-adds are avoided; ErrUnsupported when
	// the service keeps no torrent list.
	Find(ctx context.Context, infoHash string) (string, error)
	Acquire(ctx context.Context, req AcquireRequest) (*Acquired, error)
	// Link returns a direct URL for a file; ErrGone when the torrent or
	// file must be re-acquired first.
	Link(ctx context.Context, h Handle) (DirectLink, error)
	Remove(ctx context.Context, torrentID string) error
	Account(ctx context.Context) (Account, error)
}

// ProviderOptions are shared by provider constructors (tests point BaseURL
// at an httptest server and shorten the polls).
type ProviderOptions struct {
	HTTPClient   *http.Client
	BaseURL      string
	PollInterval time.Duration // 0 = 500ms
	ReadyPolls   int           // 0 = 6: how long a cached torrent may take to report ready
	Now          func() time.Time
}

func (o ProviderOptions) withDefaults(base string) ProviderOptions {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if o.BaseURL == "" {
		o.BaseURL = base
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.PollInterval <= 0 {
		o.PollInterval = 500 * time.Millisecond
	}
	if o.ReadyPolls <= 0 {
		o.ReadyPolls = 6
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// NewProvider builds the provider for a config entry.
func NewProvider(cfg config.DebridProvider, opts ProviderOptions) (Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("debrid provider %q has no API key: %w", displayName(cfg), ErrAuth)
	}
	switch cfg.Kind {
	case config.DebridRealDebrid:
		return newRealDebrid(cfg.APIKey, opts), nil
	case config.DebridTorBox:
		return newTorBox(cfg.APIKey, opts), nil
	case config.DebridAllDebrid:
		return newAllDebrid(cfg.APIKey, opts), nil
	case config.DebridPremiumize:
		return newPremiumize(cfg.APIKey, opts), nil
	}
	return nil, fmt.Errorf("debrid: unknown provider kind %q", cfg.Kind)
}

func displayName(cfg config.DebridProvider) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	if cfg.ID != "" {
		return cfg.ID
	}
	return cfg.Kind
}

// ---------------------------------------------------------------------------
// HTTP plumbing shared by providers.

// apiRequest is one provider API call.
type apiRequest struct {
	method string
	path   string
	query  url.Values
	form   url.Values // urlencoded body
	body   io.Reader  // raw body (with contentType)
	ctype  string
}

// call performs an API request and returns the status and body. Errors
// never include the request URL: several providers take the key as a
// query parameter.
func call(ctx context.Context, hc *http.Client, base string, r apiRequest, auth func(*http.Request)) (int, []byte, error) {
	u := base + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var body io.Reader
	ctype := r.ctype
	switch {
	case r.form != nil:
		body = strings.NewReader(r.form.Encode())
		ctype = "application/x-www-form-urlencoded"
	case r.body != nil:
		body = r.body
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		return 0, nil, errors.New("debrid: invalid request")
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Ether")
	if auth != nil {
		auth(req)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, sanitize(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, sanitize(err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, data, &rateLimitError{retryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	return resp.StatusCode, data, nil
}

// sanitize strips the URL from transport errors.
func sanitize(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return fmt.Errorf("debrid: request timed out: %w", context.DeadlineExceeded)
		}
		return fmt.Errorf("debrid: request failed: %w", ue.Err)
	}
	return err
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// statusError maps an unexpected HTTP status to a classified error.
func statusError(kind string, status int, msg string) error {
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%s: %s: %w", kind, msg, ErrAuth)
	case status == http.StatusNotFound || status == http.StatusGone:
		return fmt.Errorf("%s: %s: %w", kind, msg, ErrGone)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%s: %s: %w", kind, msg, ErrRateLimited)
	}
	return fmt.Errorf("%s: HTTP %d: %s", kind, status, msg)
}

func decodeJSON(kind string, data []byte, out any) error {
	if err := json.Unmarshal(bytes.TrimSpace(data), out); err != nil {
		return fmt.Errorf("%s: unexpected response", kind)
	}
	return nil
}

// sleep waits d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// listCache remembers a provider's torrent list (hash -> id) briefly so
// reuse lookups cost at most one list call per minute.
type listCache struct {
	mu     sync.Mutex
	at     time.Time
	byHash map[string]string
}

const listTTL = time.Minute

func (c *listCache) find(ctx context.Context, now time.Time, hash string, load func(context.Context) (map[string]string, error)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHash == nil || now.Sub(c.at) > listTTL {
		m, err := load(ctx)
		if err != nil {
			return "", err
		}
		c.byHash, c.at = m, now
	}
	return c.byHash[strings.ToLower(hash)], nil
}

// remember records an id the provider just returned.
func (c *listCache) remember(hash, id string) {
	c.mu.Lock()
	if c.byHash != nil {
		c.byHash[strings.ToLower(hash)] = id
	}
	c.mu.Unlock()
}

// forget drops a removed torrent.
func (c *listCache) forget(id string) {
	c.mu.Lock()
	for h, v := range c.byHash {
		if v == id {
			delete(c.byHash, h)
		}
	}
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// File selection.

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".ts": true,
	".m2ts": true, ".webm": true, ".wmv": true, ".mpg": true, ".mpeg": true,
}

func isVideo(p string) bool {
	return videoExts[strings.ToLower(path.Ext(p))]
}

// isExtra reports samples, trailers and featurettes.
func isExtra(p string) bool {
	lower := strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	for _, tok := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '/' || r == '.' || r == '-' || r == '_' || r == ' ' || r == '[' || r == ']' || r == '(' || r == ')'
	}) {
		switch tok {
		case "sample", "samples", "trailer", "trailers", "featurette", "featurettes", "extras":
			return true
		}
	}
	return false
}

// ChooseFile picks the main video file of a torrent: for a target episode
// the largest file whose name matches it, else the largest video.
func ChooseFile(files []File, target *core.ReleaseTarget) (File, error) {
	var videos []File
	for _, f := range files {
		if isVideo(f.Path) && !isExtra(f.Path) && f.Size > 0 {
			videos = append(videos, f)
		}
	}
	if len(videos) == 0 {
		return File{}, errNoVideo
	}
	sort.SliceStable(videos, func(i, j int) bool { return videos[i].Size > videos[j].Size })
	if target == nil || target.Episode <= 0 {
		return videos[0], nil
	}
	for _, f := range videos {
		info := releaseparse.Parse(path.Base(strings.ReplaceAll(f.Path, "\\", "/")))
		if releaseparse.EpisodeMatches(info, target.Season, target.Episode) {
			return f, nil
		}
	}
	// Path components can carry the season ("Season 2/Episode 05.mkv").
	for _, f := range videos {
		info := releaseparse.Parse(strings.NewReplacer("/", " ", "\\", " ").Replace(f.Path))
		if releaseparse.EpisodeMatches(info, target.Season, target.Episode) {
			return f, nil
		}
	}
	// A single-episode torrent whose file name carries no episode number.
	// A file that names a different episode (a partial pack) never stands
	// in for the wanted one.
	if len(videos) == 1 {
		info := releaseparse.Parse(strings.NewReplacer("/", " ", "\\", " ").Replace(videos[0].Path))
		if len(info.Episodes) == 0 {
			return videos[0], nil
		}
	}
	return File{}, fmt.Errorf("debrid: no file for S%02dE%02d in torrent: %w", target.Season, target.Episode, errNoVideo)
}

// sameFile chooses the file a payload already names (link refresh after
// re-adding a torrent, or on another provider): same base name and size,
// else the only file of that size.
func sameFile(p Payload) func([]File) (File, error) {
	return func(files []File) (File, error) {
		name := strings.ToLower(p.Name())
		var bySize []File
		for _, f := range files {
			if f.Size != p.Size {
				continue
			}
			if strings.ToLower(path.Base(strings.ReplaceAll(f.Path, "\\", "/"))) == name {
				return f, nil
			}
			bySize = append(bySize, f)
		}
		if len(bySize) == 1 {
			return bySize[0], nil
		}
		return File{}, fmt.Errorf("%s: %w", p.Name(), errFileMissing)
	}
}
