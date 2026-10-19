package tmdb

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalizedMetadataPreservesCanonicalSafetyAndLanguage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/movie/1" || r.URL.Query().Get("language") != "fr-CA" || r.URL.Query().Get("include_image_language") != "fr,null" {
			t.Errorf("wrong localization request %s", r.URL.String())
		}
		fmt.Fprint(w, `{"title":"Le film","overview":"Résumé","adult":true,"poster_path":"/fr.jpg","images":{"backdrops":[{"file_path":"/textless.jpg","iso_639_1":null}],"logos":[{"file_path":"/logo.png","iso_639_1":"fr"}]}}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "key")
	original := &core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, IDs: core.IDs{TMDB: 1}, Title: "Movie", Summary: "Summary", ContentRating: "PG"}
	v, e := c.Localized(context.Background(), original, "fr-CA")
	if e != nil || v.Title != "Le film" || v.LogoPath != "/logo.png" || v.BackdropPath != "/textless.jpg" || v.Adult || v.ContentRating != "PG" {
		t.Fatalf("bad translation %v %v", v, e)
	}
	if original.Title != "Movie" || calls != 1 {
		t.Fatal("canonical title mutated")
	}
	if _, e = c.Localized(context.Background(), original, "../../evil"); e != nil || calls != 1 {
		t.Fatal("invalid language triggered request")
	}
}
