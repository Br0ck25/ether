package cardigann

import (
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func TestFilters(t *testing.T) {
	env := filterEnv{vars: testVars(), now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	f := func(name string, args ...string) []Filter { return []Filter{{Name: name, Args: args}} }
	for _, tc := range []struct {
		name    string
		in      string
		filters []Filter
		want    string
	}{
		{"querystring", "/browse.php?cat=7&id=42", f("querystring", "cat"), "7"},
		{"querystring absolute", "https://x.test/d?id=42", f("querystring", "id"), "42"},
		{"regexp group", "Size: 1.4 GB", f("regexp", `Size: (.+)`), "1.4 GB"},
		{"regexp no match", "abc", f("regexp", `(\d+)`), ""},
		{"regexp lookbehind", "id=55", f("regexp", `(?<=id=)(\d+)`), "55"},
		{"re_replace", "a.b.c", f("re_replace", `\.`, " "), "a b c"},
		{"re_replace groups", "2024-03-07", f("re_replace", `(\d+)-(\d+)-(\d+)`, "$3/$2/$1"), "07/03/2024"},
		{"split", "a|b|c", f("split", "|", "1"), "b"},
		{"split negative", "a|b|c", f("split", "|", "-1"), "c"},
		{"split out of range", "a|b", f("split", "|", "5"), ""},
		{"replace", "x-y-z", f("replace", "-", "+"), "x+y+z"},
		{"trim", "  x  ", f("trim"), "x"},
		{"trim cutset", "[x]", f("trim", "[]"), "x"},
		{"prepend", "path", f("prepend", "{{ .Config.sort }}/"), "seeders/path"},
		{"append", "x", f("append", " GB"), "x GB"},
		{"tolower", "AbC", f("tolower"), "abc"},
		{"toupper", "AbC", f("toupper"), "ABC"},
		{"urldecode", "a%20b+c", f("urldecode"), "a b c"},
		{"urlencode", "a b&c", f("urlencode"), "a+b%26c"},
		{"htmldecode", "Tom &amp; Jerry &#39;s", f("htmldecode"), "Tom & Jerry 's"},
		{"htmlencode", "<b>", f("htmlencode"), "&lt;b&gt;"},
		{"dateparse go", "2024-03-07 14:05", f("dateparse", "2006-01-02 15:04"), "2024-03-07T14:05:00Z"},
		{"dateparse dotnet", "07/03/2024", f("dateparse", "dd/MM/yyyy"), "2024-03-07T00:00:00Z"},
		{"timeparse", "14:05 07.03.2024", f("timeparse", "15:04 02.01.2006"), "2024-03-07T14:05:00Z"},
		{"timeago", "2 hours ago", f("timeago"), "2026-10-19T10:00:00Z"},
		{"reltime", "3 days", f("reltime"), "2026-10-16T12:00:00Z"},
		{"fuzzytime", "Yesterday 10:00", f("fuzzytime"), "2026-10-18T10:00:00Z"},
		{"diacritics", "Amélie Poulain çà", f("diacritics", "replace"), "Amelie Poulain ca"},
		{"jsonjoinarray", `{"genres":["Drama","Sci-Fi"]}`, f("jsonjoinarray", "$.genres", ", "), "Drama, Sci-Fi"},
		{"jsonjoinarray objects", `[{"n":"a"},{"n":"b"}]`, f("jsonjoinarray", "$[*].n", "|"), "a|b"},
		{"validfilename", `a:b/c?.mkv`, f("validfilename"), "abc.mkv"},
		{"validate", "Action, Comedy, Unknown / Drama", f("validate", "action, drama, horror"), "action,drama"},
		{"strdump passthrough", "x", f("strdump"), "x"},
		{"hexdump passthrough", "x", f("hexdump"), "x"},
		{"unknown skipped", "x", f("nosuchfilter"), "x"},
		{"chain", " /dl.php?id=9 ", []Filter{{Name: "trim"}, {Name: "querystring", Args: []string{"id"}}, {Name: "prepend", Args: []string{"#"}}}, "#9"},
	} {
		got, err := applyFilters(tc.in, tc.filters, env)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := applyFilters("bad", f("dateparse", "2006-01-02"), env); err == nil {
		t.Error("dateparse accepted a non-matching value")
	}
}

func TestAndMatch(t *testing.T) {
	if !andMatch("The.Matrix.1999.1080p", "matrix 1999", 0) {
		t.Error("expected match")
	}
	if andMatch("The.Matrix.Reloaded", "matrix 1999", 0) {
		t.Error("expected no match")
	}
	if !andMatch("The.Matrix.Reloaded", "matrix 1999", 6) {
		t.Error("limit should cut the query")
	}
}

func TestJSONSelectors(t *testing.T) {
	root, err := decodeJSON([]byte(`{"data":{"total":2,"torrents":[
		{"name":"A","cached":true,"files":[{"n":"a1"}],"meta":{"imdb":"tt1"},"size":1.5},
		{"name":"B","cached":false,"meta":{}},
		{"name":"C","cached":true,"tags":["x","free"]}
	]},"list":["p","q"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ sel, want string }{
		{"data.total", "2"},
		{"$.data.total", "2"},
		{"data.torrents[0].name", "A"},
		{"data.torrents[-1].name", "C"},
		{"data.torrents[0].cached", "True"},
		{"data.torrents[1].cached", "False"},
		{"data.torrents[0].size", "1.5"},
		{"data.torrents[0]['meta'].imdb", "tt1"},
		{"list", `["p","q"]`},
		{"data.torrents[*].name", `["A","B","C"]`},
	} {
		v, ok, err := jsonSelect(root, tc.sel)
		if err != nil || !ok {
			t.Errorf("jsonSelect(%q) ok=%v err=%v", tc.sel, ok, err)
			continue
		}
		if got := jsonString(v); got != tc.want {
			t.Errorf("jsonSelect(%q) = %q, want %q", tc.sel, got, tc.want)
		}
	}
	for sel, want := range map[string]int{
		"data.torrents": 3,
		"data.torrents:has(cached:contains(True))":           2,
		"data.torrents:not(meta)":                            1,
		"data.torrents:has(meta.imdb)":                       1,
		"data.torrents:has(tags:contains(free))":             1,
		"data.torrents:has(cached:contains(True)):not(tags)": 1,
	} {
		rows, ok, err := jsonRows(root, sel)
		if err != nil || !ok || len(rows) != want {
			t.Errorf("jsonRows(%q) = %d rows ok=%v err=%v, want %d", sel, len(rows), ok, err, want)
		}
	}
	if _, ok, _ := jsonRows(root, "data.missing"); ok {
		t.Error("missing rows should report !ok")
	}
	if _, _, err := jsonSelect(root, "data:bogus(x)"); err == nil {
		t.Error("unknown JSON filter accepted")
	}
}

func TestXMLDocumentSelectors(t *testing.T) {
	doc, err := parseXMLDocument([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Show.S01E02.720p</title><link>https://x.test/dl/1</link><pubDate>Thu, 07 Mar 2024 14:05:09 +0000</pubDate>
<enclosure url="https://x.test/dl/1.torrent" length="1024" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="12"/><torznab:attr name="infohash" value="ABCDEF"/></item>
<item><title><![CDATA[Other & Co]]></title></item>
</channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	items := doc.Find("item")
	if items.Length() != 2 {
		t.Fatalf("items = %d", items.Length())
	}
	first := items.First()
	env := filterEnv{vars: testVars(), now: time.Now()}
	for _, tc := range []struct {
		sel  Selector
		want string
	}{
		{Selector{Selector: "title"}, "Show.S01E02.720p"},
		{Selector{Selector: "link"}, "https://x.test/dl/1"},
		{Selector{Selector: "pubDate"}, "Thu, 07 Mar 2024 14:05:09 +0000"},
		{Selector{Selector: "enclosure", Attribute: "url"}, "https://x.test/dl/1.torrent"},
		{Selector{Selector: `torznab\:attr[name="seeders"]`, Attribute: "value"}, "12"},
	} {
		got, ok, err := htmlValue(tc.sel, first, env)
		if err != nil || !ok || got != tc.want {
			t.Errorf("%q = %q ok=%v err=%v, want %q", tc.sel.Selector, got, ok, err, tc.want)
		}
	}
	if got, _, _ := htmlValue(Selector{Selector: "title"}, items.Last(), env); got != "Other & Co" {
		t.Errorf("CDATA title = %q", got)
	}
}

func TestHTMLValueRules(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<table><tr class="row">
<td class="name"><a href="/details.php?id=5">Movie <span class="tag">NEW</span></a></td>
<td class="free"><img src="freeleech.png"></td>
</tr></table>`))
	if err != nil {
		t.Fatal(err)
	}
	row := doc.Find("tr.row")
	env := filterEnv{vars: testVars(), now: time.Now()}
	if got, _, _ := htmlValue(Selector{Selector: "td.name a", Remove: "span"}, row, env); got != "Movie" {
		t.Errorf("remove: %q", got)
	}
	// The removal must not alter the document for later fields.
	if got, _, _ := htmlValue(Selector{Selector: "td.name a"}, row, env); got != "Movie NEW" {
		t.Errorf("after remove: %q", got)
	}
	got, ok, _ := htmlValue(Selector{Case: []KV{{Key: `img[src*="freeleech"]`, Value: "0"}, {Key: "*", Value: "1"}}}, row, env)
	if !ok || got != "0" {
		t.Errorf("case = %q", got)
	}
	if got, _, _ := htmlValue(Selector{Selector: "tr", Attribute: "class"}, row, env); got != "row" {
		t.Errorf("self match = %q", got)
	}
	if _, ok, _ := htmlValue(Selector{Selector: "td.size"}, row, env); ok {
		t.Error("missing selector reported found")
	}
	if _, ok, _ := htmlValue(Selector{Selector: "a", Attribute: "title"}, row, env); ok {
		t.Error("missing attribute reported found")
	}
	if got, _, _ := htmlValue(Selector{Text: "{{ .Config.sort }}", HasText: true, Filters: []Filter{{Name: "toupper"}}}, row, env); got != "SEEDERS" {
		t.Errorf("text = %q", got)
	}
	if _, _, err := htmlValue(Selector{Selector: "td[["}, row, env); err == nil {
		t.Error("bad selector accepted")
	}
}
