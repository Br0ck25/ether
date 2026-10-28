package collections_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

// A My Lists Addon install link carries the owner's config id; the addon
// serves a manifest and skip-paged catalogs, with search catalogs and
// virtual channels mixed in.
func TestStremioCatalogSource(t *testing.T) {
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cfg123/manifest.json":
			fmt.Fprint(w, `{"name":"My Lists","catalogs":[
				{"type":"movie","id":"mine","name":"Mine","extra":[{"name":"skip"}]},
				{"type":"series","id":"shows","name":"Shows"},
				{"type":"movie","id":"search_movies","name":"Movies","extra":[{"name":"search","isRequired":true}],"extraRequired":["search"]}]}`)
		case "/cfg123/catalog/movie/mine.json":
			fmt.Fprint(w, `{"metas":[{"id":"tmdb:101","type":"movie","name":"Space Saga"},
				{"id":"tt0000105","type":"movie","name":"Prison Hope","releaseInfo":"1994"},
				{"id":"channel_90s","type":"movie","name":"90s Channel"}]}`)
		case "/cfg123/catalog/movie/mine/skip=100.json":
			fmt.Fprint(w, `{"metas":[{"id":"tmdb:101","type":"movie","name":"Space Saga"},{"id":"tmdb:108","type":"movie","name":"Knight Returns"}]}`)
		case "/cfg123/catalog/movie/mine/skip=200.json":
			fmt.Fprint(w, `{"metas":[]}`)
		case "/cfg123/catalog/series/shows.json":
			fmt.Fprint(w, `{"metas":[{"id":"tmdb:502","type":"series","name":"Office Life"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	e := newEnv(t, withOpts(collections.WithUserAgent("Ether/test")))
	preview := func(src core.CollectionSource) *core.CollectionPreview {
		t.Helper()
		sp := spec("addon", "mixed", []core.CollectionSource{src})
		sp.Sort, sp.SortDesc = "source", false
		p, err := e.svc.Preview(e.ctx, sp, 60)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A single catalog URL pages through skip, drops repeats and channels.
	one := preview(core.CollectionSource{Kind: "stremioCatalog", URL: ext.URL + "/cfg123/catalog/movie/mine.json"})
	if got := strings.Join(titlesOf(one), ","); got != "Space Saga,Prison Hope,Knight Returns" {
		t.Fatalf("catalog url: %s %v", got, one.Warnings)
	}
	// An install link plus a catalog id; the manifest supplies the type,
	// and stremio:// is accepted.
	series := preview(core.CollectionSource{Kind: "stremioCatalog", URL: ext.URL + "/cfg123/manifest.json", Params: map[string]string{"catalog": "shows"}})
	if got := strings.Join(titlesOf(series), ","); got != "Office Life" {
		t.Fatalf("manifest + catalog: %s %v", got, series.Warnings)
	}
	// With no catalog chosen the warning lists the choices, not the search catalogs.
	none := preview(core.CollectionSource{Kind: "stremioCatalog", URL: ext.URL + "/cfg123/manifest.json"})
	w := strings.Join(none.Warnings, " ")
	if none.Total != 0 || !strings.Contains(w, "movie/mine") || !strings.Contains(w, "series/shows") || strings.Contains(w, "search_movies") {
		t.Fatalf("catalog choices: %+v", none)
	}
	// Errors name the host but never the owner's config segment.
	gone := preview(core.CollectionSource{Kind: "stremioCatalog", URL: ext.URL + "/cfg123/catalog/movie/gone.json"})
	if gone.Total != 0 || len(gone.Warnings) == 0 || strings.Contains(strings.Join(gone.Warnings, " "), "cfg123") {
		t.Fatalf("missing catalog: %+v", gone)
	}

	for url, ok := range map[string]bool{
		"stremio://example.com/cfg/manifest.json":       true,
		"https://example.com/cfg/catalog/series/x.json": true,
		"": false, "ftp://example.com/manifest.json": false, "not a url": false,
	} {
		sp := spec("v", "mixed", []core.CollectionSource{{Kind: "stremioCatalog", URL: url}})
		sp.Artwork.Mode = "auto"
		if err := collections.Validate(&sp); (err == nil) != ok {
			t.Errorf("validate %q: %v", url, err)
		}
	}
}
