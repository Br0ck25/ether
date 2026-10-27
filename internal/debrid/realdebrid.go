package debrid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Real-Debrid REST API v1 (https://api.real-debrid.com). Bearer token auth.
// Real-Debrid removed its instant-availability endpoint in late 2024, so a
// torrent counts as cached when it reaches "downloaded" within a short poll
// window after its file is selected.
//
// Endpoints used:
//
//	POST   /torrents/addMagnet          form magnet=      -> {id, uri}
//	GET    /torrents/info/{id}                            -> {status, files[{id,path,bytes,selected}], links[]}
//	POST   /torrents/selectFiles/{id}   form files=1,2    -> 204
//	DELETE /torrents/delete/{id}                          -> 204
//	POST   /unrestrict/link             form link=        -> {download, filesize, filename}
//	GET    /user                                          -> {username, type, expiration}
const (
	realDebridBase    = "https://api.real-debrid.com/rest/1.0"
	realDebridLinkTTL = 12 * time.Hour
)

type realDebrid struct {
	key  string
	opts ProviderOptions
	list listCache
}

func newRealDebrid(key string, opts ProviderOptions) *realDebrid {
	return &realDebrid{key: key, opts: opts.withDefaults(realDebridBase)}
}

func (p *realDebrid) Kind() string { return "realdebrid" }

type rdError struct {
	Error     string `json:"error"`
	ErrorCode int    `json:"error_code"`
}

type rdFile struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Selected int    `json:"selected"`
}

type rdInfo struct {
	ID     string   `json:"id"`
	Hash   string   `json:"hash"`
	Status string   `json:"status"`
	Files  []rdFile `json:"files"`
	Links  []string `json:"links"`
}

func (p *realDebrid) do(ctx context.Context, r apiRequest, out any) error {
	status, body, err := call(ctx, p.opts.HTTPClient, p.opts.BaseURL, r, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+p.key)
	})
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		if out != nil && len(body) > 0 {
			return decodeJSON("real-debrid", body, out)
		}
		return nil
	}
	var e rdError
	_ = decodeJSON("real-debrid", body, &e)
	return p.classify(status, e)
}

// classify maps Real-Debrid error codes (documented on the API page).
func (p *realDebrid) classify(status int, e rdError) error {
	msg := e.Error
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch e.ErrorCode {
	// Codes from the Real-Debrid API documentation.
	case 8, 9, 10, 11, 12, 13, 14, 15, 20, 22, 23, 36:
		// Bad token, permission denied, two-factor, invalid login, account
		// locked or not activated, premium only, IP not allowed, traffic
		// exhausted, fair-usage limit: the account, not the release.
		return fmt.Errorf("real-debrid: %s: %w", msg, ErrAuth)
	case 5, 34: // slow down, too many requests
		return fmt.Errorf("real-debrid: %s: %w", msg, ErrRateLimited)
	case 7, 24: // resource not found, file unavailable
		return fmt.Errorf("real-debrid: %s: %w", msg, ErrGone)
	case 35: // infringing file: a cached torrent Real-Debrid will not serve
		return fmt.Errorf("real-debrid: %s: %w", msg, ErrBlocked)
	case 30: // torrent file invalid
		return fmt.Errorf("real-debrid: %s: %w", msg, core.ErrReleaseRejected)
	}
	// Everything else (including 25 "service unavailable" and 19 "hoster
	// temporarily unavailable") is transient.
	return statusError("real-debrid", status, msg)
}

func (p *realDebrid) info(ctx context.Context, id string) (*rdInfo, error) {
	var info rdInfo
	if err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/torrents/info/" + url.PathEscape(id)}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (p *realDebrid) CheckCached(context.Context, []string) (map[string]bool, error) {
	return nil, ErrUnsupported
}

// Find looks the hash up in the account's most recent torrents
// (GET /torrents?limit=100).
func (p *realDebrid) Find(ctx context.Context, hash string) (string, error) {
	return p.list.find(ctx, p.opts.Now(), hash, func(ctx context.Context) (map[string]string, error) {
		var items []struct {
			ID     string `json:"id"`
			Hash   string `json:"hash"`
			Status string `json:"status"`
		}
		if err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/torrents", query: url.Values{"limit": {"100"}}}, &items); err != nil {
			return nil, err
		}
		m := map[string]string{}
		for _, it := range items {
			if it.Status == "downloaded" && it.ID != "" {
				m[strings.ToLower(it.Hash)] = it.ID
			}
		}
		return m, nil
	})
}

func rdFiles(info *rdInfo) []File {
	out := make([]File, 0, len(info.Files))
	for _, f := range info.Files {
		out = append(out, File{ID: strconv.Itoa(f.ID), Path: f.Path, Size: f.Bytes, selected: f.Selected == 1})
	}
	return out
}

// linkFor maps a selected file to its hoster link: links[] holds one entry
// per selected file, in file order.
func rdLinkFor(info *rdInfo, fileID string) string {
	i := 0
	for _, f := range info.Files {
		if f.Selected != 1 {
			continue
		}
		if strconv.Itoa(f.ID) == fileID {
			if i < len(info.Links) {
				return info.Links[i]
			}
			return ""
		}
		i++
	}
	return ""
}

func (p *realDebrid) Acquire(ctx context.Context, req AcquireRequest) (_ *Acquired, err error) {
	id, added := req.TorrentID, false
	if id == "" {
		var out struct {
			ID string `json:"id"`
		}
		form := url.Values{"magnet": {req.Magnet}}
		if err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/torrents/addMagnet", form: form}, &out); err != nil {
			return nil, err
		}
		if out.ID == "" {
			return nil, errors.New("real-debrid: addMagnet returned no id")
		}
		id, added = out.ID, true
	}
	defer func() {
		if err == nil {
			p.list.remember(req.InfoHash, id)
		}
	}()
	cleanup := func() {
		if added && req.RemoveUncached {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if req.mayRemove(rctx, id) {
				_ = p.Remove(rctx, id)
			}
			cancel()
		}
	}
	info, err := p.info(ctx, id)
	for polls := 0; err == nil && info.Status == "magnet_conversion" && polls < p.opts.ReadyPolls*2; polls++ {
		if err = sleep(ctx, p.opts.PollInterval); err == nil {
			info, err = p.info(ctx, id)
		}
	}
	if err != nil {
		if added && !errors.Is(err, context.Canceled) {
			cleanup()
		}
		return nil, err
	}
	var chosen File
	switch info.Status {
	case "waiting_files_selection":
		if chosen, err = req.Choose(rdFiles(info)); err != nil {
			cleanup()
			return nil, err
		}
		form := url.Values{"files": {chosen.ID}}
		if err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/torrents/selectFiles/" + url.PathEscape(id), form: form}, nil); err != nil {
			cleanup()
			return nil, err
		}
		for polls := 0; polls < p.opts.ReadyPolls; polls++ {
			if info, err = p.info(ctx, id); err != nil {
				cleanup()
				return nil, err
			}
			if info.Status != "magnet_conversion" && info.Status != "waiting_files_selection" && info.Status != "queued" {
				break
			}
			if err = sleep(ctx, p.opts.PollInterval); err != nil {
				return nil, err
			}
		}
	case "downloaded":
		// A reused torrent: the wanted file must already be selected.
		if chosen, err = req.Choose(rdFiles(info)); err != nil {
			return nil, err
		}
		if !chosen.selected {
			return nil, fmt.Errorf("real-debrid: file not selected in existing torrent: %w", ErrGone)
		}
	}
	switch info.Status {
	case "downloaded":
	case "magnet_error", "error", "virus", "dead":
		cleanup()
		return nil, fmt.Errorf("real-debrid: torrent %s: %w", info.Status, core.ErrNotCached)
	default: // queued, downloading, compressing, uploading: not cached
		if req.CachedOnly {
			cleanup()
		}
		return nil, fmt.Errorf("real-debrid: torrent is %s: %w", info.Status, core.ErrNotCached)
	}
	chosen.Link = rdLinkFor(info, chosen.ID)
	if chosen.Link == "" {
		cleanup()
		return nil, fmt.Errorf("real-debrid: no link for the selected file: %w", core.ErrNotCached)
	}
	return &Acquired{TorrentID: id, File: chosen, Added: added}, nil
}

func (p *realDebrid) Link(ctx context.Context, h Handle) (DirectLink, error) {
	link := h.Link
	if link == "" {
		if h.TorrentID == "" {
			return DirectLink{}, ErrGone
		}
		info, err := p.info(ctx, h.TorrentID)
		if err != nil {
			return DirectLink{}, err
		}
		if info.Status != "downloaded" {
			return DirectLink{}, fmt.Errorf("real-debrid: torrent is %s: %w", info.Status, ErrGone)
		}
		if link = rdLinkFor(info, h.FileID); link == "" {
			return DirectLink{}, ErrGone
		}
	}
	var out struct {
		Download string `json:"download"`
		Filesize int64  `json:"filesize"`
	}
	if err := p.do(ctx, apiRequest{method: http.MethodPost, path: "/unrestrict/link", form: url.Values{"link": {link}}}, &out); err != nil {
		return DirectLink{}, err
	}
	if out.Download == "" {
		return DirectLink{}, fmt.Errorf("real-debrid: unrestrict returned no link: %w", ErrGone)
	}
	return DirectLink{URL: out.Download, Size: out.Filesize, Expires: p.opts.Now().Add(realDebridLinkTTL)}, nil
}

func (p *realDebrid) Remove(ctx context.Context, torrentID string) error {
	if torrentID == "" {
		return nil
	}
	p.list.forget(torrentID)
	return p.do(ctx, apiRequest{method: http.MethodDelete, path: "/torrents/delete/" + url.PathEscape(torrentID)}, nil)
}

func (p *realDebrid) Account(ctx context.Context) (Account, error) {
	var out struct {
		Username   string `json:"username"`
		Type       string `json:"type"`
		Expiration string `json:"expiration"`
	}
	if err := p.do(ctx, apiRequest{method: http.MethodGet, path: "/user"}, &out); err != nil {
		return Account{}, err
	}
	a := Account{User: out.Username, Premium: out.Type == "premium"}
	a.Expires, _ = time.Parse(time.RFC3339, out.Expiration)
	return a, nil
}
