package model

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
)

func sample() MediaContainer {
	return MediaContainer{
		Size:       2,
		AllowSync:  Ptr(false),
		Identifier: LibraryIdentifier,
		Title1:     `Movies & "TV" <x>`,
		Extra:      []Attr{{"machineIdentifier", "abc"}, {"transcoderVideo", true}, {"livetv", 0}},
		Metadata: []Metadata{
			{
				RatingKey: "1000000000000550",
				Key:       "/library/metadata/1000000000000550",
				Type:      "movie",
				Title:     "Fight Club",
				Summary:   "line1\nline2",
				Year:      1999,
				Rating:    8.4,
				Index:     Ptr(0),
				Media:     []Media{{ID: 1, Duration: 8340000, Part: []Part{{ID: 1, Key: "/library/parts/1/0/file.mkv"}}}},
				Genre:     []Tag{{Tag: "Drama"}},
				Guid:      []GUID{{ID: "imdb://tt0137523"}},
			},
			{RatingKey: "2", Type: "show", Title: "Show", LeafCount: Ptr(10)},
		},
	}
}

func TestEncodeXML(t *testing.T) {
	b, err := Marshal(sample(), XML)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `<?xml version="1.0" encoding="UTF-8"?>
<MediaContainer size="2" allowSync="0" identifier="com.plexapp.plugins.library" livetv="0" machineIdentifier="abc" title1="Movies &amp; &#34;TV&#34; &lt;x&gt;" transcoderVideo="1">
<Video ratingKey="1000000000000550" key="/library/metadata/1000000000000550" type="movie" title="Fight Club" summary="line1&#10;line2" index="0" rating="8.4" year="1999">
<Media id="1" duration="8340000">
<Part id="1" key="/library/parts/1/0/file.mkv" />
</Media>
<Genre tag="Drama" />
<Guid id="imdb://tt0137523" />
</Video>
<Directory ratingKey="2" type="show" title="Show" summary="" leafCount="10" />
</MediaContainer>
`
	if got != want {
		t.Fatalf("XML mismatch\n got: %s\nwant: %s", got, want)
	}
	// Must be well-formed.
	d := xml.NewDecoder(strings.NewReader(got))
	for {
		if _, err := d.Token(); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("not well-formed: %v", err)
		}
	}
}

func TestEncodeJSON(t *testing.T) {
	b, err := Marshal(sample(), JSON)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON %s: %v", b, err)
	}
	mc := v["MediaContainer"]
	if mc["size"].(float64) != 2 || mc["allowSync"] != false || mc["transcoderVideo"] != true {
		t.Fatalf("bad attrs: %v", mc)
	}
	md := mc["Metadata"].([]any)
	if len(md) != 2 {
		t.Fatalf("metadata len %d", len(md))
	}
	m0 := md[0].(map[string]any)
	if m0["ratingKey"] != "1000000000000550" || m0["year"].(float64) != 1999 || m0["index"].(float64) != 0 {
		t.Fatalf("bad item: %v", m0)
	}
	if _, ok := m0["Media"].([]any)[0].(map[string]any)["Part"].([]any); !ok {
		t.Fatalf("Part not an array: %v", m0["Media"])
	}
	if !strings.Contains(string(b), `"title1":"Movies & \"TV\" <x>"`) {
		t.Fatalf("html escaped or wrong: %s", b)
	}
}

func TestObjectChildren(t *testing.T) {
	mc := MediaContainer{Meta: &Meta{Type: []MetaType{{Key: "k", Type: "movie", Title: "Movies", Active: true}}}}
	b, _ := Marshal(mc, JSON)
	var v struct {
		MediaContainer struct {
			Meta struct {
				Type []struct{ Key string }
			}
		}
	}
	if err := json.Unmarshal(b, &v); err != nil || len(v.MediaContainer.Meta.Type) != 1 {
		t.Fatalf("Meta should be an object: %s (%v)", b, err)
	}
	x, _ := Marshal(mc, XML)
	if !strings.Contains(string(x), "<Meta>\n<Type key=\"k\" type=\"movie\" title=\"Movies\" active=\"1\" />\n</Meta>") {
		t.Fatalf("xml meta: %s", x)
	}
}

func TestRootName(t *testing.T) {
	b, _ := Marshal(MyPlex{Username: "u", SignInState: "ok"}, XML)
	if !strings.Contains(string(b), "<MyPlex username=\"u\"") {
		t.Fatalf("%s", b)
	}
	j, _ := Marshal(&MyPlex{Username: "u"}, JSON)
	if !strings.HasPrefix(string(j), `{"MyPlex":{`) {
		t.Fatalf("%s", j)
	}
}

func TestInvalidXMLChars(t *testing.T) {
	b, _ := Marshal(MediaContainer{Title1: "a\x01b\xffc"}, XML)
	if !strings.Contains(string(b), `title1="abc"`) {
		t.Fatalf("%s", b)
	}
}

func TestEmptyRootAndApostrophe(t *testing.T) {
	b, _ := Marshal(MediaContainer{Title1: "Don't"}, XML)
	want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<MediaContainer size=\"0\" title1=\"Don&#39;t\">\n</MediaContainer>\n"
	if string(b) != want {
		t.Fatalf("got %q", b)
	}
}

func TestAlphabeticalDecisionOrder(t *testing.T) {
	st := Stream{ID: 5, StreamType: 1, Codec: "h264", Decision: "copy", Location: "direct", Selected: Ptr(true)}
	st.SetAlphabetical()
	m := Media{ID: 1, VideoCodec: "h264", Container: "mkv", Selected: Ptr(true), Part: []Part{{ID: 2, Decision: "directplay", Selected: Ptr(true), Stream: []Stream{st}}}}
	m.SetAlphabetical()
	m.Part[0].SetAlphabetical()
	b, _ := Marshal(MediaContainer{Size: 1, Metadata: []Metadata{{Type: "movie", Title: "x", Media: []Media{m}}}}, XML)
	s := string(b)
	for _, want := range []string{
		`<Media container="mkv" id="1" videoCodec="h264" selected="1">`,
		`<Part id="2" decision="directplay" selected="1">`,
		`<Stream codec="h264" id="5" streamType="1" decision="copy" selected="1" location="direct" />`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
}
