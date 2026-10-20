package cardigann

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// Real definitions use setting names that are not Go identifiers.
func TestRenderNonIdentifierFields(t *testing.T) {
	v := baseVars(map[string]any{"cat-id": "5", "2facode": "123", "sitelink": "http://x/"}, testNow)
	v["Result"] = map[string]any{"title-alt": "Alt"}
	for tpl, want := range map[string]string{
		"{{ .Config.cat-id }}":                                   "5",
		"{{ .Config.2facode }}":                                  "123",
		"c={{ if .Config.cat-id }}{{ .Config.cat-id }}{{ end }}": "c=5",
		"{{ .Result.title-alt }}":                                "Alt",
		`{{ re_replace .Config.cat-id "\d" "n" }}`:               "n",
		`{{ if eq .Config.cat-id ".Config.cat-id" }}x{{ end }}y`: "y", // literal untouched
	} {
		got, err := render(tpl, v, nil)
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", tpl, got, err, want)
		}
	}
}

// .NET named Unicode blocks, used by about a hundred real definitions.
func TestNETUnicodeBlocks(t *testing.T) {
	cases := []struct {
		pattern, in, want string
	}{
		{`[\p{IsCyrillic}\W]+`, "Матрица 1999", "1999"},
		{`\p{IsCyrillic}+`, "abc Матрица", "abc "},
		{`[\p{IsCJKUnifiedIdeographs}\W]+`, "信号.The.Signal", "TheSignal"},
		{`\P{IsBasicLatin}`, "café", "caf"},
	}
	for _, c := range cases {
		re, err := compileRegex(c.pattern)
		if err != nil {
			t.Fatalf("%s: %v", c.pattern, err)
		}
		got, err := re.Replace(c.in, "", -1, -1)
		if err != nil || got != c.want {
			t.Errorf("%s on %q = %q, %v; want %q", c.pattern, c.in, got, err, c.want)
		}
	}
	if _, err := compileRegex(`[\P{IsCyrillic}]`); err == nil {
		t.Error("a negated block inside a class must be reported")
	}
	if _, err := compileRegex(`\p{IsNoSuchBlock}`); err == nil {
		t.Error("unknown block accepted")
	}
}

// YAML 1.2's "\/" escape in double-quoted strings, which go-yaml rejects.
func TestParseAcceptsSlashEscape(t *testing.T) {
	def := `---
id: slashy
name: Slashy
type: public
language: en-US
links:
  - https://slashy.test/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: Movies}
  modes:
    search: [q]
search:
  paths:
    - path: "search\/{{ .Keywords }}"
  rows:
    selector: 'tr.row\/x'
  fields:
    title:
      selector: a
      filters:
        - name: re_replace
          args: ["N\/A", ""]
    download:
      selector: a
      attribute: href
`
	d, _, err := Parse([]byte(def))
	if err != nil {
		t.Fatal(err)
	}
	if d.Search.Paths[0].Path != "search/{{ .Keywords }}" {
		t.Errorf("path %q", d.Search.Paths[0].Path)
	}
	if d.Search.Rows.Selector != `tr.row\/x` {
		t.Errorf("single-quoted text changed: %q", d.Search.Rows.Selector)
	}
	if got := d.Search.Fields[0].Selector.Filters[0].Args[0]; got != "N/A" {
		t.Errorf("filter arg %q", got)
	}
}

// A templated search method: POST with keywords, GET without.
func TestTemplatedSearchMethod(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.Write([]byte(`<html><body><table></table></body></html>`))
	}))
	defer srv.Close()
	def, _, err := Parse([]byte(`---
id: methody
name: Methody
type: public
language: en-US
links:
  - https://methody.test/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: Movies}
  modes:
    search: [q]
search:
  paths:
    - path: search
      method: "{{ if .Keywords }}post{{ else }}get{{ end }}"
  inputs:
    q: "{{ .Keywords }}"
  rows:
    selector: tr
  fields:
    title:
      selector: a
    download:
      selector: a
      attribute: href
`))
	if err != nil {
		t.Fatal(err)
	}
	r := testRunner(t, def, srv.URL+"/", runnerSetup{})
	if _, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "The Signal"}, nil, nil, nil); err != nil && !strings.Contains(err.Error(), "no results") {
		t.Logf("search: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 || methods[0] != http.MethodPost {
		t.Fatalf("methods %v, want POST for a keyword search", methods)
	}
}
