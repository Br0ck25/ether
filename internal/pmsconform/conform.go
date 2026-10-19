// Package pmsconform captures and compares the HTTP surface exposed by Plex Media Servers.
package pmsconform

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Client describes a Plex application header profile.
type Client struct {
	Name    string
	Headers http.Header
}

// Clients returns representative Plex Web, iOS and Apple TV request profiles.
func Clients() map[string]Client {
	return map[string]Client{
		"web":     {Name: "Plex Web", Headers: headers("Plex Web", "Chrome", "Windows", "Plex Web", "1.0")},
		"ios":     {Name: "Plex for iOS", Headers: headers("Plex for iOS", "iPhone", "iOS", "iPhone", "8.0")},
		"appletv": {Name: "Plex for Apple TV", Headers: headers("Plex for Apple TV", "Apple TV", "tvOS", "Apple TV", "8.0")},
	}
}
func headers(product, platform, osName, device, version string) http.Header {
	h := http.Header{}
	h.Set("X-Plex-Product", product)
	h.Set("X-Plex-Version", version)
	h.Set("X-Plex-Platform", platform)
	h.Set("X-Plex-Platform-Version", osName)
	h.Set("X-Plex-Device", device)
	h.Set("X-Plex-Device-Name", device)
	h.Set("X-Plex-Client-Identifier", "pmsdiff-client")
	h.Set("X-Plex-Provides", "player,controller")
	h.Set("X-Plex-Token", "{{token}}")
	return h
}

// Request is one logical request in the PMS probe corpus.
type Request struct {
	Name, Method, Path string
	Headers            map[string]http.Header
}

// Corpus returns probes used when a Plex application connects and browses a server.
func Corpus() []Request {
	paths := []string{"/", "/identity", "/library/sections", "/library/sections/{{section}}/all?X-Plex-Container-Start=0&X-Plex-Container-Size=50", "/library/metadata/{{ratingKey}}", "/library/metadata/{{ratingKey}}/children", "/library/metadata/{{ratingKey}}/grandchildren", "/hubs", "/hubs/sections/{{section}}", "/hubs/search?query=", "/library/onDeck", "/hubs/continueWatching", "/media/providers", "/library/collections", "/playlists", "/video/:/transcode/universal/decision?path=%2Flibrary%2Fmetadata%2F{{ratingKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&X-Plex-Client-Profile-Extra=", "/status/sessions", "/myplex/account", "/:/prefs"}
	clients := Clients()
	out := make([]Request, 0, len(paths))
	for _, p := range paths {
		hs := map[string]http.Header{}
		for k, c := range clients {
			h := c.Headers.Clone()
			h.Set("Accept", "application/xml")
			hs[k] = h
			j := h.Clone()
			j.Set("Accept", "application/json")
			hs[k+"-json"] = j
		}
		out = append(out, Request{Name: p, Method: http.MethodGet, Path: p, Headers: hs})
	}
	return out
}

// Response is a captured HTTP response.
type Response struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

// Difference describes one structural or protocol mismatch.
type Difference struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Reference any    `json:"reference,omitempty"`
	Target    any    `json:"target,omitempty"`
	Blocking  bool   `json:"blocking"`
}

// Report is the result of comparing two responses.
type Report struct {
	Differences []Difference `json:"differences"`
}

// Compare checks status, selected headers and response structure while tolerating volatile values.
func Compare(a, b Response) Report {
	r := Report{}
	add := func(path, kind string, x, y any) {
		r.Differences = append(r.Differences, Difference{Path: path, Kind: kind, Reference: x, Target: y, Blocking: true})
	}
	if a.Status != b.Status {
		add("$status", "status", a.Status, b.Status)
	}
	for _, k := range []string{"Content-Type", "X-Plex-Protocol", "Access-Control-Allow-Origin", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods"} {
		av, bv := a.Headers.Get(k), b.Headers.Get(k)
		if av != bv {
			add("$headers."+k, "header", av, bv)
		}
	}
	am, ae := flatten(a.Body)
	bm, be := flatten(b.Body)
	if ae != nil || be != nil {
		if ae != nil || be != nil {
			add("$body", "parse", errString(ae), errString(be))
		}
		return r
	}
	keys := map[string]bool{}
	for k := range am {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		x, xok := am[k]
		y, yok := bm[k]
		switch {
		case !xok:
			add(k, "extra", nil, y)
		case !yok:
			add(k, "missing", x, nil)
		case x.kind != y.kind:
			add(k, "type", x.kind, y.kind)
		case x.value != y.value && !volatile(k):
			if !shapeEquivalent(k, x.value, y.value) {
				add(k, "value", x.value, y.value)
			}
		}
	}
	return r
}
func errString(e error) any {
	if e != nil {
		return e.Error()
	}
	return nil
}

type atom struct{ kind, value string }
type xmlFrame struct {
	name     string
	index    map[string]int
	children []string
}

var volatileRE = regexp.MustCompile(`(?i)(title|summary|machineIdentifier|token|timestamp|updatedAt|username|email|guid|ratingKey|key|id|thumb|art|banner|logo|url)`)

func volatile(path string) bool {
	leaf := path[strings.LastIndex(path, "/")+1:]
	leaf = strings.TrimPrefix(leaf, "@")
	return volatileRE.MatchString(leaf)
}
func shapeEquivalent(path, x, y string) bool {
	if volatile(path) {
		if strings.Contains(strings.ToLower(path), "url") {
			return urlShape(x) == urlShape(y)
		}
		return true
	}
	if (x == "0" || x == "1") && (y == "false" || y == "true") || (y == "0" || y == "1") && (x == "false" || x == "true") {
		return true
	}
	return false
}
func urlShape(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexByte(s, '/'); j >= 0 {
			s = s[j:]
		}
		if j := strings.IndexAny(s, "?#"); j >= 0 {
			s = s[:j]
		}
	}
	return regexp.MustCompile(`/[0-9]+`).ReplaceAllString(s, "/{id}")
}
func flatten(body []byte) (map[string]atom, error) {
	out := map[string]atom{}
	t := bytes.TrimSpace(body)
	if len(t) == 0 {
		return out, nil
	}
	if t[0] == '{' || t[0] == '[' {
		var v any
		if e := json.Unmarshal(t, &v); e != nil {
			return nil, e
		}
		walkJSON(out, "$", v)
		return out, nil
	}
	dec := xml.NewDecoder(bytes.NewReader(t))
	stack := []xmlFrame{}
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch x := tok.(type) {
		case xml.StartElement:
			name := x.Name.Local
			if len(stack) > 0 {
				p := &stack[len(stack)-1]
				p.children = append(p.children, name)
				i := p.index[name]
				p.index[name]++
				name = fmt.Sprintf("%s[%d]", name, i)
			}
			stack = append(stack, xmlFrame{name: name, index: map[string]int{}})
			path := xmlPath(stack)
			out[path] = atom{kind: "element", value: ""}
			for _, a := range x.Attr {
				out[path+"/@"+a.Name.Local] = atomFor(a.Value)
			}
		case xml.CharData:
			s := strings.TrimSpace(string(x))
			if s != "" && len(stack) > 0 {
				out[xmlPath(stack)+"/#text"] = atomFor(s)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				if len(stack[len(stack)-1].children) > 1 {
					out[xmlPath(stack)+"/#order"] = atom{kind: "order", value: strings.Join(stack[len(stack)-1].children, ",")}
				}
				stack = stack[:len(stack)-1]
			}
		}
	}
	return out, nil
}
func xmlPath(s []xmlFrame) string {
	p := ""
	for _, f := range s {
		p += "/" + f.name
	}
	return p
}
func walkJSON(m map[string]atom, p string, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkJSON(m, p+"/"+k, x[k])
		}
	case []any:
		for i, v := range x {
			walkJSON(m, fmt.Sprintf("%s[%d]", p, i), v)
		}
	default:
		switch x.(type) {
		case nil:
			m[p] = atom{"null", "null"}
		case bool:
			m[p] = atom{"boolean", fmt.Sprint(x)}
		case float64:
			m[p] = atom{"number", fmt.Sprint(x)}
		case string:
			m[p] = atom{"string", x.(string)}
		}
	}
}
func atomFor(s string) atom {
	if _, e := strconv.ParseInt(s, 10, 64); e == nil {
		return atom{"number", s}
	}
	if _, e := strconv.ParseFloat(s, 64); e == nil {
		return atom{"number", s}
	}
	if s == "true" || s == "false" {
		return atom{"boolean", s}
	}
	return atom{"string", s}
}

// Redact removes common account and host identifiers from captured XML/JSON/text.
func Redact(body []byte) []byte {
	s := string(body)
	patterns := []struct {
		re   *regexp.Regexp
		repl string
	}{{regexp.MustCompile(`(?i)(X-Plex-Token[=:" ]+)[^&\s"<>]+`), "${1}[REDACTED]"}, {regexp.MustCompile(`(?i)(token[=:" ]+)[^&\s"<>]+`), "${1}[REDACTED]"}, {regexp.MustCompile(`(?i)([\w.+-]+)@([\w.-]+\.[A-Za-z]{2,})`), "[REDACTED_EMAIL]"}, {regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "[REDACTED_IP]"}, {regexp.MustCompile(`(?i)(machineIdentifier[=:" ]+)[^&\s"<>]+`), "${1}[REDACTED]"}, {regexp.MustCompile(`(?i)(username|user|friendlyName|serverName)(="|: ?")([^"]*)`), "${1}=${2}[REDACTED]"}}
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return []byte(s)
}

// SaveFixture stores one redacted response as a JSON fixture.
func SaveFixture(dir, name string, r Response) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	r.Body = Redact(r.Body)
	for k := range r.Headers {
		if strings.Contains(strings.ToLower(k), "token") {
			r.Headers.Del(k)
		}
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644)
}

// LoadFixture reads a response fixture.
func LoadFixture(path string) (Response, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Response{}, e
	}
	var r Response
	e = json.Unmarshal(b, &r)
	return r, e
}

// Fetch issues a probe, substituting discovered IDs and token, and captures its response.
func Fetch(ctx context.Context, client *http.Client, base, token string, req Request, profile string, vars map[string]string, jsonMode bool) (Response, error) {
	path := req.Path
	for k, v := range vars {
		path = strings.ReplaceAll(path, "{{"+k+"}}", v)
	}
	if strings.Contains(path, "{{") {
		path = strings.ReplaceAll(path, "{{section}}", "1")
		path = strings.ReplaceAll(path, "{{ratingKey}}", "1")
	}
	h := req.Headers[profile]
	if jsonMode {
		h = req.Headers[profile+"-json"]
	}
	u := strings.TrimRight(base, "/") + path
	q, e := http.NewRequestWithContext(ctx, req.Method, u, nil)
	if e != nil {
		return Response{}, e
	}
	for k, v := range h {
		for _, x := range v {
			q.Header.Add(k, strings.ReplaceAll(x, "{{token}}", token))
		}
	}
	res, e := client.Do(q)
	if e != nil {
		return Response{}, e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if e != nil {
		return Response{}, e
	}
	return Response{Status: res.StatusCode, Headers: res.Header.Clone(), Body: b}, nil
}
