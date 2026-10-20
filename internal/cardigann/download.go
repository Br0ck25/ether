package cardigann

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
)

// errSessionExpired marks a download that returned a page instead of a
// payload, so the caller logs in again once.
var errSessionExpired = errors.New("the site returned a web page instead of the download; the session may have expired")

// downloadVars adds .DownloadUri for download-time templates.
func (r *Runner) downloadVars(link *url.URL) vars {
	v := r.vars()
	q := map[string]string{}
	for k, vals := range link.Query() {
		if len(vals) > 0 {
			q[k] = vals[0]
		}
	}
	v["DownloadUri"] = map[string]any{
		"AbsoluteUri":  link.String(),
		"AbsolutePath": link.EscapedPath(),
		"PathAndQuery": link.RequestURI(),
		"Scheme":       link.Scheme,
		"Host":         link.Hostname(),
		"Port":         link.Port(),
		"Query":        q,
	}
	return v
}

// payload is a download's result: a document (.torrent or .nzb) or a magnet.
type payload struct {
	body   []byte
	magnet string
}

// fetch downloads a release link through the definition's download block,
// counting one grab and signing in again once if the session expired.
func (r *Runner) fetch(ctx context.Context, link string, reserve Reserver) (payload, error) {
	if reserve == nil {
		reserve = noReserve
	}
	if err := r.ensureLogin(ctx); err != nil {
		return payload{}, err
	}
	if err := reserve(ctx, "grab"); err != nil {
		return payload{}, err
	}
	for attempt := 0; ; attempt++ {
		_, version := r.session()
		p, err := r.fetchOnce(ctx, link)
		if errors.Is(err, errSessionExpired) && attempt == 0 && r.def.Login != nil {
			if err := r.reauth(ctx, version); err != nil {
				return payload{}, err
			}
			continue
		}
		return p, err
	}
}

func (r *Runner) fetchOnce(ctx context.Context, link string) (payload, error) {
	u, err := r.resolve(link, nil)
	if err != nil {
		return payload{}, err
	}
	dl := r.def.Download
	if dl == nil {
		return r.getPayload(ctx, u, http.MethodGet, nil)
	}
	v := r.downloadVars(u)
	env := r.env(v)
	hdr := r.headers(r.def.Search.Headers, v)
	var before *response
	if dl.Before != nil {
		target, err := r.resolve(mustRender(dl.Before.Path, v), u)
		if err != nil {
			return payload{}, err
		}
		form, raw, err := r.renderInputs(dl.Before.Inputs, v, false)
		if err != nil {
			return payload{}, err
		}
		method := http.MethodGet
		if dl.Before.Method == "post" {
			method = http.MethodPost
		}
		if before, err = r.http.do(ctx, request{method: method, url: target.String(), form: form, raw: raw, headers: hdr, follow: true}); err != nil {
			return payload{}, err
		}
	}
	if dl.InfoHash == nil && len(dl.Selectors) == 0 {
		return r.getPayload(ctx, u, downloadMethod(dl.Method), hdr)
	}
	var page *response
	pageDoc := func(useBefore bool) (*goquery.Document, *url.URL, error) {
		resp := before
		if !useBefore || resp == nil {
			if page == nil {
				p, err := r.http.do(ctx, request{method: http.MethodGet, url: u.String(), headers: hdr, follow: true})
				if err != nil {
					return nil, nil, err
				}
				if p.redirected() && strings.HasPrefix(p.location, "magnet:") {
					p.body = []byte(p.location)
				}
				page = p
			}
			resp = page
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(r.http.text(resp)))
		return doc, resp.url, err
	}
	if ih := dl.InfoHash; ih != nil {
		doc, _, err := pageDoc(ih.UseBeforeResponse)
		if err != nil {
			return payload{}, err
		}
		hash, ok, err := htmlValue(ih.Hash, doc.Selection, env)
		if err != nil || !ok {
			return payload{}, errSessionExpired
		}
		h := newznab.NormalizeInfoHash(hash)
		if h == "" {
			return payload{}, fmt.Errorf("%s: %q is not an info hash", r.def.Name, abbreviate(hash))
		}
		title, _, _ := htmlValue(ih.Title, doc.Selection, env)
		return payload{magnet: newznab.MagnetFor(h, title, nil)}, nil
	}
	var lastErr error
	for _, sel := range dl.Selectors {
		doc, pageURL, err := pageDoc(sel.UseBeforeResponse)
		if err != nil {
			return payload{}, err
		}
		if page != nil && strings.HasPrefix(string(page.body), "magnet:") {
			return payload{magnet: string(page.body)}, nil
		}
		href, ok, err := htmlValue(sel, doc.Selection, env)
		if err != nil {
			lastErr = err
			continue
		}
		if !ok || strings.TrimSpace(href) == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(href), "magnet:") {
			return payload{magnet: href}, nil
		}
		target, err := r.resolve(href, pageURL)
		if err != nil {
			lastErr = err
			continue
		}
		p, err := r.getPayload(ctx, target, downloadMethod(dl.Method), hdr)
		if err == nil {
			return p, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return payload{}, lastErr
	}
	return payload{}, fmt.Errorf("%s: no download link was found on the release page: %w", r.def.Name, errSessionExpired)
}

func downloadMethod(m string) string {
	if m == "post" {
		return http.MethodPost
	}
	return http.MethodGet
}

// getPayload fetches a .torrent/.nzb document, stopping at magnet redirects.
func (r *Runner) getPayload(ctx context.Context, u *url.URL, method string, hdr http.Header) (payload, error) {
	target := *u
	req := request{method: method, url: target.String(), headers: hdr, follow: true}
	if method == http.MethodPost && target.RawQuery != "" {
		// Post the link's query as the form body.
		vals, _ := url.ParseQuery(target.RawQuery)
		target.RawQuery = ""
		req.url = target.String()
		for k, vs := range vals {
			for _, v := range vs {
				req.form = append(req.form, KV{Key: k, Value: v})
			}
		}
	}
	resp, err := r.http.do(ctx, req)
	if err != nil {
		return payload{}, err
	}
	if resp.redirected() {
		if strings.HasPrefix(strings.ToLower(resp.location), "magnet:") {
			return payload{magnet: resp.location}, nil
		}
		return payload{}, errSessionExpired
	}
	switch {
	case resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden:
		return payload{}, errSessionExpired
	case resp.status == http.StatusTooManyRequests:
		return payload{}, fmt.Errorf("%s: the site is rate limiting downloads (HTTP 429)", r.def.Name)
	case resp.status/100 != 2:
		return payload{}, fmt.Errorf("%s: download returned HTTP %d", r.def.Name, resp.status)
	}
	body := resp.body
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(bytes.ToLower(trimmed), []byte("magnet:")) {
		return payload{magnet: string(trimmed)}, nil
	}
	if len(trimmed) > 0 && trimmed[0] == '<' && !bytes.Contains(bytes.ToLower(trimmed[:min(len(trimmed), 2048)]), []byte("<nzb")) {
		return payload{}, errSessionExpired
	}
	return payload{body: body}, nil
}

// ResolveTorrent fills a torrent release's info hash and magnet,
// downloading its .torrent (one grab) when the result had only a link.
func (r *Runner) ResolveTorrent(ctx context.Context, rel core.Release, reserve Reserver) (core.Release, error) {
	if h := newznab.NormalizeInfoHash(rel.InfoHash); h != "" {
		rel.InfoHash = h
		if rel.MagnetURL == "" {
			rel.MagnetURL = newznab.MagnetFor(h, rel.Name, nil)
		}
		return rel, nil
	}
	if h := newznab.InfoHashFromMagnet(rel.MagnetURL); h != "" {
		rel.InfoHash = h
		return rel, nil
	}
	if rel.NZBURL == "" {
		return rel, fmt.Errorf("%s: torrent %q has no download link: %w", r.def.Name, rel.Name, core.ErrReleaseRejected)
	}
	p, err := r.fetch(ctx, rel.NZBURL, reserve)
	if err != nil {
		return rel, err
	}
	if p.magnet != "" {
		h := newznab.InfoHashFromMagnet(p.magnet)
		if h == "" {
			return rel, fmt.Errorf("%s: magnet for %q has no info hash: %w", r.def.Name, rel.Name, core.ErrReleaseRejected)
		}
		rel.InfoHash, rel.MagnetURL = h, p.magnet
		return rel, nil
	}
	meta, err := newznab.ParseTorrent(p.body)
	if err != nil {
		return rel, fmt.Errorf("%s: %q: %w", r.def.Name, rel.Name, err)
	}
	name := meta.Name
	if name == "" {
		name = rel.Name
	}
	rel.InfoHash = meta.InfoHash
	rel.MagnetURL = newznab.MagnetFor(meta.InfoHash, name, meta.Trackers)
	return rel, nil
}

// FetchNZB downloads a Usenet site's NZB (one grab).
func (r *Runner) FetchNZB(ctx context.Context, rel core.Release, reserve Reserver) ([]byte, error) {
	if rel.NZBURL == "" {
		return nil, fmt.Errorf("%s: release %q has no NZB link", r.def.Name, rel.Name)
	}
	p, err := r.fetch(ctx, rel.NZBURL, reserve)
	if err != nil {
		return nil, err
	}
	if p.magnet != "" || !bytes.Contains(bytes.ToLower(p.body[:min(len(p.body), 4096)]), []byte("<nzb")) {
		return nil, fmt.Errorf("%s: the download for %q is not an NZB", r.def.Name, rel.Name)
	}
	return p.body, nil
}

// TestResult is the outcome of an indexer test.
type TestResult struct {
	OK      bool     `json:"ok"`
	Results int      `json:"results"`
	Titles  []string `json:"titles,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Test signs in afresh (when the site needs it) and runs a sample search
// for the latest releases.
func (r *Runner) Test(ctx context.Context) (TestResult, error) {
	if r.def.Login != nil {
		if err := r.login(ctx, 0, true); err != nil {
			return TestResult{Error: err.Error()}, err
		}
	}
	rels, err := r.search(ctx, query{mode: "search", test: true}, noReserve)
	if err != nil {
		return TestResult{Error: err.Error()}, err
	}
	res := TestResult{OK: true, Results: len(rels)}
	for i := 0; i < len(rels) && i < 5; i++ {
		res.Titles = append(res.Titles, rels[i].Name)
	}
	return res, nil
}
