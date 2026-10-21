package livetv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// The input relay hands ffmpeg and ffprobe a loopback URL for a session's
// selected input instead of the provider URL and headers. Those would
// otherwise sit on the tools' argv, which every local user can read through
// ps, and in their logs.
//
// Each tune attempt gets one grant: an unguessable token bound to that
// stream source. A grant serves the source URL itself and only the
// resources named by the HLS playlists and DASH manifests it served; it is
// not a proxy for anything else. Provider headers go only to the source's
// own origin, and nothing fetched over HTTPS may continue over HTTP.
const (
	relayPathPrefix    = "/livetv/input/"
	relayManifestLimit = 4 << 20
	relayResourceLimit = 8192
	relayGrantLimit    = 1024
	relayConcurrency   = 8
)

// relayIOTimeout matches the -rw_timeout ffmpeg applies to HTTP inputs: the
// longest wait for response headers or for one upstream read.
var relayIOTimeout = 30 * time.Second

var (
	errRelayDowngrade   = errors.New("livetv input relay: HTTPS downgrade")
	errRelayUnsupported = errors.New("livetv input relay: unsupported manifest")
	relayExt            = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
)

type inputRelay struct {
	mu     sync.Mutex
	host   string // 127.0.0.1:port; "" until first use
	grants map[[sha256.Size]byte]*inputGrant
	bySess map[*session]*inputGrant
}

type inputGrant struct {
	token   string
	origin  string      // the only origin that receives headers
	headers http.Header // provider headers; immutable
	ctx     context.Context
	cancel  context.CancelFunc
	slots   chan struct{}

	mu        sync.Mutex
	resources map[string]*relayResource
	byKey     map[string]string
	order     []string
	next      int
}

// relayResource is an exact upstream URL, or a DASH SegmentTemplate whose
// identifiers are filled from the request path.
type relayResource struct {
	key   string // registration key, for eviction
	name  string
	url   string
	vars  []mpdVar
	scope *mpdScope
}

func relayOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func relayName(stem, p string) string {
	if ext := strings.ToLower(path.Ext(p)); relayExt.MatchString(ext) {
		return stem + ext
	}
	return stem
}

func tokenKey(token string) [sha256.Size]byte { return sha256.Sum256([]byte(token)) }

// grantInput replaces the session's grant with one for src. The token is
// the only credential; it reaches ffmpeg as a path component.
func (s *Service) grantInput(ss *session, src StreamSource, headers http.Header) error {
	u, err := url.Parse(src.URL)
	if err != nil || !validHTTP(src.URL) {
		return core.ErrNotAvailable
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	g := &inputGrant{
		token: base64.RawURLEncoding.EncodeToString(b[:]), origin: relayOrigin(u), headers: headers.Clone(),
		ctx: ctx, cancel: cancel, slots: make(chan struct{}, relayConcurrency),
		resources: map[string]*relayResource{"0": {name: relayName("stream", u.Path), url: src.URL}},
		byKey:     map[string]string{},
	}
	r := &s.inputRelay
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.bySess[ss]; old != nil {
		r.dropLocked(ss, old)
	}
	if err := s.startRelayLocked(); err != nil {
		cancel()
		return err
	}
	if len(r.grants) >= relayGrantLimit {
		cancel()
		return core.ErrNotAvailable
	}
	r.grants[tokenKey(g.token)] = g
	r.bySess[ss] = g
	return nil
}

// revokeInput ends the session's grant and any relay request using it.
func (s *Service) revokeInput(ss *session) {
	r := &s.inputRelay
	r.mu.Lock()
	if g := r.bySess[ss]; g != nil {
		r.dropLocked(ss, g)
	}
	r.mu.Unlock()
}

func (r *inputRelay) dropLocked(ss *session, g *inputGrant) {
	g.cancel()
	delete(r.grants, tokenKey(g.token))
	delete(r.bySess, ss)
}

// relayInput is the URL ffmpeg and ffprobe read the session's input from.
func (s *Service) relayInput(ss *session) (string, error) {
	r := &s.inputRelay
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.bySess[ss]
	if g == nil || g.ctx.Err() != nil {
		return "", core.ErrNotAvailable
	}
	return "http://" + r.host + relayPathPrefix + g.token + "/0/" + g.resources["0"].name, nil
}

func (s *Service) startRelayLocked() error {
	r := &s.inputRelay
	if s.ctx.Err() != nil {
		return core.ErrNotAvailable
	}
	if r.host != "" {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("livetv input relay: %w", err)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(s.serveInputRelay),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		// net/http's own messages can quote request paths, which hold tokens.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	r.host = ln.Addr().String()
	r.grants, r.bySess = map[[sha256.Size]byte]*inputGrant{}, map[*session]*inputGrant{}
	go srv.Serve(ln)
	go func() {
		<-s.ctx.Done()
		srv.Close()
	}()
	return nil
}

// relayURL registers an exact upstream URL with the grant.
func (g *inputGrant) relayURL(host, raw string) string {
	u, _ := url.Parse(raw)
	return g.register(host, "e\x00"+raw, &relayResource{name: relayName("media", u.Path), url: raw})
}

func (g *inputGrant) register(host, key string, res *relayResource) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.byKey[key]
	if !ok {
		g.next++
		id = fmt.Sprintf("%x", g.next)
		g.byKey[key] = id
		g.order = append(g.order, id)
		// Live manifests keep naming new segments; forget the oldest.
		for len(g.order) > relayResourceLimit {
			old := g.order[0]
			g.order = g.order[1:]
			delete(g.byKey, g.resources[old].key)
			delete(g.resources, old)
		}
	}
	res.key = key
	g.resources[id] = res
	tail := res.name
	if len(res.vars) > 0 {
		parts := make([]string, 0, len(res.vars)+1)
		for _, v := range res.vars {
			parts = append(parts, "$"+v.raw+"$")
		}
		tail = strings.Join(append(parts, res.name), "/")
	}
	return "http://" + host + relayPathPrefix + g.token + "/" + id + "/" + tail
}

// lookup maps a request path to its grant and upstream URL.
func (r *inputRelay) lookup(p string) (*inputGrant, string, bool) {
	parts := strings.Split(strings.TrimPrefix(p, relayPathPrefix), "/")
	if !strings.HasPrefix(p, relayPathPrefix) || len(parts) < 3 {
		return nil, "", false
	}
	r.mu.Lock()
	g := r.grants[tokenKey(parts[0])]
	r.mu.Unlock()
	if g == nil || g.ctx.Err() != nil {
		return nil, "", false
	}
	g.mu.Lock()
	res := g.resources[parts[1]]
	g.mu.Unlock()
	tail := parts[2:]
	if res == nil || len(tail) != len(res.vars)+1 || tail[len(tail)-1] != res.name {
		return nil, "", false
	}
	if len(res.vars) == 0 {
		return g, res.url, true
	}
	raw, ok := res.fill(tail[:len(tail)-1])
	return g, raw, ok
}

func (s *Service) serveInputRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	s.inputRelay.mu.Lock()
	want := s.inputRelay.host
	s.inputRelay.mu.Unlock()
	if ip == nil || !ip.IsLoopback() || r.Host != want {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	g, raw, ok := s.inputRelay.lookup(r.URL.EscapedPath())
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(g.ctx, cancel)()
	res, err := s.relayFetch(ctx, cancel, g, raw, r)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		}
		http.Error(w, "upstream unavailable", code)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		code := res.StatusCode
		if code < 400 || code > 599 {
			code = http.StatusBadGateway
		}
		http.Error(w, "upstream unavailable", code)
		return
	}
	idle := time.AfterFunc(relayIOTimeout, cancel)
	defer idle.Stop()
	body := bufio.NewReaderSize(&idleReader{r: res.Body, t: idle}, 32<<10)
	head := sniffHead(body)
	ctype := strings.ToLower(res.Header.Get("Content-Type"))
	hls := bytes.HasPrefix(head, []byte("#EXTM3U"))
	mpd := bytes.HasPrefix(head, []byte("<?xml")) || bytes.HasPrefix(head, []byte("<MPD")) ||
		(bytes.HasPrefix(head, []byte("<")) && strings.Contains(ctype, "xml"))
	if hls || mpd {
		s.serveRelayManifest(w, g, res.Request.URL, body, hls)
		return
	}
	for _, name := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := res.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	if mt, params, err := mime.ParseMediaType(res.Header.Get("Content-Type")); err == nil {
		w.Header().Set("Content-Type", mime.FormatMediaType(mt, params))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

func (s *Service) serveRelayManifest(w http.ResponseWriter, g *inputGrant, base *url.URL, body io.Reader, hls bool) {
	b, err := io.ReadAll(io.LimitReader(body, relayManifestLimit+1))
	if err != nil || len(b) > relayManifestLimit {
		http.Error(w, "upstream manifest unavailable", http.StatusBadGateway)
		return
	}
	s.inputRelay.mu.Lock()
	host := s.inputRelay.host
	s.inputRelay.mu.Unlock()
	exact := func(raw string) (string, error) {
		u, err := url.Parse(raw)
		if err != nil || !validHTTP(raw) {
			return "", errRelayUnsupported
		}
		if base.Scheme == "https" && u.Scheme != "https" {
			return "", errRelayDowngrade
		}
		return g.relayURL(host, raw), nil
	}
	var out []byte
	ctype := "application/dash+xml"
	if hls {
		ctype = "application/vnd.apple.mpegurl"
		var rerr error
		out, err = rewriteHLS(b, base, func(raw string) string {
			u, err := exact(raw)
			if err != nil {
				rerr = err
			}
			return u
		})
		if err == nil {
			err = rerr
		}
	} else {
		out, err = rewriteMPD(b, base, exact, func(tmpl string, vars []mpdVar, scope *mpdScope) (string, error) {
			u, err := url.Parse(tmpl)
			if err != nil || !validHTTP(tmpl) {
				return "", errRelayUnsupported
			}
			if base.Scheme == "https" && u.Scheme != "https" {
				return "", errRelayDowngrade
			}
			return g.register(host, "t\x00"+tmpl, &relayResource{name: relayName("media", u.Path), url: tmpl, vars: vars, scope: scope}), nil
		})
	}
	if err != nil {
		http.Error(w, "unsupported upstream manifest", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", fmt.Sprint(len(out)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

// relayFetch fetches one upstream object. Headers wait at most
// relayIOTimeout; the body is then bounded by the caller's idle reader.
// Errors never carry the upstream URL to the caller's response.
func (s *Service) relayFetch(ctx context.Context, cancel context.CancelFunc, g *inputGrant, raw string, in *http.Request) (*http.Response, error) {
	client := *s.o.HTTPClient
	client.Timeout = 0 // a live input is endless; idle time is bounded instead
	checkRedirect := s.o.HTTPClient.CheckRedirect
	ua := in.Header.Get("User-Agent")
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if checkRedirect != nil {
			if err := checkRedirect(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errRelayDowngrade
		}
		if relayOrigin(req.URL) != g.origin {
			for name := range g.headers {
				req.Header.Del(name)
			}
			if ua != "" && req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", ua)
			}
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errRelayUnsupported
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, errRelayUnsupported
		}
		if relayOrigin(u) == g.origin {
			req.Header = g.headers.Clone()
		}
		if req.Header.Get("User-Agent") == "" && ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		for _, name := range []string{"Range", "If-Range"} {
			if v := in.Header.Get(name); v != "" {
				req.Header.Set(name, v)
			}
		}
		var timedOut atomic.Bool
		t := time.AfterFunc(relayIOTimeout, func() { timedOut.Store(true); cancel() })
		res, err := client.Do(req)
		t.Stop()
		if timedOut.Load() {
			if res != nil {
				res.Body.Close()
			}
			return nil, context.DeadlineExceeded
		}
		if err == nil && res.StatusCode < 500 && res.StatusCode != 429 {
			return res, nil
		}
		if res != nil {
			res.Body.Close()
		}
		if ctx.Err() != nil || errors.Is(err, errRelayDowngrade) {
			return nil, core.ErrNotAvailable
		}
		last = core.ErrNotAvailable
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, core.ErrNotAvailable
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
		}
	}
	return nil, last
}

// sniffHead returns the start of a body after any BOM and whitespace, reading
// only until it can tell a manifest from media: a slow live stream must not
// wait for a fixed-size prefix.
func sniffHead(br *bufio.Reader) []byte {
	bom := []byte("\xef\xbb\xbf")
	for n := 1; ; n++ {
		b, err := br.Peek(n)
		if err != nil || n >= 512 {
			return bytes.TrimLeft(bytes.TrimPrefix(b, bom), " \t\r\n")
		}
		if bytes.HasPrefix(bom, b) {
			continue
		}
		t := bytes.TrimLeft(bytes.TrimPrefix(b, bom), " \t\r\n")
		if len(t) >= 7 || len(t) > 0 && t[0] != '#' && t[0] != '<' {
			return t
		}
	}
}

// idleReader cancels the upstream request when one read waits longer than
// relayIOTimeout. Time spent writing to a slow reader does not count.
type idleReader struct {
	r io.Reader
	t *time.Timer
}

func (r *idleReader) Read(p []byte) (int, error) {
	r.t.Reset(relayIOTimeout)
	n, err := r.r.Read(p)
	r.t.Stop()
	return n, err
}
