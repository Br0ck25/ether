package catalog_test

import (
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFeedMatchesAreLocalUnambiguousAndRespectIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("feed matching fetched remote metadata")
		http.Error(w, "unexpected", 500)
	}))
	defer srv.Close()
	c := newTestCatalog(t, srv)
	ctx := t.Context()
	film := core.Title{Key: core.MovieKey(9), Type: core.TypeMovie, Title: "Premiere", Year: 2020, IDs: core.IDs{TMDB: 9, IMDB: "tt0000009"}, Released: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	if e := c.UpsertTitle(ctx, film, true); e != nil {
		t.Fatal(e)
	}
	releases := []core.Release{{ID: "good", Name: "Premiere.2020.1080p.WEB-DL"}, {ID: "conflict", Name: "Premiere.2020.1080p.WEB-DL", TitleIDs: core.IDs{TMDB: 8}}, {ID: "noyear", Name: "Premiere.1080p.WEB-DL"}, {ID: "id", Name: "Localized.2020.1080p.WEB-DL", TitleIDs: core.IDs{TMDB: 9}}}
	for i := range releases {
		releases[i].Info = releaseparse.Parse(releases[i].Name)
	}
	got, e := c.ReleaseMatches(ctx, releases)
	if e != nil {
		t.Fatal(e)
	}
	if len(got["good"]) != 1 || len(got["id"]) != 1 || len(got["conflict"]) != 0 || len(got["noyear"]) != 0 {
		t.Fatalf("bad matches: %+v", got)
	}
	// Same title/year with no IDs must become ambiguous, never guess.
	film.Key = core.MovieKey(10)
	film.IDs = core.IDs{TMDB: 10}
	if e = c.UpsertTitle(ctx, film, true); e != nil {
		t.Fatal(e)
	}
	got, e = c.ReleaseMatches(ctx, releases)
	if e != nil || len(got["good"]) != 0 || len(got["id"]) != 1 {
		t.Fatal(got, e)
	}
}
