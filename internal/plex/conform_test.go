package plex

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/pmsconform"
)

// The reference fixtures in testdata/pms were recorded from a real PMS
// 1.43.4 playing "The 'Burbs" (HEVC Main 10 + DTS stereo + PGS in MKV).
// These tests give Ether the same title and media and diff its decision
// responses against PMS's with pmsconform: field names, JSON types,
// child order and XML attribute order must match; only data Ether does not
// have (Common Sense Media, critic ratings, extras, deep analysis) may be
// missing.

var burbs = core.Title{
	Key: core.MovieKey(11974), Type: core.TypeMovie, IDs: core.IDs{TMDB: 11974, IMDB: "tt0096734"},
	Title: "The 'Burbs", Summary: "He's a man of peace in a savage land: Suburbia.", Tagline: "He's a man of peace in a savage land... Suburbia.",
	Year: 1989, Released: time.Date(1989, 2, 17, 0, 0, 0, 0, time.UTC), Runtime: 101 * time.Minute,
	Genres: []string{"Comedy", "Thriller", "Mystery", "Horror"}, Studios: []string{"Universal Pictures"}, Countries: []string{"United States of America"},
	ContentRating: "PG", Rating: 6.8, PosterPath: "/burbs.jpg", BackdropPath: "/burbsart.jpg", LogoPath: "/burbslogo.png",
	Cast: []core.Person{
		{TMDB: 31, Name: "Tom Hanks", Role: "Ray Peterson", ThumbPath: "/hanks.jpg"},
		{TMDB: 1150, Name: "Bruce Dern", Role: "Lt. Mark Rumsfield", ThumbPath: "/dern.jpg"},
	},
	Directors:    []core.Person{{TMDB: 11218, Name: "Joe Dante", ThumbPath: "/dante.jpg"}},
	Writers:      []core.Person{{TMDB: 70939, Name: "Dana Olsen"}},
	CollectionID: 9999, CollectionName: "Suburbia",
	UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
}

var burbsInfo = core.MediaInfo{
	Container: "mkv", Duration: 6086390 * time.Millisecond, Size: 4289930164, BitrateKbps: 5639,
	Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "hevc", Profile: "main 10", Language: "en", Width: 1920, Height: 1036, FrameRate: 23.976, BitDepth: 10, BitrateKbps: 4103, ColorTransfer: "bt709"},
		{Index: 1, Type: "audio", Codec: "dts", Profile: "dts", Language: "en", Title: "Stereo", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000, Default: true, BitrateKbps: 1536},
		{Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "en"},
	},
}

// pmsOnlyRE are paths of data PMS has and Ether doesn't: Common Sense
// Media, critic ratings, extras, locked fields, labels, producers and
// similar-title tags, deep analysis, preview thumbnails and probe details
// that the virtual media can't know before a release is opened.
var pmsOnlyRE = regexp.MustCompile(`(@|/)(chapterSource|contentRatingAge|primaryExtraKey|rating|ratingImage|CommonSenseMedia|Field|Label|Producer|Similar|deepAnalysisVersion|indexes|requiredBandwidths|bitrateMode|chromaLocation|codedHeight|codedWidth|colorPrimaries|colorRange|colorSpace|level|refFrames|titleSort)$`)

// etherOnlyRE are paths Ether legitimately adds (TMDB has writer photos).
var etherOnlyRE = regexp.MustCompile(`Writer(\[\])?/@?thumb$`)

func pmsFixtureName(profile, path string, jsonMode bool) string {
	ext := "xml"
	if jsonMode {
		ext = "json"
	}
	return strings.NewReplacer("/", "_", ":", "", "?", "_", "=", "-").Replace(profile+"_"+ext+path) + ".json"
}

func TestDecisionConformsToPMS(t *testing.T) {
	type mode struct {
		name     string
		template string // corpus path
		decision core.Decision
		codes    map[string]string // container attributes that must equal PMS's
	}
	transcode := core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", AudioCodec: "mp3", Container: "mpegts", BitrateKbps: 9079}
	modes := []mode{
		{
			name:     "legacy",
			template: "/video/:/transcode/universal/decision?path=%2Flibrary%2Fmetadata%2F{{movieKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&directPlay=1&directStream=1&videoQuality=8&X-Plex-Client-Profile-Extra=",
			decision: transcode,
			codes:    map[string]string{"generalDecisionCode": "", "generalDecisionText": "", "directPlayDecisionCode": "", "transcodeDecisionCode": "", "transcodeDecisionText": ""},
		},
		{
			name:     "mde-direct",
			template: "/video/:/transcode/universal/decision?hasMDE=1&path=%2Flibrary%2Fmetadata%2F{{movieKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&directPlay=1&directStream=1&videoQuality=8",
			decision: transcode, // ignored: the client decides
			codes:    map[string]string{"mdeDecisionCode": "", "mdeDecisionText": "", "generalDecisionCode": "", "transcodeDecisionCode": ""},
		},
		{
			name:     "mde-stream",
			template: "/video/:/transcode/universal/decision?hasMDE=1&path=%2Flibrary%2Fmetadata%2F{{movieKey}}&mediaIndex=0&partIndex=0&protocol=hls&fastSeek=1&directPlay=0&directStream=1&subtitleSize=100&audioBoost=100&location=lan",
			decision: core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", AudioCodec: "mp3", Container: "mpegts", BitrateKbps: 20256},
			codes:    map[string]string{"generalDecisionCode": "", "directPlayDecisionCode": "", "directPlayDecisionText": "", "transcodeDecisionCode": "", "mdeDecisionCode": ""},
		},
	}
	clients := pmsconform.Clients()
	for _, md := range modes {
		for _, profile := range []string{"web", "ios", "appletv"} {
			for _, jsonMode := range []bool{false, true} {
				name := md.name + "/" + profile + "/" + map[bool]string{false: "xml", true: "json"}[jsonMode]
				t.Run(name, func(t *testing.T) {
					ref, err := pmsconform.LoadFixture(filepath.Join("..", "..", "testdata", "pms", profile, pmsFixtureName(profile, md.template, jsonMode)))
					if err != nil {
						t.Fatalf("fixture: %v", err)
					}
					f := newFixture()
					defer f.srv.Close()
					f.catalog.titles[burbs.Key] = burbs
					f.playback.info = burbsInfo
					f.trans.decision = md.decision

					hdr := map[string]string{}
					for k, v := range clients[profile].Headers {
						if k != "X-Plex-Token" {
							hdr[k] = v[0]
						}
					}
					hdr["Accept"] = "application/xml"
					if jsonMode {
						hdr["Accept"] = "application/json"
					}
					target := strings.ReplaceAll(md.template, "{{movieKey}}", burbs.Key.String())
					f.request(t, http.MethodGet, target, hdr)
					f.srv.waitBackground() // UltraBlurColors
					rec := f.request(t, http.MethodGet, target, hdr)
					ours := pmsconform.Response{Status: rec.Code, Headers: rec.Header(), Body: rec.Body.Bytes()}

					for _, d := range pmsconform.Compare(ref, ours).Differences {
						switch {
						case d.Kind == pmsconform.KindMissing && pmsOnlyRE.MatchString(d.Path):
						case d.Kind == pmsconform.KindExtra && etherOnlyRE.MatchString(d.Path):
						case d.Kind == pmsconform.KindValue && !strings.HasSuffix(d.Path, "#order"):
						default:
							t.Errorf("%s %s ref=%v ours=%v", d.Kind, d.Path, d.Reference, d.Target)
						}
					}
					refAttrs, ourAttrs := containerAttrs(t, ref.Body), containerAttrs(t, ours.Body)
					for k := range md.codes {
						if refAttrs[k] != ourAttrs[k] {
							t.Errorf("container %s = %q, PMS %q", k, ourAttrs[k], refAttrs[k])
						}
					}
					if !jsonMode {
						checkAttrOrder(t, ref.Body, ours.Body)
					}
				})
			}
		}
	}
}

// containerAttrs returns the root container's attributes as strings.
func containerAttrs(t *testing.T, body []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	b := bytes.TrimSpace(body)
	if len(b) > 0 && b[0] == '{' {
		var v struct {
			MediaContainer map[string]any `json:"MediaContainer"`
		}
		if err := jsonUnmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		for k, x := range v.MediaContainer {
			switch x.(type) {
			case map[string]any, []any:
			default:
				out[k] = jsonScalar(x)
			}
		}
		return out
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			for _, a := range se.Attr {
				out[a.Name.Local] = a.Value
			}
			return out
		}
	}
}

// checkAttrOrder requires the attributes Ether and PMS both send on each
// element to appear in the same order (first element of each name;
// streams by type).
func checkAttrOrder(t *testing.T, ref, ours []byte) {
	t.Helper()
	ro, oo := attrOrders(t, ref), attrOrders(t, ours)
	for el, want := range ro {
		got, ok := oo[el]
		if !ok {
			continue
		}
		common := map[string]bool{}
		for _, a := range want {
			common[a] = true
		}
		var g []string
		inGot := map[string]bool{}
		for _, a := range got {
			inGot[a] = true
			if common[a] {
				g = append(g, a)
			}
		}
		var w []string
		for _, a := range want {
			if inGot[a] {
				w = append(w, a)
			}
		}
		if strings.Join(g, ",") != strings.Join(w, ",") {
			t.Errorf("%s attribute order:\n ours %v\n PMS  %v", el, g, w)
		}
	}
}

func attrOrders(t *testing.T, body []byte) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		key := se.Name.Local
		var names []string
		for _, a := range se.Attr {
			names = append(names, a.Name.Local)
			if key == "Stream" && a.Name.Local == "streamType" {
				key += ":" + a.Value
			}
		}
		if key == "Stream" {
			key += ":?"
		}
		if _, seen := out[key]; !seen {
			out[key] = names
		}
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func jsonScalar(x any) string {
	switch v := x.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	}
	return fmt.Sprint(x)
}

func TestPMSFixturesPresent(t *testing.T) {
	// Guard against the fixture set moving: the conformance tests above
	// silently depend on it.
	if _, err := os.Stat(filepath.Join("..", "..", "testdata", "pms", "manifest.json")); err != nil {
		t.Fatalf("reference fixtures missing: %v", err)
	}
}
