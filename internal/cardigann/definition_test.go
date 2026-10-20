package cardigann

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseFixtureDefinitions(t *testing.T) {
	for _, name := range []string{"public.yml", "json.yml", "multi.yml", "rss.yml", "private.yml", "post.yml", "cookie.yml", "cf.yml", "usenet.yml", "hash.yml"} {
		def, warnings, err := Parse(readFixture(t, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(warnings) > 0 {
			t.Errorf("%s: unexpected warnings %v", name, warnings)
		}
		if def.ID == "" || def.Name == "" || len(def.Search.Paths) == 0 {
			t.Errorf("%s: incomplete %+v", name, def)
		}
	}
}

func TestParsePublicDetails(t *testing.T) {
	def := loadDef(t, "public.yml")
	if def.Type != "public" || def.Language != "en-US" || len(def.LegacyLinks) != 1 || def.Protocol != "torrent" {
		t.Fatalf("metadata = %+v", def)
	}
	if len(def.Caps.CategoryMappings) != 3 || def.Caps.CategoryMappings[0].Newznab != 2040 || !def.Caps.CategoryMappings[0].Default {
		t.Fatalf("categories = %+v", def.Caps.CategoryMappings)
	}
	if got := def.Caps.Modes["movie-search"]; len(got) != 2 || got[1] != "imdbid" {
		t.Fatalf("modes = %v", def.Caps.Modes)
	}
	if !def.SupportsMovies() || !def.SupportsTV() {
		t.Fatal("supports")
	}
	names := []string{}
	for _, f := range def.Search.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "category,title,details,download,magnet,size,seeders,leechers,grabs,date,imdbid,downloadvolumefactor,uploadvolumefactor" {
		t.Fatalf("field order = %v", names)
	}
	dvf := def.Search.Fields[11].Selector
	if len(dvf.Case) != 2 || dvf.Case[0].Key != `img[alt="Free"]` || dvf.Case[0].Value != "0" || dvf.Case[1].Key != "*" {
		t.Fatalf("case = %+v", dvf.Case)
	}
	if s := def.Settings[0]; s.Type != "select" || s.Default != "added" || len(s.Options) != 2 || s.Options[1].Value != "seeders" {
		t.Fatalf("select = %+v", s)
	}
	if !def.Settings[2].Info() || def.Settings[1].Secret() {
		t.Fatal("setting kinds")
	}
	if p := def.Search.Paths[0]; len(p.Categories) != 2 || p.Categories[0] != "1" || !p.InheritInputs {
		t.Fatalf("path = %+v", p)
	}
	if def.Search.Inputs[0].Key != "$raw" {
		t.Fatalf("inputs = %+v", def.Search.Inputs)
	}
	if def.SiteLink("") != "https://fakepublic.invalid/" || def.SiteLink("https://mirror.invalid") != "https://mirror.invalid/" {
		t.Fatal("site link")
	}
}

func TestLegacyCategoriesAndUsenetProtocol(t *testing.T) {
	multi := loadDef(t, "multi.yml")
	if len(multi.Caps.CategoryMappings) != 1 || multi.Caps.CategoryMappings[0].ID != "1" || multi.Caps.CategoryMappings[0].Newznab != 2000 {
		t.Fatalf("legacy categories = %+v", multi.Caps.CategoryMappings)
	}
	if multi.SupportsTV() {
		t.Fatal("movies-only site claims TV")
	}
	if loadDef(t, "usenet.yml").Protocol != "usenet" {
		t.Fatal("protocol")
	}
}

func TestValidation(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want string
	}{
		{"not: [valid", "not valid YAML"},
		{"- a\n- b\n", "YAML mapping"},
		{"name: X\nlinks: [https://x.invalid/]\nsearch: {path: s, rows: {selector: li}, fields: {title: {selector: a}, magnet: {selector: a}}}", "id is required"},
		{"id: x y\nname: X\nlinks: [https://x.invalid/]\nsearch: {path: s, rows: {selector: li}, fields: {title: {selector: a}, magnet: {selector: a}}}", "id may contain"},
		{"id: x\nname: X\nlinks: [ftp://x.invalid/]\nsearch: {path: s, rows: {selector: li}, fields: {title: {selector: a}, magnet: {selector: a}}}", "not an http(s) URL"},
		{"id: x\nname: X\nlinks: [https://x.invalid/]\nsearch: {rows: {selector: li}, fields: {title: {selector: a}, magnet: {selector: a}}}", "path or paths"},
		{"id: x\nname: X\nlinks: [https://x.invalid/]\nsearch: {path: s, fields: {title: {selector: a}, magnet: {selector: a}}}", "rows.selector"},
		{"id: x\nname: X\nlinks: [https://x.invalid/]\nsearch: {path: s, rows: {selector: li}, fields: {magnet: {selector: a}}}", "include title"},
		{"id: x\nname: X\nlinks: [https://x.invalid/]\nsearch: {path: s, rows: {selector: li}, fields: {title: {selector: a}}}", "download, magnet or infohash"},
		{"id: x\nname: X\nlinks: [https://x.invalid/]\nsearch: {paths: [{path: s, response: {type: csv}}], rows: {selector: li}, fields: {title: {selector: a}, magnet: {selector: a}}}", `response type "csv"`},
		{"id: x\nname: X\nlinks: [https://x.invalid/]\nlogin: {method: oauth}\nsearch: {path: s, rows: {selector: li}, fields: {title: {selector: a}, magnet: {selector: a}}}", `login method "oauth"`},
	} {
		_, _, err := Parse([]byte(tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) err = %v, want %q", tc.yaml, err, tc.want)
		}
	}
	var ve *ValidationError
	_, _, err := Parse([]byte("id: x\n"))
	if !errors.As(err, &ve) || len(ve.Problems) < 3 {
		t.Fatalf("ValidationError = %v", err)
	}
}

func TestUnsupportedFeaturesAreWarnings(t *testing.T) {
	_, warnings, err := Parse([]byte(`id: x
name: X
links: [https://x.invalid/]
caps:
  categorymappings:
    - {id: 1, cat: Movies/Imaginary}
settings:
  - {name: s, type: colourpicker}
search:
  path: s
  rows: {selector: li}
  fields:
    title: {selector: a, filters: [{name: sparkle}]}
    magnet: {selector: a}
download:
  before:
    pathselector: {selector: a}
`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "|")
	for _, want := range []string{`filter "sparkle"`, `unsupported type "colourpicker"`, "unknown Newznab category", "pathselector"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
}

func TestYAMLAnchorsAndScalars(t *testing.T) {
	def, _, err := Parse([]byte(`id: anchors
name: Anchors
links: [https://x.invalid/]
requestDelay: 2.5
search:
  path: s
  inputs:
    page: 0
    enabled: true
  rows: {selector: li}
  fields:
    title: &t {selector: a}
    magnet:
      <<: *t
      attribute: href
`))
	if err != nil {
		t.Fatal(err)
	}
	if def.RequestDelay != 2.5 || def.Search.Inputs[0].Value != "0" || def.Search.Inputs[1].Value != "true" {
		t.Fatalf("scalars = %+v", def.Search.Inputs)
	}
	if m := def.Search.Fields[1].Selector; m.Selector != "a" || m.Attribute != "href" {
		t.Fatalf("merge key = %+v", m)
	}
}

// A merge key that references its own anchor must not recurse until the
// stack overflows (a fatal, unrecoverable crash), and stacked merges must
// not expand exponentially.
func TestYAMLSelfMergeDoesNotRecurseForever(t *testing.T) {
	cyclic := "id: x\nname: x\nlinks: [\"https://a.example/\"]\nsearch: &s\n  <<: *s\n"
	if _, _, err := Parse([]byte(cyclic)); err == nil {
		t.Fatal("definition without search paths parsed")
	}
	var b strings.Builder
	b.WriteString("id: x\nname: x\nlinks: [\"https://a.example/\"]\nl0: &l0 {a: 1, b: 2}\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "l%d: &l%d {<<: *l%d, <<: *l%d}\n", i, i, i-1, i-1)
	}
	b.WriteString("search:\n  <<: *l40\n")
	if _, _, err := Parse([]byte(b.String())); err == nil || strings.Contains(err.Error(), "not valid YAML") {
		t.Fatal("definition without search paths parsed")
	}
}

// Every alias is copied by the walker, so a document of many aliases to one
// large anchor used to expand quadratically: well under the 1 MiB upload
// limit, this one parsed into millions of filters and gigabytes of memory.
func TestYAMLAliasFanOutIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("id: x\nname: X\nlinks: [\"https://a.example/\"]\nsearch:\n  path: s\n  rows: {selector: tr}\n  fields:\n    title:\n      selector: a\n      filters: &f\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("        - {name: trim}\n")
	}
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "    f%d: {selector: a, filters: *f}\n", i)
	}
	b.WriteString("    download: {selector: a}\n")
	if b.Len() > maxDefinitionBytes {
		t.Fatalf("test document is %d bytes", b.Len())
	}
	def, _, err := Parse([]byte(b.String()))
	if err == nil || !strings.Contains(err.Error(), "aliases") {
		n := 0
		if def != nil {
			for _, f := range def.Search.Fields {
				n += len(f.Selector.Filters)
			}
		}
		t.Fatalf("err = %v (parsed %d filters)", err, n)
	}
	// A self-referencing anchor is rejected too.
	if _, _, err := Parse([]byte("id: x\nname: X\nlinks: [\"https://a.example/\"]\nl: &l [*l]\nsearch: {path: s}\n")); err == nil || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("cyclic alias: err = %v", err)
	}
}
