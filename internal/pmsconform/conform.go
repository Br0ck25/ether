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
	"strconv"
	"strings"
)

// Client describes a Plex application header profile.
type Client struct {
	Name    string
	Headers http.Header
}

// Clients returns representative Plex Web, iOS and Apple TV request
// profiles, with the X-Plex-Platform values the real apps send (PMS picks
// its client profile from them and answers 400 to a decision request from
// an unknown platform such as "iPhone").
func Clients() map[string]Client {
	return map[string]Client{
		"web":     {Name: "Plex Web", Headers: headers("Plex Web", "Chrome", "129.0", "Windows", "Chrome", "4.160.0")},
		"ios":     {Name: "Plex for iOS", Headers: headers("Plex for iOS", "iOS", "18.0", "iPhone", "iPhone", "8.40")},
		"appletv": {Name: "Plex for Apple TV", Headers: headers("Plex for Apple TV", "tvOS", "18.0", "Apple TV", "Apple TV", "8.40")},
	}
}
func headers(product, platform, platformVersion, device, deviceName, version string) http.Header {
	h := http.Header{}
	h.Set("X-Plex-Product", product)
	h.Set("X-Plex-Version", version)
	h.Set("X-Plex-Platform", platform)
	h.Set("X-Plex-Platform-Version", platformVersion)
	h.Set("X-Plex-Device", device)
	h.Set("X-Plex-Device-Name", deviceName)
	h.Set("X-Plex-Client-Identifier", "pmsdiff-client")
	h.Set("X-Plex-Provides", "player,controller")
	h.Set("X-Plex-Token", "{{token}}")
	return h
}

// Request is one logical request in the PMS probe corpus.
type Request struct {
	Name, Method, Path string
	Headers            map[string]http.Header
	Body               []byte
}

// Corpus returns probes used when a Plex application connects and browses a server.
func Corpus() []Request {
	paths := []string{"/", "/identity", "/library/sections", "/library/sections/{{section}}/all?X-Plex-Container-Start=0&X-Plex-Container-Size=50", "/library/sections/{{movieSection}}/prefs", "/library/sections/{{showSection}}/prefs", "/library/metadata/{{movieKey}}?includeExtras=1&includeRelated=1&includeOnDeck=1&includeChapters=1&includeGuids=1", "/library/metadata/{{showKey}}/children", "/library/metadata/{{seasonKey}}/children", "/library/metadata/{{episodeKey}}", "/hubs", "/hubs/promoted", "/hubs/sections/{{section}}", "/hubs/search?query=", "/library/onDeck", "/hubs/continueWatching", "/media/providers", "/library/collections/{{collectionKey}}", "/playlists/{{playlistKey}}", "/video/:/transcode/universal/decision?path=%2Flibrary%2Fmetadata%2F{{movieKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&directPlay=1&directStream=1&videoQuality=8&X-Plex-Client-Profile-Extra=", "/status/sessions", "/myplex/account", "/:/prefs",
		// Current apps run a client-side media decision engine (hasMDE=1):
		// with direct play allowed PMS only reports mdeDecisionCode; Plex
		// Web asks with directPlay=0 for a stream.
		"/video/:/transcode/universal/decision?hasMDE=1&path=%2Flibrary%2Fmetadata%2F{{movieKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&directPlay=1&directStream=1&videoQuality=8",
		"/video/:/transcode/universal/decision?hasMDE=1&path=%2Flibrary%2Fmetadata%2F{{movieKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&directPlay=0&directStream=1&subtitleSize=100&audioBoost=100&location=lan",
		// Searches with results (the query matches the discovered movie).
		"/hubs/search?query=burbs&limit=10&includeCollections=1",
		"/library/search?query=burbs&limit=10&searchTypes=movies%2Ctv&includeMetadata=1"}
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

// DiscoveredIDs holds per-server identifiers used to make templated probes target real items.
type DiscoveredIDs struct {
	Values   map[string]string `json:"values"`
	Warnings []string          `json:"warnings,omitempty"`
}

type xmlNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:"-"`
	Children []xmlNode  `xml:"-"`
}

func (n *xmlNode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	n.XMLName = start.Name
	n.Attrs = start.Attr
	for {
		t, e := d.Token()
		if e != nil {
			return e
		}
		switch x := t.(type) {
		case xml.StartElement:
			var c xmlNode
			if e := d.DecodeElement(&c, &x); e != nil {
				return e
			}
			n.Children = append(n.Children, c)
		case xml.EndElement:
			if x.Name == start.Name {
				return nil
			}
		}
	}
}
func (n xmlNode) attr(k string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == k {
			return a.Value
		}
	}
	return ""
}
func (n xmlNode) descendants(name string) []xmlNode {
	var out []xmlNode
	for _, c := range n.Children {
		if c.XMLName.Local == name {
			out = append(out, c)
		}
		out = append(out, c.descendants(name)...)
	}
	return out
}
func (n xmlNode) direct(names ...string) []xmlNode {
	var out []xmlNode
	for _, c := range n.Children {
		for _, name := range names {
			if c.XMLName.Local == name {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
func (n xmlNode) first(names ...string) xmlNode {
	for _, name := range names {
		for _, c := range n.Children {
			if c.XMLName.Local == name {
				return c
			}
		}
	}
	return xmlNode{}
}
func parsePlexXML(b []byte) (xmlNode, error) { var n xmlNode; e := xml.Unmarshal(b, &n); return n, e }

// Discover reads sections and representative items on one server. If align is non-nil,
// matching item GUIDs from that server are preferred; absent matches use the first item.
func Discover(ctx context.Context, client *http.Client, base, token, profile string, align *DiscoveredIDs) DiscoveredIDs {
	out := DiscoveredIDs{Values: map[string]string{}}
	get := func(path string) (xmlNode, error) {
		req := Request{Method: http.MethodGet, Path: path, Headers: map[string]http.Header{}}
		c := Clients()[profile]
		h := c.Headers.Clone()
		h.Set("Accept", "application/xml")
		req.Headers[profile] = h
		r, e := Fetch(ctx, client, base, token, req, profile, nil, false)
		if e != nil {
			return xmlNode{}, e
		}
		if r.Status < 200 || r.Status >= 300 {
			return xmlNode{}, fmt.Errorf("HTTP %d", r.Status)
		}
		return parsePlexXML(r.Body)
	}
	root, e := get("/library/sections")
	if e != nil {
		out.Warnings = append(out.Warnings, "sections discovery failed: "+e.Error())
		return out
	}
	sections := root.descendants("Directory")
	sectionByType := map[string]string{}
	for _, s := range sections {
		typ := strings.ToLower(s.attr("type"))
		if typ == "movie" || typ == "show" {
			if sectionByType[typ] == "" {
				sectionByType[typ] = s.attr("key")
			}
		}
	}
	for _, typ := range []string{"movie", "show"} {
		sid := sectionByType[typ]
		if sid == "" {
			out.Warnings = append(out.Warnings, "no "+typ+" section; using fallback section ID 1")
			continue
		}
		out.Values[typ+"Section"] = sid
		listing, err := get("/library/sections/" + sid + "/all?X-Plex-Container-Start=0&X-Plex-Container-Size=50")
		if err != nil {
			out.Warnings = append(out.Warnings, typ+" listing failed: "+err.Error())
			continue
		}
		items := listing.direct("Video", "Metadata")
		if len(items) == 0 {
			items = listing.direct("Directory")
		}
		if len(items) == 0 {
			out.Warnings = append(out.Warnings, "no "+typ+" item; using fallback ratingKey 1")
			continue
		}
		chosen := selectByGuid(items, alignedGuid(align, typ))
		key := nodeKey(chosen)
		if key == "" {
			key = "1"
			out.Warnings = append(out.Warnings, typ+" item has no key; using fallback ratingKey 1")
		}
		out.Values[typ+"Key"] = key
		guid := nodeGuid(chosen)
		if detail, er := get("/library/metadata/" + key + "?includeExtras=1&includeRelated=1&includeOnDeck=1&includeChapters=1&includeGuids=1"); er == nil {
			detailItems := append(detail.descendants("Metadata"), detail.descendants("Video")...)
			for _, item := range detailItems {
				if g := nodeGuid(item); strings.HasPrefix(g, "tmdb://") || strings.HasPrefix(g, "imdb://") {
					guid = g
					break
				}
			}
		}
		out.Values[typ+"Guid"] = guid
		if typ == "show" {
			seasons, err := get("/library/metadata/" + key + "/children")
			if err != nil {
				out.Warnings = append(out.Warnings, "season discovery failed: "+err.Error())
				continue
			}
			ss := seasons.direct("Metadata")
			if len(ss) == 0 {
				ss = seasons.direct("Directory")
			}
			filtered := ss[:0]
			for _, season := range ss {
				if !strings.Contains(strings.ToLower(season.attr("key")), "allleaves") && !strings.EqualFold(season.attr("title"), "All episodes") {
					filtered = append(filtered, season)
				}
			}
			ss = filtered
			if len(ss) > 0 {
				season := ss[0]
				sk := nodeKey(season)
				if sk != "" {
					out.Values["seasonKey"] = sk
					eps, er := get("/library/metadata/" + sk + "/children")
					if er == nil {
						es := eps.direct("Video", "Metadata")
						if len(es) > 0 {
							out.Values["episodeKey"] = nodeKey(es[0])
						}
					} else {
						out.Warnings = append(out.Warnings, "episode discovery failed: "+er.Error())
					}
				}
			}
		}
	}
	for _, entry := range []struct{ key, path, tag string }{{"collectionKey", "/library/collections", "Directory"}, {"playlistKey", "/playlists", "Playlist"}} {
		n, er := get(entry.path)
		if er != nil && entry.key == "collectionKey" && out.Values["movieSection"] != "" {
			n, er = get("/library/sections/" + out.Values["movieSection"] + "/all?type=18&X-Plex-Container-Start=0&X-Plex-Container-Size=50")
		}
		if er != nil && entry.key == "playlistKey" {
			n, er = get("/playlists/all")
		}
		if er != nil {
			out.Warnings = append(out.Warnings, entry.key+" discovery failed: "+er.Error())
			continue
		}
		xs := n.descendants(entry.tag)
		if len(xs) > 0 {
			v := nodeKey(xs[0])
			if v != "" {
				out.Values[entry.key] = v
			}
		}
	}
	if out.Values["section"] == "" {
		if out.Values["movieSection"] != "" {
			out.Values["section"] = out.Values["movieSection"]
		} else if out.Values["showSection"] != "" {
			out.Values["section"] = out.Values["showSection"]
		}
	}
	for k, v := range map[string]string{"section": "1", "movieKey": "1", "showKey": "1", "seasonKey": "1", "episodeKey": "1", "collectionKey": "1", "playlistKey": "1"} {
		if out.Values[k] == "" {
			out.Values[k] = v
			out.Warnings = append(out.Warnings, "using fallback "+k+" ID "+v)
		}
	}
	return out
}
func nodeKey(n xmlNode) string {
	for _, k := range []string{"ratingKey", "id", "key"} {
		if v := n.attr(k); v != "" {
			if k == "key" && strings.HasPrefix(v, "/") {
				parts := strings.Split(strings.Trim(v, "/"), "/")
				for i := len(parts) - 1; i >= 0; i-- {
					if _, err := strconv.ParseInt(parts[i], 10, 64); err == nil {
						return parts[i]
					}
				}
			}
			return v
		}
	}
	return ""
}
func nodeGuid(n xmlNode) string {
	if v := n.attr("guid"); strings.HasPrefix(v, "tmdb://") || strings.HasPrefix(v, "imdb://") {
		return v
	}
	var fallback string
	if v := n.attr("guid"); v != "" {
		fallback = v
	}
	for _, g := range append([]xmlNode{n.first("Guid")}, n.descendants("Guid")...) {
		v := g.attr("id")
		if strings.HasPrefix(v, "tmdb://") || strings.HasPrefix(v, "imdb://") {
			return v
		}
		if fallback == "" {
			fallback = v
		}
	}
	return fallback
}
func alignedGuid(d *DiscoveredIDs, typ string) string {
	if d == nil {
		return ""
	}
	g := d.Values[typ+"Guid"]
	if strings.HasPrefix(g, "tmdb://") || strings.HasPrefix(g, "imdb://") {
		return g
	}
	return ""
}
func selectByGuid(xs []xmlNode, guid string) xmlNode {
	if guid != "" {
		for _, x := range xs {
			if nodeGuid(x) == guid {
				return x
			}
		}
	}
	return xs[0]
}

// Redact removes common account and host identifiers from captured XML/JSON/text.
func Redact(body []byte) []byte {
	s := string(body)
	patterns := []struct {
		re   *regexp.Regexp
		repl string
	}{{regexp.MustCompile(`(?i)(X-Plex-Token"?\s*[=:]\s*"?)[^&\s"<>]+`), "${1}[REDACTED]"}, {regexp.MustCompile(`(?i)(token"?\s*[=:]\s*"?)[^&\s"<>]+`), "${1}[REDACTED]"}, {regexp.MustCompile(`(?i)([\w.+-]+)@([\w.-]+\.[A-Za-z]{2,})`), "[REDACTED_EMAIL]"}, {regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "[REDACTED_IP]"}, {regexp.MustCompile(`(?i)(machineIdentifier"?\s*[=:]\s*"?)[^&\s"<>]+`), "${1}[REDACTED]"}, {regexp.MustCompile(`(?i)\b((?:\w*username|\w*friendlyName|\w*serverName|user|name)(?:"\s*:\s*"|="|=))[^&\s"<>]+`), "${1}[REDACTED]"}}
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	s = regexp.MustCompile(`(?i)(<(?:username|user|friendlyName|serverName|machineIdentifier|X-Plex-Token)[^>]*>)[^<]*(</(?:username|user|friendlyName|serverName|machineIdentifier|X-Plex-Token)>)`).ReplaceAllString(s, "${1}[REDACTED]${2}")
	s = regexp.MustCompile(`\b(?:[A-Fa-f0-9]{1,4}:){2,7}[A-Fa-f0-9]{0,4}\b`).ReplaceAllString(s, "[REDACTED_IP]")
	return []byte(s)
}

// SaveFixture stores one redacted response as a JSON fixture.
func SaveFixture(dir, name string, r Response) error {
	_, err := SaveFixtureTrimmed(dir, name, r)
	return err
}

// SaveFixtureTrimmed redacts and limits each repeated container child list to 50 items.
// It returns the number of response bodies whose listings were shortened.
func SaveFixtureTrimmed(dir, name string, r Response) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	var trimmed bool
	r.Body, trimmed = TrimBody(r.Body)
	r.Body = Redact(r.Body)
	for k, values := range r.Headers {
		if strings.EqualFold(k, "Set-Cookie") || strings.EqualFold(k, "Authorization") {
			r.Headers.Del(k)
			continue
		}
		for i := range values {
			values[i] = string(Redact([]byte(values[i])))
		}
		r.Headers[k] = values
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return 0, e
	}
	if e = os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); e != nil {
		return 0, e
	}
	if trimmed {
		return 1, nil
	}
	return 0, nil
}

// TrimBody truncates XML repeated sibling elements and JSON arrays to their first 50 entries.
func TrimBody(body []byte) ([]byte, bool) {
	t := bytes.TrimSpace(body)
	if len(t) == 0 {
		return body, false
	}
	if t[0] == '{' || t[0] == '[' {
		var v any
		if json.Unmarshal(t, &v) != nil {
			return body, false
		}
		n := trimJSON(v)
		b, e := json.Marshal(n.value)
		if e != nil {
			return body, false
		}
		return b, n.trimmed
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	type frame struct{ counts map[string]int }
	stack := []frame{}
	skip := 0
	trimmed := false
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return body, false
		}
		if skip > 0 {
			switch tok.(type) {
			case xml.StartElement:
				skip++
			case xml.EndElement:
				skip--
			}
			continue
		}
		switch x := tok.(type) {
		case xml.StartElement:
			if len(stack) > 0 {
				p := &stack[len(stack)-1]
				if p.counts[x.Name.Local] >= 50 {
					skip = 1
					trimmed = true
					continue
				}
				p.counts[x.Name.Local]++
			}
			stack = append(stack, frame{counts: map[string]int{}})
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		if enc.EncodeToken(tok) != nil {
			return body, false
		}
	}
	if enc.Flush() != nil {
		return body, false
	}
	return out.Bytes(), trimmed
}

type trimResult struct {
	value   any
	trimmed bool
}

func trimJSON(v any) trimResult {
	switch x := v.(type) {
	case map[string]any:
		was := false
		for k, val := range x {
			r := trimJSON(val)
			x[k] = r.value
			if r.trimmed {
				was = true
			}
		}
		return trimResult{x, was}
	case []any:
		was := len(x) > 50
		if was {
			x = x[:50]
		}
		for i, val := range x {
			r := trimJSON(val)
			x[i] = r.value
			was = was || r.trimmed
		}
		return trimResult{x, was}
	default:
		return trimResult{v, false}
	}
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
	q, e := http.NewRequestWithContext(ctx, req.Method, u, bytes.NewReader(req.Body))
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
