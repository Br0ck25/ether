package cardigann

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzParse feeds arbitrary YAML to the definition parser: it must not
// panic, hang or blow up memory.
func FuzzParse(f *testing.F) {
	paths, _ := filepath.Glob(filepath.Join("testdata", "*.yml"))
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("a: &a {<<: *a}\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _ = Parse(b)
	})
}

// FuzzRender runs arbitrary template text through the Cardigann-to-Go
// template translation and execution.
func FuzzRender(f *testing.F) {
	for _, s := range []string{
		"{{ .Keywords }}", `{{ re_replace .Keywords "\s+" "." }}`, "{{- if .Config.x -}}a{{ else }}b{{ end }}",
		"{{ join .Categories \",\" }}", "{{ .Config.cat-id }}", "{{ \"a`b\" }}", "{{ range .Categories }}{{.}}{{end}}",
	} {
		f.Add(s)
	}
	v := baseVars(map[string]any{"x": "True", "cat-id": "7"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = render(s, v.clone(), nil)
	})
}
