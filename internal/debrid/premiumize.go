package debrid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Premiumize API (https://www.premiumize.me/api). The API key is sent as the
// apikey parameter (form body for POST, query for GET). Cached content is
// served by directdl without adding anything to the account, so there is
// no torrent id to reuse or remove.
//
// Endpoints used:
//
//	GET  /cache/check?items[]=hash        -> {status, response[], filename[], filesize[]}
//	POST /transfer/directdl  src=magnet    -> {status, content[{path,size,link,stream_link}]}
//	POST /transfer/create    src=magnet    (only when not cached-only)
//	GET  /account/info                     -> {status, customer_id, premium_until}
const (
	premiumizeBase    = "https://www.premiumize.me/api"
	premiumizeLinkTTL = 6 * time.Hour
)

type premiumize struct {
	key  string
	opts ProviderOptions
}

func newPremiumize(key string, opts ProviderOptions) *premiumize {
	return &premiumize{key: key, opts: opts.withDefaults(premiumizeBase)}
}

func (p *premiumize) Kind() string { return "premiumize" }

type pmStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (p *premiumize) do(ctx context.Context, r apiRequest, out any) error {
	if r.method == http.MethodGet {
		if r.query == nil {
			r.query = url.Values{}
		}
		r.query.Set("apikey", p.key)
	} else {
		if r.form == nil {
			r.form = url.Values{}
		}
		r.form.Set("apikey", p.key)
	}
	status, body, err := call(ctx, p.opts.HTTPClient, p.opts.BaseURL, r, nil)
	if err != nil {
		return err
	}
	var st pmStatus
	derr := decodeJSON("premiumize", body, &st)
	if status >= 200 && status < 300 && derr == nil && st.Status == "success" {
		if out != nil {
			return decodeJSON("premiumize", body, out)
		}
		return nil
	}
	msg := st.Message
	lower := strings.ToLower(msg)
	switch {
	case status == http.StatusUnauthorized || strings.Contains(lower, "logged in") || strings.Contains(lower, "api key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "premium account") || strings.Contains(lower, "premium membership"):
		return fmt.Errorf("premiumize: %s: %w", msg, ErrAuth)
	case strings.Contains(lower, "not cached") || strings.Contains(lower, "no cached") || strings.Contains(lower, "content not in cache"):
		return fmt.Errorf("premiumize: %s: %w", msg, core.ErrNotCached)
	case strings.Contains(lower, "too many") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate-limit"):
		return fmt.Errorf("premiumize: %s: %w", msg, ErrRateLimited)
	}
	if status >= 200 && status < 300 {
		status = http.StatusBadGateway
	}
	return statusError("premiumize", status, msg)
}

func (p *premiumize) CheckCached(ctx context.Context, hashes []string) (map[string]bool, error) {
	out := make(map[string]bool, len(hashes))
	for start := 0; start < len(hashes); start += 50 {
		batch := hashes[start:min(start+50, len(hashes))]
		var resp struct {
			Response []bool `json:"response"`
		}
		if err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/cache/check", query: url.Values{"items[]": batch}}, &resp); err != nil {
			return nil, err
		}
		for i, h := range batch {
			out[strings.ToLower(h)] = i < len(resp.Response) && resp.Response[i]
		}
	}
	return out, nil
}

func (p *premiumize) Find(context.Context, string) (string, error) { return "", ErrUnsupported }

type pmContent struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Link       string `json:"link"`
	StreamLink string `json:"stream_link"`
}

func (p *premiumize) directdl(ctx context.Context, magnet string) ([]File, error) {
	var resp struct {
		Content []pmContent `json:"content"`
	}
	if err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/transfer/directdl", form: url.Values{"src": {magnet}}}, &resp); err != nil {
		return nil, err
	}
	files := make([]File, 0, len(resp.Content))
	for _, c := range resp.Content {
		if c.Link != "" {
			files = append(files, File{ID: c.Path, Path: c.Path, Size: c.Size, Link: c.Link})
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("premiumize: no cached content: %w", core.ErrNotCached)
	}
	return files, nil
}

func (p *premiumize) Acquire(ctx context.Context, req AcquireRequest) (*Acquired, error) {
	cached, err := p.CheckCached(ctx, []string{req.InfoHash})
	if err != nil {
		return nil, err
	}
	if !cached[strings.ToLower(req.InfoHash)] {
		if !req.CachedOnly {
			// Queue a cloud download for later; playback never waits for it.
			_ = p.do(ctx, apiRequest{method: http.MethodPost, path: "/transfer/create", form: url.Values{"src": {req.Magnet}}}, nil)
		}
		return nil, fmt.Errorf("premiumize: %w", core.ErrNotCached)
	}
	files, err := p.directdl(ctx, req.Magnet)
	if err != nil {
		return nil, err
	}
	chosen, err := req.Choose(files)
	if err != nil {
		return nil, err
	}
	return &Acquired{File: chosen}, nil
}

func (p *premiumize) Link(ctx context.Context, h Handle) (DirectLink, error) {
	if h.Magnet == "" {
		return DirectLink{}, errors.New("premiumize: payload has no magnet")
	}
	files, err := p.directdl(ctx, h.Magnet)
	if err != nil {
		if errors.Is(err, core.ErrNotCached) {
			return DirectLink{}, fmt.Errorf("%w: %w", ErrGone, err)
		}
		return DirectLink{}, err
	}
	f, err := sameFile(Payload{Path: h.Path, Size: h.Size})(files)
	if err != nil {
		return DirectLink{}, err
	}
	return DirectLink{URL: f.Link, Size: f.Size, Expires: p.opts.Now().Add(premiumizeLinkTTL)}, nil
}

func (p *premiumize) Remove(context.Context, string) error { return nil }

func (p *premiumize) Account(ctx context.Context) (Account, error) {
	var out struct {
		CustomerID   any   `json:"customer_id"`
		PremiumUntil int64 `json:"premium_until"`
	}
	if err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/account/info"}, &out); err != nil {
		return Account{}, err
	}
	a := Account{User: fmt.Sprint(out.CustomerID), Premium: out.PremiumUntil > p.opts.Now().Unix()}
	if out.PremiumUntil > 0 {
		a.Expires = time.Unix(out.PremiumUntil, 0)
	}
	return a, nil
}
