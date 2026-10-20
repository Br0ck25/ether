package livetv

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var m3uAttr = regexp.MustCompile(`([a-zA-Z0-9_-]+)="([^"]*)"`)

func stableID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:10])
}
func validHTTP(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ParseM3U preserves only explicitly provided HTTP(S) streams. Executable,
// file and media-tool pseudo protocols are rejected.
func ParseM3U(r io.Reader, sourceID string) ([]Channel, error) {
	sc := bufio.NewScanner(io.LimitReader(r, 16<<20))
	sc.Buffer(make([]byte, 4096), 1<<20)
	var out []Channel
	var pending *Channel
	header := false
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		if !header {
			if !strings.HasPrefix(line, "#EXTM3U") {
				return nil, fmt.Errorf("playlist must start with #EXTM3U")
			}
			header = true
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			attrs := map[string]string{}
			for _, m := range m3uAttr.FindAllStringSubmatch(line, -1) {
				attrs[m[1]] = m[2]
			}
			// Commas inside quoted attributes are not the display-name separator.
			quote := false
			comma := -1
			for i, ch := range line {
				if ch == '"' {
					quote = !quote
				}
				if ch == ',' && !quote {
					comma = i
					break
				}
			}
			name := attrs["tvg-name"]
			if comma >= 0 && strings.TrimSpace(line[comma+1:]) != "" {
				name = strings.TrimSpace(line[comma+1:])
			}
			pending = &Channel{Name: name, Number: attrs["tvg-chno"], Logo: attrs["tvg-logo"], GuideID: attrs["tvg-id"], Group: attrs["group-title"], SourceID: sourceID, Kind: "iptv", Enabled: true}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pending == nil {
			return nil, fmt.Errorf("stream has no #EXTINF entry")
		}
		if !validHTTP(line) {
			return nil, fmt.Errorf("channel %q needs an HTTP(S) stream", pending.Name)
		}
		pending.StreamURL = line
		identity := pending.GuideID
		if identity == "" {
			identity = line
		}
		pending.ID = stableID(sourceID, identity)
		if seen[pending.ID] {
			pending.ID = stableID(sourceID, identity, pending.Name, pending.Number, pending.Group)
			if seen[pending.ID] {
				pending.ID = stableID(sourceID, identity, line)
			}
			if seen[pending.ID] {
				pending = nil
				continue
			}
		}
		seen[pending.ID] = true
		pending.StreamSources = []StreamSource{{ID: stableID(pending.ID, "playlist"), Name: "Imported stream", URL: line, Enabled: true}}
		if pending.Name == "" {
			pending.Name = pending.GuideID
		}
		if pending.Name == "" {
			pending.Name = "Channel"
		}
		if pending.Number == "" {
			pending.Number = fmt.Sprint(len(out) + 1)
		}
		out = append(out, *pending)
		pending = nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if pending != nil {
		return nil, fmt.Errorf("playlist ends before a stream URL")
	}
	if !header {
		return nil, fmt.Errorf("empty playlist")
	}
	return out, nil
}

type xmlText struct {
	Value string `xml:",chardata"`
}
type xmlChannel struct {
	ID   string    `xml:"id,attr"`
	Name []xmlText `xml:"display-name"`
	Icon struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
}
type xmlProgramme struct {
	Channel string    `xml:"channel,attr"`
	Start   string    `xml:"start,attr"`
	Stop    string    `xml:"stop,attr"`
	Title   []xmlText `xml:"title"`
	Desc    []xmlText `xml:"desc"`
	Rating  []struct {
		Value string `xml:"value"`
	} `xml:"rating"`
	Icon struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
}
type XMLGuide struct {
	Channels   map[string]Channel
	Programmes []Programme
	// Names holds every display-name of each XMLTV channel, in order.
	Names map[string][]string
}

func xmlTime(s string) (time.Time, error) {
	for _, layout := range []string{"20060102150405 -0700", "200601021504 -0700", "20060102150405-0700", "200601021504-0700", "20060102150405", "200601021504"} {
		if t, e := time.Parse(layout, strings.TrimSpace(s)); e == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid XMLTV timestamp %q", s)
}
func ParseXMLTV(r io.Reader) (XMLGuide, error) {
	g := XMLGuide{Channels: map[string]Channel{}, Names: map[string][]string{}}
	d := xml.NewDecoder(io.LimitReader(r, 64<<20))
	root := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return g, err
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !root {
			if el.Name.Local != "tv" {
				return g, fmt.Errorf("XMLTV root must be tv")
			}
			root = true
			continue
		}
		switch el.Name.Local {
		case "channel":
			var c xmlChannel
			if err := d.DecodeElement(&c, &el); err != nil {
				return g, err
			}
			name := c.ID
			if len(c.Name) > 0 {
				name = c.Name[0].Value
			}
			g.Channels[c.ID] = Channel{GuideID: c.ID, Name: name, Logo: c.Icon.Src}
			for _, n := range c.Name {
				g.Names[c.ID] = append(g.Names[c.ID], strings.TrimSpace(n.Value))
			}
		case "programme":
			var p xmlProgramme
			if err := d.DecodeElement(&p, &el); err != nil {
				return g, err
			}
			start, err := xmlTime(p.Start)
			if err != nil {
				return g, err
			}
			// stop is optional in XMLTV; such a programme runs until the
			// channel's next one (resolved after the whole guide is read).
			var end time.Time
			if strings.TrimSpace(p.Stop) != "" {
				if end, err = xmlTime(p.Stop); err != nil {
					return g, err
				}
				if !end.After(start) {
					return g, fmt.Errorf("programme stop must follow start")
				}
			}
			item := Item{Thumb: p.Icon.Src}
			if !end.IsZero() {
				item.DurationMS = end.Sub(start).Milliseconds()
			}
			if len(p.Title) > 0 {
				item.Title = p.Title[0].Value
			}
			if len(p.Desc) > 0 {
				item.Summary = p.Desc[0].Value
			}
			if len(p.Rating) > 0 {
				item.Rating = p.Rating[0].Value
			}
			g.Programmes = append(g.Programmes, Programme{ID: stableID(p.Channel, p.Start), ChannelID: p.Channel, Start: start, End: end, Item: item})
		}
	}
	if !root {
		return g, fmt.Errorf("empty XMLTV guide")
	}
	g.Programmes = inferMissingStops(g.Programmes)
	return g, nil
}

// inferMissingStops ends each programme without a stop time at the start
// of the next later programme on its channel; one with no successor has no
// knowable end and is dropped.
func inferMissingStops(ps []Programme) []Programme {
	open := false
	for _, p := range ps {
		if p.End.IsZero() {
			open = true
			break
		}
	}
	if !open {
		return ps
	}
	starts := map[string][]time.Time{}
	for _, p := range ps {
		starts[p.ChannelID] = append(starts[p.ChannelID], p.Start)
	}
	for _, ts := range starts {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
	}
	out := ps[:0]
	for _, p := range ps {
		if p.End.IsZero() {
			ts := starts[p.ChannelID]
			i := sort.Search(len(ts), func(i int) bool { return ts[i].After(p.Start) })
			if i == len(ts) {
				continue
			}
			p.End = ts[i]
			p.Item.DurationMS = p.End.Sub(p.Start).Milliseconds()
		}
		out = append(out, p)
	}
	return out
}
