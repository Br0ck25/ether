package cardigann

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func testVars() vars {
	v := baseVars(map[string]any{"freeleech": "True", "off": "", "sort": "seeders", "cats": []string{"a", "b"}}, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	v["Keywords"] = "The Signal 1999"
	v["Query"] = map[string]string{"IMDBID": "tt0133093", "IMDBIDShort": "0133093", "Season": "", "Ep": "", "Keywords": "The Signal 1999"}
	v["Categories"] = []string{"1", "7"}
	v["Result"] = map[string]string{"title_phase1": "Some.Title", "empty": ""}
	return v
}

func TestRender(t *testing.T) {
	v := testVars()
	for _, tc := range []struct{ tpl, want string }{
		{"plain text", "plain text"},
		{"{{ .Keywords }}", "The Signal 1999"},
		{"{{ if .Config.freeleech }}fl=1{{ else }}fl=0{{ end }}", "fl=1"},
		{"{{ if .Config.off }}on{{ else }}off{{ end }}", "off"},
		{"{{if .Query.IMDBID}}imdb={{ .Query.IMDBIDShort }}{{else}}q={{ .Keywords }}{{end}}", "imdb=0133093"},
		{"{{ range .Categories }}cat[]={{.}}&{{end}}", "cat[]=1&cat[]=7&"},
		{"{{ range $i, $c := .Categories }}{{ $i }}:{{ $c }} {{ end }}", "0:1 1:7 "},
		{"{{ join .Categories \",\" }}", "1,7"},
		{`{{ re_replace .Keywords "\s+" "." }}`, "The.Signal.1999"},
		{`{{ re_replace .Keywords "[^a-zA-Z0-9]+" "%" }}`, "The%Signal%1999"},
		{`{{ re_replace .Keywords "(\w+) (\w+)" "$2-$1" }}`, "Signal-The 1999"},
		{"{{ .Result.title_phase1 }}", "Some.Title"},
		{"[{{ .Result.missing }}]", "[]"},
		{"[{{ .Config.missing }}]", "[]"},
		{"[{{ .Query.Season }}]", "[]"},
		{`{{ if eq .Config.sort "seeders" }}s{{ else }}d{{ end }}`, "s"},
		{`{{ if ne .Config.missing "x" }}ok{{ end }}`, "ok"},
		{`{{ if and .Config.freeleech .Keywords }}both{{ end }}`, "both"},
		{`{{ if or .Query.Season .Query.IMDBID }}any{{ end }}`, "any"},
		{`{{ or .Query.Season .Keywords }}`, "The Signal 1999"},
		{"{{ .Today.Year }}", "2026"},
		{"{{ .True }}|{{ .False }}", "True|"},
		{"{{ join .Config.cats \"+\" }}", "a+b"},
		{"{{- .Keywords -}}", "The Signal 1999"},
		{"{{ if .Result.empty }}x{{ else if .Result.title_phase1 }}y{{ end }}", "y"},
	} {
		got, err := render(tc.tpl, v, nil)
		if err != nil {
			t.Errorf("render(%q): %v", tc.tpl, err)
			continue
		}
		if got != tc.want {
			t.Errorf("render(%q) = %q, want %q", tc.tpl, got, tc.want)
		}
	}
}

func TestRenderModifierEncodesValuesOnly(t *testing.T) {
	v := testVars()
	got, err := render("search/{{ .Keywords }}/{{ if .Config.freeleech }}free only{{ end }}", v, url.QueryEscape)
	if err != nil {
		t.Fatal(err)
	}
	if got != "search/The+Signal+1999/free only" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderErrors(t *testing.T) {
	if _, err := render("{{ if .Keywords }}unterminated", testVars(), nil); err == nil {
		t.Fatal("expected a parse error")
	}
	if _, err := render(`{{ re_replace .Keywords "(" "x" }}`, testVars(), nil); err == nil || !strings.Contains(err.Error(), "regular expression") {
		t.Fatalf("expected a regex error, got %v", err)
	}
}

func TestTranslateKeepsBackticksAndQuotes(t *testing.T) {
	got := translate("a{{ re_replace .K \"x`y\" `z` }}b")
	if !strings.Contains(got, "\"x`y\"") || !strings.Contains(got, "`z`") {
		t.Fatalf("translate = %q", got)
	}
}
