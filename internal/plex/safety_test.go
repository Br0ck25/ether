package plex

import (
	"context"
	"database/sql"
	"github.com/braedonsaunders/ether/internal/core"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRestrictedViewerCannotRetrieveHiddenTitles(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Auth.(*fakeAuth).users["kid"] = core.User{ID: 30, Username: "kid", MaxContentRating: "PG"}
	key := fightClub.Key.String()
	for _, path := range []string{"/library/metadata/" + key, "/library/metadata/" + key + "/thumb/1", "/library/parts/" + key + "/1/movie.mkv", "/photo/:/transcode?url=" + url.QueryEscape("/library/metadata/"+key+"/thumb/1"), "/library/metadata/" + breakingBad.Key.String() + "/children"} {
		r := f.request(t, http.MethodGet, path, map[string]string{"X-Plex-Token": "kid"})
		if r.Code != 404 {
			t.Errorf("%s status %d body %s", path, r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"/hubs", "/hubs/search?query=Fight", "/library/sections/1/all", "/hubs/sections/1"} {
		r := f.request(t, http.MethodGet, path, map[string]string{"X-Plex-Token": "kid"})
		if r.Code != 200 || strings.Contains(r.Body.String(), `title="Fight Club"`) {
			t.Errorf("%s leaked title %s", path, r.Body.String())
		}
	}
}
func TestAdultTitlesHiddenEvenFromOwner(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	x := fightClub
	x.Adult = true
	f.catalog.titles[x.Key] = x
	r := f.get(t, "/library/metadata/"+x.Key.String())
	if r.Code != 404 {
		t.Fatalf("adult metadata %d", r.Code)
	}
}

func TestRestrictedAbsoluteArtworkRequiresVisibleTitle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE catalog_titles(key INTEGER,adult INTEGER,poster TEXT,backdrop TEXT,still TEXT,logo TEXT); INSERT INTO catalog_titles VALUES(?,0,'/visible.jpg','','',''),(?,0,'/hidden.jpg','','','')`, core.MovieKey(999), fightClub.Key)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.DB = db
	f.catalog.titles[core.MovieKey(999)] = core.Title{Key: core.MovieKey(999), Type: core.TypeMovie, ContentRating: "PG"}
	r := httptest.NewRequest("GET", "/photo/:/transcode", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, &Request{User: &core.User{ID: 30, MaxContentRating: "PG"}}))
	for _, c := range []struct {
		path    string
		allowed bool
	}{{"https://image.tmdb.org/t/p/w500/visible.jpg", true}, {"https://image.tmdb.org/t/p/w500/hidden.jpg", false}, {"https://image.tmdb.org/t/p/w500/unknown.jpg", false}} {
		u, _ := url.Parse(c.path)
		if got := f.srv.remoteArtworkAllowed(r, u); got != c.allowed {
			t.Errorf("%s allowed=%v", c.path, got)
		}
	}
}
