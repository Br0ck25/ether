package pmsconform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareDetectsStructureAndShape(t *testing.T) {
	cases := []struct{ name, a, b, kind string }{{"missing attr", `<MediaContainer size="1"><Video id="4"/></MediaContainer>`, `<MediaContainer size="1"><Video/></MediaContainer>`, `missing`}, {"type change", `<MediaContainer size="1"/>`, `<MediaContainer size="one"/>`, `type`}, {"extra element", `<MediaContainer/>`, `<MediaContainer><Video/></MediaContainer>`, `extra`}, {"changed ordering", `<MediaContainer><Directory/><Video/></MediaContainer>`, `<MediaContainer><Video/><Directory/></MediaContainer>`, `value`}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Compare(Response{Status: 200, Body: []byte(tc.a)}, Response{Status: 200, Body: []byte(tc.b)})
			found := false
			for _, d := range r.Differences {
				if d.Kind == tc.kind {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s difference: %+v", tc.kind, r)
			}
		})
	}
}
func TestJSONComparisonAndVolatileShape(t *testing.T) {
	a := Response{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"MediaContainer":{"size":1,"machineIdentifier":"abc","Video":[{"id":4}]}}`)}
	b := a
	b.Body = []byte(`{"MediaContainer":{"size":"1","machineIdentifier":"xyz","Video":[{"id":8}],"extra":true}}`)
	r := Compare(a, b)
	foundType, foundExtra := false, false
	for _, d := range r.Differences {
		foundType = foundType || d.Kind == "type"
		foundExtra = foundExtra || d.Kind == "extra"
	}
	if !foundType || !foundExtra {
		t.Fatalf("expected type and extra diffs: %+v", r)
	}
}
func TestRedact(t *testing.T) {
	s := string(Redact([]byte(`X-Plex-Token=secret machineIdentifier="machine" user="alice" email=alice@example.com ip=192.168.1.4`)))
	for _, v := range []string{"secret", "machine\"", "alice@example.com", "192.168.1.4"} {
		if strings.Contains(s, v) {
			t.Errorf("unredacted %q in %s", v, s)
		}
	}
}
func TestCorpusFetchesSameTemplatedIDsForServers(t *testing.T) {
	var paths []string
	makeServer := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Path == "/library/sections" {
				w.Write([]byte(`<MediaContainer><Directory key="` + id + `"/></MediaContainer>`))
			} else {
				w.Write([]byte(`<MediaContainer/>`))
			}
		}))
	}
	a, b := makeServer("3"), makeServer("7")
	defer a.Close()
	defer b.Close()
	ctx := context.Background()
	req := Corpus()[3]
	for _, srv := range []*httptest.Server{a, b} {
		if _, e := Fetch(ctx, srv.Client(), srv.URL, "", req, "web", map[string]string{"section": "3", "ratingKey": "42"}, false); e != nil {
			t.Fatal(e)
		}
	}
	if len(paths) != 2 || paths[0] != paths[1] || !strings.Contains(paths[0], "/3/all") {
		t.Fatalf("request paths not consistent: %v", paths)
	}
}
