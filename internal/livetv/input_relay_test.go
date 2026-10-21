package livetv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// relaySecrets are planted in the provider URL and headers; none of them may
// reach a tool's argv, the transcoder log, or a relayed manifest.
var relaySecrets = []string{"xpass-S3CRET", "QTOKEN-S3CRET", "BEARER-S3CRET", "HDR-S3CRET", "ProviderUA"}

func relayAuth() *SourceAuth {
	return &SourceAuth{Type: "bearer", Token: "BEARER-S3CRET", UserAgent: "ProviderUA/1", Headers: map[string]string{"X-Provider-Key": "HDR-S3CRET"}}
}

// authenticated reports whether r carries every planted provider header.
func authenticated(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer BEARER-S3CRET" && r.Header.Get("X-Provider-Key") == "HDR-S3CRET" && r.Header.Get("User-Agent") == "ProviderUA/1"
}

func carriesProviderHeaders(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || r.Header.Get("X-Provider-Key") != "" || strings.Contains(r.Header.Get("User-Agent"), "ProviderUA")
}

// tuneRelayChannel imports a provider-authenticated source whose only input
// is streamURL, tunes it with transcoding (so ffmpeg reads the input), and
// returns the session.
func tuneRelayChannel(t *testing.T, s *Service, streamURL string) SessionInfo {
	t.Helper()
	ctx := context.Background()
	src := Source{ID: "provider", Name: "Provider", Auth: relayAuth(), Playlist: "#EXTM3U\n#EXTINF:-1 tvg-id=\"news\",News\n" + streamURL + "\n"}
	if _, err := s.ImportSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	channels, _ := s.Channels(ctx)
	c := channels[0]
	c.Transcode = true
	if _, err := s.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	info, err := s.Tune(ctx, &core.User{ID: 1}, c.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func relayGet(t *testing.T, raw string, header http.Header) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

func assertNoSecrets(t *testing.T, what, text string, hosts ...string) {
	t.Helper()
	for _, secret := range append(append([]string(nil), relaySecrets...), hosts...) {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%s exposes %q:\n%s", what, secret, text)
		}
	}
}

func hostOf(raw string) string {
	u, _ := url.Parse(raw)
	return u.Host
}

// argvRunner stands in for ffmpeg and ffprobe. It records every command line
// and reads each -i input over HTTP, as the tools would, without headers.
type argvRunner struct {
	mu     sync.Mutex
	calls  [][]string
	bodies []string
	procs  []*argvProc
}

const argvProbeJSON = `{"format":{"format_name":"mpegts"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1280,"height":720},{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000"}]}`

func (r *argvRunner) record(cmd transcode.Command) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{filepath.Base(cmd.Path)}, cmd.Args...))
	r.mu.Unlock()
	for i := 0; i+1 < len(cmd.Args); i++ {
		if cmd.Args[i] == "-i" && strings.HasPrefix(cmd.Args[i+1], "http") {
			req, _ := http.NewRequest(http.MethodGet, cmd.Args[i+1], nil)
			req.Header.Set("User-Agent", "Lavf/61")
			body := "unreachable"
			if res, err := http.DefaultClient.Do(req); err == nil {
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				body = fmt.Sprintf("%d %s", res.StatusCode, b)
			}
			r.mu.Lock()
			r.bodies = append(r.bodies, body)
			r.mu.Unlock()
		}
	}
}

func (r *argvRunner) Run(_ context.Context, cmd transcode.Command) error {
	r.record(cmd)
	w := func(s string) {
		if cmd.Stdout != nil {
			io.WriteString(cmd.Stdout, s)
		}
	}
	switch {
	case strings.Contains(filepath.Base(cmd.Path), "ffprobe"):
		w(argvProbeJSON)
	case contains(cmd.Args, "-version"):
		w("ffmpeg version 7.1-fixture\n")
	case contains(cmd.Args, "-encoders"):
		w("Encoders:\n ------\n V....D libx264              libx264 H.264 (codec h264)\n A....D aac                  AAC (Advanced Audio Coding)\n")
	case contains(cmd.Args, "-hwaccels"):
		w("Hardware acceleration methods:\n\n")
	case contains(cmd.Args, "-filters"):
		w("Filters:\n .. scale             V->V       Scale the input video size.\n")
	}
	return nil
}

func (r *argvRunner) Start(cmd transcode.Command) (transcode.Process, error) {
	r.record(cmd)
	p := &argvProc{done: make(chan struct{})}
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	return p, nil
}

func contains(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

type argvProc struct {
	once sync.Once
	done chan struct{}
}

func (p *argvProc) Done() <-chan struct{} { return p.done }
func (p *argvProc) Err() error            { <-p.done; return nil }
func (p *argvProc) Stop()                 { p.once.Do(func() { close(p.done) }) }
func (p *argvProc) Pause() error          { return nil }
func (p *argvProc) Resume() error         { return nil }

func TestFFmpegArgvCarriesNoProviderURLOrHeaders(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live/xuser/xpass-S3CRET/1001.ts" || r.URL.Query().Get("token") != "QTOKEN-S3CRET" || !authenticated(r) {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		io.WriteString(w, "TS-FIXTURE")
	}))
	defer up.Close()
	dir := t.TempDir()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &argvRunner{}
	var logs bytes.Buffer
	tc, err := transcode.New(config.Transcode{FFmpegPath: filepath.Join(dir, "ffmpeg"), FFprobePath: filepath.Join(dir, "ffprobe"), TempDir: filepath.Join(dir, "work")},
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), transcode.WithRunner(runner), transcode.WithoutHWDetection())
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	s := testService(t, Options{Transcoder: tc, HTTPClient: up.Client()})
	info := tuneRelayChannel(t, s, up.URL+"/live/xuser/xpass-S3CRET/1001.ts?token=QTOKEN-S3CRET")

	runner.mu.Lock()
	calls := append([][]string(nil), runner.calls...)
	bodies := append([]string(nil), runner.bodies...)
	runner.mu.Unlock()
	var inputs []string
	for _, args := range calls {
		assertNoSecrets(t, "command line", strings.Join(args, " "), hostOf(up.URL), "/live/", "Authorization", "X-Provider-Key")
		if contains(args, "-headers") || contains(args, "-referer") {
			t.Fatalf("provider headers on argv: %q", args)
		}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-i" {
				inputs = append(inputs, args[i+1])
			}
		}
	}
	// One ffprobe analysis and one ffmpeg relay, both reading the loopback grant.
	if len(inputs) != 2 || inputs[0] != inputs[1] || !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/livetv/input/[A-Za-z0-9_-]{43}/0/stream\.ts$`).MatchString(inputs[0]) {
		t.Fatalf("tool inputs = %q", inputs)
	}
	if len(bodies) != 2 || bodies[0] != "200 TS-FIXTURE" || bodies[1] != "200 TS-FIXTURE" || hits.Load() < 3 {
		t.Fatalf("tools did not read the authenticated upstream: %q hits=%d", bodies, hits.Load())
	}
	assertNoSecrets(t, "transcoder log", logs.String(), hostOf(up.URL))

	if err := s.Stop(context.Background(), info.ID, &core.User{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := relayGet(t, inputs[0], nil); code != http.StatusNotFound {
		t.Fatalf("grant outlived its session: %d", code)
	}
	runner.mu.Lock()
	for _, p := range runner.procs {
		select {
		case <-p.done:
		default:
			t.Error("ffmpeg survived session stop")
		}
	}
	runner.mu.Unlock()
}

var playlistURI = regexp.MustCompile(`URI="([^"]+)"`)

func playlistURIs(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
		for _, m := range playlistURI.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func TestRelayHLSChainScopesProviderHeadersToSourceOrigin(t *testing.T) {
	var cdnSaw atomic.Value
	cdnSaw.Store("")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if carriesProviderHeaders(r) {
			t.Errorf("CDN %s received provider headers", r.URL.Path)
		}
		cdnSaw.Store(cdnSaw.Load().(string) + r.URL.Path + "|" + r.Header.Get("User-Agent") + " ")
		io.WriteString(w, "CDN-MEDIA")
	}))
	defer cdn.Close()
	var originRange atomic.Value
	originRange.Store("")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/live/u/xpass-S3CRET/master.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"audio.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1000,AUDIO=\"a\"\nvariant/index.m3u8?QTOKEN-S3CRET\n")
		case "/live/u/xpass-S3CRET/audio.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\n"+cdn.URL+"/audio1.aac\n")
		case "/live/u/xpass-S3CRET/variant/index.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-KEY:METHOD=AES-128,URI=\"../key.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-BYTERANGE:4@0\n#EXTINF:4,\nseg1.ts\n#EXTINF:4,\n"+cdn.URL+"/seg2.ts?sig=cdn\n#EXTINF:4,\n/redirect.ts\n")
		case "/live/u/xpass-S3CRET/key.bin":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl") // mislabelled key bytes
			io.WriteString(w, "0123456789abcdef")
		case "/live/u/xpass-S3CRET/variant/init.mp4":
			io.WriteString(w, "INIT")
		case "/live/u/xpass-S3CRET/variant/seg1.ts":
			originRange.Store(r.Header.Get("Range"))
			http.ServeContent(w, r, "seg1.ts", time.Time{}, strings.NewReader("SEG1-BYTES"))
		case "/redirect.ts":
			http.Redirect(w, r, cdn.URL+"/seg3.ts", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: origin.Client()})
	tuneRelayChannel(t, s, origin.URL+"/live/u/xpass-S3CRET/master.m3u8?token=QTOKEN-S3CRET")
	if len(tc.requests) != 1 || len(tc.requests[0].InputHeaders) != 0 {
		t.Fatalf("relay request = %+v", tc.requests)
	}
	entry := tc.requests[0].InputURL
	assertNoSecrets(t, "ffmpeg input", entry, hostOf(origin.URL))
	if !strings.HasSuffix(entry, "/0/stream.m3u8") {
		t.Fatalf("entry %q lost the extension ffmpeg uses to accept HLS", entry)
	}

	ffmpeg := http.Header{"User-Agent": {"Lavf/61"}, "Range": {"bytes=0-3"}}
	fetched := map[string]string{}
	queue := []string{entry}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if _, seen := fetched[next]; seen {
			continue
		}
		code, header, body := relayGet(t, next, ffmpeg)
		if code != http.StatusOK && code != http.StatusPartialContent {
			t.Fatalf("relay %s = %d %q", next, code, body)
		}
		if header.Get("Location") != "" {
			t.Fatal("relay published a Location header")
		}
		fetched[next] = body
		if strings.HasPrefix(body, "#EXTM3U") {
			assertNoSecrets(t, "relayed playlist", body, hostOf(origin.URL), hostOf(cdn.URL))
			for _, u := range playlistURIs(body) {
				if !strings.HasPrefix(u, "http://"+hostOf(entry)+relayPathPrefix) {
					t.Fatalf("playlist URI %q bypasses the relay", u)
				}
				queue = append(queue, u)
			}
		}
	}
	var media []string
	for _, body := range fetched {
		if !strings.HasPrefix(body, "#EXTM3U") {
			media = append(media, body)
		}
	}
	all := strings.Join(media, ",")
	for _, want := range []string{"0123456789abcdef", "INIT", "SEG1", "CDN-MEDIA"} {
		if !strings.Contains(all, want) {
			t.Fatalf("relay chain missed %q: %q", want, all)
		}
	}
	if originRange.Load() != "bytes=0-3" {
		t.Fatalf("byte range not forwarded: %q", originRange.Load())
	}
	saw := cdnSaw.Load().(string)
	for _, want := range []string{"/audio1.aac|Lavf/61", "/seg2.ts|Lavf/61", "/seg3.ts|Lavf/61"} {
		if !strings.Contains(saw, want) {
			t.Fatalf("CDN requests %q missing %q", saw, want)
		}
	}
}

func TestRelayRejectsUnauthorizedAndUngrantedRequests(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "TS")
	}))
	defer up.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: up.Client()})
	info := tuneRelayChannel(t, s, up.URL+"/live.ts")
	entry := tc.requests[0].InputURL
	if code, _, body := relayGet(t, entry, nil); code != 200 || body != "TS" {
		t.Fatalf("granted input = %d %q", code, body)
	}
	u, _ := url.Parse(entry)
	token := strings.Split(strings.TrimPrefix(u.Path, relayPathPrefix), "/")[0]
	base := "http://" + u.Host + relayPathPrefix
	for _, raw := range []string{
		base + strings.Repeat("A", len(token)) + "/0/stream.ts", // wrong token
		base + token + "/0/stream.m3u8",                         // wrong name
		base + token + "/0/stream.ts/extra",                     // extra path
		base + token + "/ff/media.ts",                           // ungranted resource
		base + token + "/0",
		"http://" + u.Host + "/" + url.PathEscape(up.URL), // not a proxy
	} {
		if code, _, _ := relayGet(t, raw, nil); code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", raw, code)
		}
	}
	res, err := http.Post(entry, "text/plain", strings.NewReader("x"))
	if err != nil || res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST accepted: %v", err)
	}
	res.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, entry, nil)
	req.Host = "localhost"
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host accepted: %v", err)
	} else {
		res.Body.Close()
	}
	direct := httptest.NewRequest(http.MethodGet, u.Path, nil)
	direct.RemoteAddr, direct.Host = "192.0.2.10:5000", u.Host
	rec := httptest.NewRecorder()
	s.serveInputRelay(rec, direct)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback peer = %d", rec.Code)
	}

	s.Stop(context.Background(), info.ID, &core.User{ID: 1})
	if code, _, _ := relayGet(t, entry, nil); code != http.StatusNotFound {
		t.Fatalf("revoked grant = %d", code)
	}
	s.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		res, err := http.Get(entry)
		if err != nil {
			break
		}
		res.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("relay listener outlived the service")
		}
	}
}

func TestRelayStopCancelsInFlightUpstream(t *testing.T) {
	upstreamDone := make(chan struct{})
	var once sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=7-" {
			io.WriteString(w, "FIRST")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			once.Do(func() { close(upstreamDone) })
			return
		}
		io.WriteString(w, "TS")
	}))
	defer up.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: up.Client()})
	info := tuneRelayChannel(t, s, up.URL+"/live.ts")
	req, _ := http.NewRequest(http.MethodGet, tc.requests[0].InputURL, nil)
	req.Header.Set("Range", "bytes=7-")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	began := time.Now()
	first := make([]byte, 5)
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != "FIRST" || time.Since(began) > 2*time.Second {
		t.Fatalf("stream start %q %v", first, err)
	}
	ended := make(chan struct{})
	go func() {
		io.Copy(io.Discard, res.Body)
		close(ended)
	}()
	s.Stop(context.Background(), info.ID, &core.User{ID: 1})
	for name, ch := range map[string]chan struct{}{"upstream request": upstreamDone, "relay response": ended} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s survived session stop", name)
		}
	}
}

func TestRelayBoundsUpstreamIdleTime(t *testing.T) {
	old := relayIOTimeout
	relayIOTimeout = 200 * time.Millisecond
	defer func() { relayIOTimeout = old }()
	release := make(chan struct{})
	defer close(release)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Range") {
		case "bytes=1-":
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "bytes=2-":
			io.WriteString(w, "PARTIAL")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		default:
			io.WriteString(w, "TS")
		}
	}))
	defer up.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: up.Client()})
	tuneRelayChannel(t, s, up.URL+"/live.ts")
	entry := tc.requests[0].InputURL
	start := time.Now()
	if code, _, _ := relayGet(t, entry, http.Header{"Range": {"bytes=1-"}}); code != http.StatusGatewayTimeout {
		t.Fatalf("stalled headers = %d", code)
	}
	if _, _, body := relayGet(t, entry, http.Header{"Range": {"bytes=2-"}}); body != "PARTIAL" {
		t.Fatalf("stalled body = %q", body)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("idle upstream was not bounded")
	}
}

func TestRelayErrorsCarryNoUpstreamDetail(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Range") {
		case "bytes=3-":
			w.Header().Set("Location", "https://xuser:xpass-S3CRET@upstream.invalid/")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "account xpass-S3CRET QTOKEN-S3CRET")
		case "bytes=4-":
			http.Redirect(w, r, "http://127.0.0.1:1/xpass-S3CRET", http.StatusFound)
		default:
			io.WriteString(w, "TS")
		}
	}))
	defer up.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: up.Client()})
	tuneRelayChannel(t, s, up.URL+"/live/xuser/xpass-S3CRET/1.ts?token=QTOKEN-S3CRET")
	entry := tc.requests[0].InputURL
	for rng, want := range map[string]int{"bytes=3-": http.StatusForbidden, "bytes=4-": http.StatusBadGateway} {
		code, header, body := relayGet(t, entry, http.Header{"Range": {rng}})
		if code != want || header.Get("Location") != "" {
			t.Fatalf("%s = %d %v", rng, code, header)
		}
		assertNoSecrets(t, rng+" response", body, hostOf(up.URL), "upstream.invalid", "127.0.0.1:1")
	}
}

func TestRelayRefusesHTTPSDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("plaintext upstream reached: %s", r.URL.Path)
		io.WriteString(w, "PLAIN")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/live.m3u8" && r.Header.Get("X-Case") == "playlist":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\n"+plain.URL+"/seg.ts\n")
		case r.URL.Path == "/live.m3u8" && r.Header.Get("X-Case") == "redirect":
			http.Redirect(w, r, plain.URL+"/seg.ts", http.StatusFound)
		default:
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nseg.ts\n")
		}
	}))
	defer secure.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: secure.Client()})
	tuneRelayChannel(t, s, secure.URL+"/live.m3u8")
	entry := tc.requests[0].InputURL
	for _, c := range []string{"playlist", "redirect"} {
		// The relay forwards only Range from ffmpeg, so select the case by
		// rewriting the grant's own header set.
		s.inputRelay.mu.Lock()
		for _, g := range s.inputRelay.grants {
			g.headers.Set("X-Case", c)
		}
		s.inputRelay.mu.Unlock()
		code, _, body := relayGet(t, entry, nil)
		if code != http.StatusBadGateway {
			t.Fatalf("%s downgrade = %d %q", c, code, body)
		}
		assertNoSecrets(t, c+" response", body, hostOf(plain.URL), hostOf(secure.URL))
	}
}

const relayMPD = `<?xml version="1.0" encoding="UTF-8"?>
<!-- packager note: account xpass-S3CRET -->
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic" minimumUpdatePeriod="PT2S">
  <Location>ORIGIN/dash/u/xpass-S3CRET/manifest.mpd?token=QTOKEN-S3CRET</Location>
  <UTCTiming schemeIdUri="urn:mpeg:dash:utc:http-iso:2014" value="ORIGIN/time?token=QTOKEN-S3CRET"/>
  <Period id="1">
    <BaseURL>media/</BaseURL>
    <AdaptationSet mimeType="video/mp4">
      <Role schemeIdUri="urn:mpeg:dash:role:2011" value="main"/>
      <SegmentTemplate timescale="1" duration="4" startNumber="1" initialization="$RepresentationID$/init.mp4?token=QTOKEN-S3CRET" media="$RepresentationID$/seg-$Number%05d$.m4s?token=QTOKEN-S3CRET&amp;b=$Bandwidth$"/>
      <Representation id="v1" bandwidth="1000" codecs="avc1.64001f"/>
      <Representation id="v2" bandwidth="2000"/>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4">
      <Representation id="a1" bandwidth="128">
        <BaseURL>CDN/audio/</BaseURL>
        <SegmentList duration="4">
          <Initialization sourceURL="init.mp4"/>
          <SegmentURL media="s1.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestRelayRewritesDASHWithManifestBoundTemplates(t *testing.T) {
	var cdnPaths atomic.Value
	cdnPaths.Store("")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if carriesProviderHeaders(r) {
			t.Errorf("CDN %s received provider headers", r.URL.Path)
		}
		cdnPaths.Store(cdnPaths.Load().(string) + r.URL.Path + " ")
		io.WriteString(w, "CDN-"+r.URL.Path)
	}))
	defer cdn.Close()
	var originURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/dash/u/xpass-S3CRET/manifest.mpd" {
			w.Header().Set("Content-Type", "application/dash+xml")
			io.WriteString(w, strings.NewReplacer("ORIGIN", originURL, "CDN", cdn.URL).Replace(relayMPD))
			return
		}
		io.WriteString(w, "ORIGIN-"+r.URL.Path+"?"+r.URL.RawQuery)
	}))
	defer origin.Close()
	originURL = origin.URL
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: origin.Client()})
	tuneRelayChannel(t, s, origin.URL+"/dash/u/xpass-S3CRET/manifest.mpd?token=QTOKEN-S3CRET")
	entry := tc.requests[0].InputURL
	if !strings.HasSuffix(entry, "/0/stream.mpd") {
		t.Fatalf("entry = %q", entry)
	}
	code, header, mpd := relayGet(t, entry, nil)
	if code != 200 || header.Get("Content-Type") != "application/dash+xml" {
		t.Fatalf("manifest = %d %q", code, mpd)
	}
	assertNoSecrets(t, "relayed MPD", mpd, hostOf(origin.URL), hostOf(cdn.URL), "<Location", "<UTCTiming", "<!--")
	attr := func(name string) string {
		m := regexp.MustCompile(name + `="([^"]+)"`).FindStringSubmatch(mpd)
		if m == nil {
			t.Fatalf("relayed MPD has no %s:\n%s", name, mpd)
		}
		return strings.ReplaceAll(m[1], "&amp;", "&")
	}
	media, init := attr("media"), attr("initialization")
	if !strings.HasSuffix(media, "/$RepresentationID$/$Number%05d$/$Bandwidth$/media.m4s") {
		t.Fatalf("template = %q", media)
	}
	fill := func(tmpl, rep, number, bw string) string {
		return strings.NewReplacer("$RepresentationID$", rep, "$Number%05d$", number, "$Bandwidth$", bw).Replace(tmpl)
	}
	if code, _, body := relayGet(t, fill(media, "v1", "00042", "1000"), nil); code != 200 || body != "ORIGIN-/dash/u/xpass-S3CRET/media/v1/seg-00042.m4s?token=QTOKEN-S3CRET&b=1000" {
		t.Fatalf("templated segment = %d %q", code, body)
	}
	if code, _, body := relayGet(t, fill(init, "v2", "", ""), nil); code != 200 || body != "ORIGIN-/dash/u/xpass-S3CRET/media/v2/init.mp4?token=QTOKEN-S3CRET" {
		t.Fatalf("templated init = %d %q", code, body)
	}
	// Template capabilities accept only the manifest's own identifiers.
	for _, bad := range []string{
		fill(media, "v9", "00042", "1000"),
		fill(media, "v1", "4x", "1000"),
		fill(media, "v1", "42", "1000"), // narrower than %05d
		fill(media, "v1", "00042", "999"),
		fill(media, "..", "00042", "1000"),
		fill(media, "v1%2F..%2F..", "00042", "1000"),
	} {
		if code, _, _ := relayGet(t, bad, nil); code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", bad, code)
		}
	}
	list := attr("SegmentURL media")
	if code, _, body := relayGet(t, list, nil); code != 200 || body != "CDN-/audio/s1.m4s" {
		t.Fatalf("segment list = %d %q", code, body)
	}
	if code, _, body := relayGet(t, attr("sourceURL"), nil); code != 200 || body != "CDN-/audio/init.mp4" {
		t.Fatalf("segment list init = %d %q", code, body)
	}
	for _, m := range regexp.MustCompile(`<BaseURL>([^<]+)</BaseURL>`).FindAllStringSubmatch(mpd, -1) {
		if !strings.HasPrefix(m[1], "http://"+hostOf(entry)+relayPathPrefix) {
			t.Fatalf("BaseURL %q bypasses the relay", m[1])
		}
	}
}

func TestRewriteMPDSupportedAndUnsupportedForms(t *testing.T) {
	base, _ := url.Parse("https://origin.invalid/live/manifest.mpd")
	var exacts, templates []string
	exact := func(raw string) (string, error) { exacts = append(exacts, raw); return "http://relay/e", nil }
	template := func(raw string, vars []mpdVar, _ *mpdScope) (string, error) {
		templates = append(templates, raw)
		return "http://relay/t", nil
	}
	ok := `<MPD><Period><AdaptationSet><SegmentTemplate media="https://cdn.invalid/$RepresentationID$/$Time$.m4s" initialization="https://cdn.invalid/$$init.mp4"/>` +
		`<Representation id="r1" bandwidth="1"><BaseURL>r1/</BaseURL></Representation></AdaptationSet></Period></MPD>`
	out, err := rewriteMPD([]byte(ok), base, exact, template)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 || templates[0] != "https://cdn.invalid/"+placeholder(0)+"/"+placeholder(1)+".m4s" {
		t.Fatalf("templates = %q", templates)
	}
	if want := []string{"https://cdn.invalid/$init.mp4", "https://origin.invalid/live/r1/"}; fmt.Sprint(exacts) != fmt.Sprint(want) {
		t.Fatalf("exact URLs = %q", exacts)
	}
	if strings.Contains(string(out), "invalid") {
		t.Fatalf("rewritten MPD kept upstream URLs: %s", out)
	}
	for name, doc := range map[string]string{
		"xlink":               `<MPD xmlns:xlink="http://www.w3.org/1999/xlink"><Period xlink:href="https://evil.invalid/p.xml"/></MPD>`,
		"unknown identifier":  `<MPD><Period><SegmentTemplate media="$Foo$.m4s"/></Period></MPD>`,
		"formatted rep id":    `<MPD><Period><SegmentTemplate media="$RepresentationID%05d$.m4s"/></Period></MPD>`,
		"unterminated":        `<MPD><Period><SegmentTemplate media="$Number.m4s"/></Period></MPD>`,
		"inherited relative":  `<MPD><Period><AdaptationSet><SegmentTemplate media="$Number$.m4s"/><Representation id="a"><BaseURL>a/</BaseURL></Representation></AdaptationSet></Period></MPD>`,
		"late BaseURL":        `<MPD><Period><SegmentTemplate media="$Number$.m4s"/><BaseURL>x/</BaseURL></Period></MPD>`,
		"doctype":             `<!DOCTYPE MPD [<!ENTITY x "y">]><MPD/>`,
		"not an MPD":          `<?xml version="1.0"?><html/>`,
		"non-UTF-8":           `<?xml version="1.0" encoding="ISO-8859-1"?><MPD/>`,
		"unbalanced":          `<MPD><Period></MPD>`,
		"placeholder smuggle": `<MPD><Period><SegmentTemplate media="ethervar0q/$Number$.m4s"/></Period></MPD>`,
	} {
		if _, err := rewriteMPD([]byte(doc), base, exact, template); !errors.Is(err, errRelayUnsupported) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRelayResourcesStayBounded(t *testing.T) {
	g := &inputGrant{token: "t", resources: map[string]*relayResource{"0": {name: "stream"}}, byKey: map[string]string{}}
	first := g.relayURL("relay", "https://origin.invalid/seg0.ts")
	for i := 1; i <= relayResourceLimit+10; i++ {
		g.relayURL("relay", fmt.Sprintf("https://origin.invalid/seg%d.ts", i))
	}
	if len(g.resources) != relayResourceLimit+1 || len(g.byKey) != relayResourceLimit || g.resources["0"] == nil {
		t.Fatalf("resources=%d keys=%d", len(g.resources), len(g.byKey))
	}
	if again := g.relayURL("relay", "https://origin.invalid/seg0.ts"); again == first {
		t.Fatal("evicted resource kept its capability")
	}
}
