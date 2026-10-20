package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// AllDebrid API v4 / v4.1 (https://api.alldebrid.com). Bearer API key plus
// the agent parameter. AllDebrid answers HTTP 200 with
// {"status":"error","error":{"code":...}} on failure.
//
// Its torrent API changed in 2024-2025 (the instant-availability endpoint
// was removed; magnet status no longer carries links, which moved to
// /v4/magnet/files). Endpoints used:
//
//	POST /v4/magnet/upload   magnets[]=            -> data.magnets[{id,hash,ready}]
//	GET  /v4.1/magnet/status id=                   -> data.magnets{id,hash,statusCode} (4 = ready)
//	GET  /v4.1/magnet/status                       -> data.magnets[...] (reuse by hash)
//	POST /v4/magnet/files    id[]=                 -> data.magnets[{id,files:[{n,s,l}|{n,e:[...]}]}]
//	POST /v4/link/unlock     link=                 -> data{link,filesize}
//	POST /v4/magnet/delete   id=
//	GET  /v4/user                                  -> data.user{username,isPremium,premiumUntil}
const (
	allDebridBase    = "https://api.alldebrid.com"
	allDebridAgent   = "ether"
	allDebridLinkTTL = 6 * time.Hour
	adStatusReady    = 4
)

type allDebrid struct {
	key  string
	opts ProviderOptions
	list listCache
}

func newAllDebrid(key string, opts ProviderOptions) *allDebrid {
	return &allDebrid{key: key, opts: opts.withDefaults(allDebridBase)}
}

func (p *allDebrid) Kind() string { return "alldebrid" }

type adEnvelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type adMagnet struct {
	ID         json.Number `json:"id"`
	Hash       string      `json:"hash"`
	Ready      bool        `json:"ready"`
	StatusCode int         `json:"statusCode"`
	Error      *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *allDebrid) do(ctx context.Context, r apiRequest) (json.RawMessage, error) {
	if r.query == nil {
		r.query = url.Values{}
	}
	r.query.Set("agent", allDebridAgent)
	status, body, err := call(ctx, p.opts.HTTPClient, p.opts.BaseURL, r, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+p.key)
	})
	if err != nil {
		return nil, err
	}
	var env adEnvelope
	derr := decodeJSON("alldebrid", body, &env)
	if derr == nil && env.Status == "success" {
		return env.Data, nil
	}
	if derr != nil && status >= 200 && status < 300 {
		return nil, derr
	}
	return nil, adError(status, env.Error.Code, env.Error.Message)
}

func adError(status int, code, msg string) error {
	if msg == "" {
		msg = code
	}
	switch {
	case strings.HasPrefix(code, "AUTH_") || strings.HasSuffix(code, "MUST_BE_PREMIUM") || code == "MUST_BE_PREMIUM" || code == "FREE_TRIAL_LIMIT_REACHED":
		return fmt.Errorf("alldebrid: %s: %w", msg, ErrAuth)
	case code == "MAGNET_INVALID_ID" || code == "LINK_DOWN" || code == "LINK_NOT_FOUND" || code == "MAGNET_NOT_FOUND" || code == "LINK_HOST_UNAVAILABLE":
		return fmt.Errorf("alldebrid: %s: %w", msg, ErrGone)
	case strings.Contains(code, "TOO_MANY") || strings.Contains(code, "RATE"):
		return fmt.Errorf("alldebrid: %s: %w", msg, ErrRateLimited)
	case code == "MAGNET_INVALID_URI" || code == "MAGNET_NO_URI" || code == "MAGNET_PROCESSING_FAILED":
		return fmt.Errorf("alldebrid: %s: %w", msg, core.ErrNotCached)
	}
	if status < 300 {
		status = http.StatusBadGateway
	}
	return statusError("alldebrid", status, msg)
}

func (p *allDebrid) CheckCached(context.Context, []string) (map[string]bool, error) {
	return nil, ErrUnsupported
}

// magnets decodes data.magnets as an object (single id) or an array.
func adMagnets(data json.RawMessage) []adMagnet {
	var wrap struct {
		Magnets json.RawMessage `json:"magnets"`
	}
	if json.Unmarshal(data, &wrap) != nil {
		return nil
	}
	t := bytes.TrimSpace(wrap.Magnets)
	if len(t) == 0 {
		return nil
	}
	if t[0] == '{' {
		var m adMagnet
		if json.Unmarshal(t, &m) == nil {
			return []adMagnet{m}
		}
		return nil
	}
	var ms []adMagnet
	_ = json.Unmarshal(t, &ms)
	return ms
}

func (p *allDebrid) Find(ctx context.Context, hash string) (string, error) {
	return p.list.find(ctx, p.opts.Now(), hash, func(ctx context.Context) (map[string]string, error) {
		data, err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/v4.1/magnet/status"})
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for _, mg := range adMagnets(data) {
			if mg.StatusCode == adStatusReady {
				m[strings.ToLower(mg.Hash)] = mg.ID.String()
			}
		}
		return m, nil
	})
}

func (p *allDebrid) status(ctx context.Context, id string) (*adMagnet, error) {
	data, err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/v4.1/magnet/status", query: url.Values{"id": {id}}})
	if err != nil {
		return nil, err
	}
	ms := adMagnets(data)
	if len(ms) == 0 {
		return nil, fmt.Errorf("alldebrid: magnet %s not listed: %w", id, ErrGone)
	}
	return &ms[0], nil
}

// files lists a ready magnet's files, flattening folders.
func (p *allDebrid) files(ctx context.Context, id string) ([]File, error) {
	data, err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/v4/magnet/files", form: url.Values{"id[]": {id}}})
	if err != nil {
		return nil, err
	}
	type node struct {
		N string            `json:"n"`
		S int64             `json:"s"`
		L string            `json:"l"`
		E []json.RawMessage `json:"e"`
	}
	var wrap struct {
		Magnets []struct {
			ID    json.Number       `json:"id"`
			Files []json.RawMessage `json:"files"`
		} `json:"magnets"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil || len(wrap.Magnets) == 0 {
		return nil, fmt.Errorf("alldebrid: no files for magnet %s: %w", id, ErrGone)
	}
	var out []File
	var walk func(prefix string, raws []json.RawMessage, depth int)
	walk = func(prefix string, raws []json.RawMessage, depth int) {
		if depth > 16 {
			return
		}
		for _, raw := range raws {
			var n node
			if json.Unmarshal(raw, &n) != nil {
				continue
			}
			p := path.Join(prefix, n.N)
			if len(n.E) > 0 {
				walk(p, n.E, depth+1)
				continue
			}
			if n.L != "" {
				out = append(out, File{ID: n.L, Path: p, Size: n.S, Link: n.L})
			}
		}
	}
	walk("", wrap.Magnets[0].Files, 0)
	return out, nil
}

func (p *allDebrid) Acquire(ctx context.Context, req AcquireRequest) (*Acquired, error) {
	id, added := req.TorrentID, false
	ready := false
	if id == "" {
		data, err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/v4/magnet/upload", form: url.Values{"magnets[]": {req.Magnet}}})
		if err != nil {
			return nil, err
		}
		ms := adMagnets(data)
		if len(ms) == 0 {
			return nil, errors.New("alldebrid: upload returned no magnet")
		}
		if ms[0].Error != nil {
			return nil, adError(http.StatusOK, ms[0].Error.Code, ms[0].Error.Message)
		}
		id, added, ready = ms[0].ID.String(), true, ms[0].Ready
	}
	cleanup := func() {
		if added && req.RemoveUncached {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if req.mayRemove(rctx, id) {
				_ = p.Remove(rctx, id)
			}
			cancel()
		}
	}
	for polls := 0; !ready && polls < p.opts.ReadyPolls; polls++ {
		st, err := p.status(ctx, id)
		if err != nil {
			return nil, err
		}
		if st.StatusCode == adStatusReady {
			ready = true
			break
		}
		if st.StatusCode > adStatusReady { // 5+ are errors
			cleanup()
			return nil, fmt.Errorf("alldebrid: magnet failed (status %d): %w", st.StatusCode, core.ErrNotCached)
		}
		if err := sleep(ctx, p.opts.PollInterval); err != nil {
			return nil, err
		}
	}
	if !ready {
		if req.CachedOnly {
			cleanup()
		}
		return nil, fmt.Errorf("alldebrid: magnet is not ready: %w", core.ErrNotCached)
	}
	files, err := p.files(ctx, id)
	if err != nil {
		return nil, err
	}
	chosen, err := req.Choose(files)
	if err != nil {
		return nil, err
	}
	p.list.remember(req.InfoHash, id)
	return &Acquired{TorrentID: id, File: chosen, Added: added}, nil
}

func (p *allDebrid) Link(ctx context.Context, h Handle) (DirectLink, error) {
	link := h.Link
	if link == "" {
		if h.TorrentID == "" {
			return DirectLink{}, ErrGone
		}
		files, err := p.files(ctx, h.TorrentID)
		if err != nil {
			return DirectLink{}, err
		}
		f, err := sameFile(Payload{Path: h.Path, Size: h.Size})(files)
		if err != nil {
			return DirectLink{}, err
		}
		link = f.Link
	}
	data, err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/v4/link/unlock", form: url.Values{"link": {link}}})
	if err != nil {
		return DirectLink{}, err
	}
	var out struct {
		Link     string          `json:"link"`
		Filesize int64           `json:"filesize"`
		Delayed  json.RawMessage `json:"delayed"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return DirectLink{}, errors.New("alldebrid: unexpected unlock response")
	}
	if out.Link == "" {
		// Delayed links need a second polling API; treat as temporarily unavailable.
		return DirectLink{}, errors.New("alldebrid: link generation delayed")
	}
	return DirectLink{URL: out.Link, Size: out.Filesize, Expires: p.opts.Now().Add(allDebridLinkTTL)}, nil
}

func (p *allDebrid) Remove(ctx context.Context, torrentID string) error {
	if torrentID == "" {
		return nil
	}
	p.list.forget(torrentID)
	_, err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/v4/magnet/delete", form: url.Values{"id": {torrentID}}})
	return err
}

func (p *allDebrid) Account(ctx context.Context) (Account, error) {
	data, err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/v4/user"})
	if err != nil {
		return Account{}, err
	}
	var out struct {
		User struct {
			Username     string `json:"username"`
			IsPremium    bool   `json:"isPremium"`
			PremiumUntil int64  `json:"premiumUntil"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Account{}, errors.New("alldebrid: unexpected account response")
	}
	a := Account{User: out.User.Username, Premium: out.User.IsPremium}
	if out.User.PremiumUntil > 0 {
		a.Expires = time.Unix(out.User.PremiumUntil, 0)
	}
	return a, nil
}
