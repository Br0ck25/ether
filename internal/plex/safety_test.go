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
		if r.Code != 200 || strings.Contains(r.Body.String(), `title="Fjord Harbor"`) {
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

type keywordPolicy struct{ blocked string }

func (p keywordPolicy) Allowed(u *core.User, t *core.Title) bool {
	if u == nil || u.IsOwner {
		return true
	}
	for _, k := range t.Keywords {
		if k == p.blocked {
			return false
		}
	}
	return true
}

// Seasons and episodes do not carry the show's keywords, so a show blocked
// by owner policy must also hide its children when addressed directly.
func TestBlockedShowChildrenStayHidden(t *testing.T) {
	f := newFixture(func(d *Deps) { d.AccessPolicy = keywordPolicy{blocked: "blocked"} })
	defer f.srv.Close()
	show := breakingBad
	show.Keywords = []string{"blocked"}
	f.catalog.titles[show.Key] = show
	ep, season := bbE1.Key.String(), bbS1.Key.String()
	for _, path := range []string{
		"/library/metadata/" + show.Key.String(),
		"/library/metadata/" + ep,
		"/library/metadata/" + season,
		"/library/metadata/" + season + "/children",
		"/library/metadata/" + season + "/allLeaves",
		"/library/metadata/" + ep + "/parent",
		"/video/:/transcode/universal/decision?path=%2Flibrary%2Fmetadata%2F" + ep,
		"/video/:/transcode/universal/start.m3u8?path=%2Flibrary%2Fmetadata%2F" + ep + "&session=blocked-ep",
	} {
		if rec := f.get(t, path, "X-Plex-Token", friendToken); rec.Code != 404 {
			t.Errorf("%s: status %d body %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := f.request(t, http.MethodPost, "/playQueues?type=video&uri="+url.QueryEscape("server://x/com.plexapp.plugins.library/library/metadata/"+ep), map[string]string{"X-Plex-Token": friendToken})
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "Pilot") {
		t.Errorf("play queue of a blocked episode: %d %s", rec.Code, rec.Body.String())
	}
	// The owner is unaffected.
	if rec := f.get(t, "/library/metadata/"+ep); rec.Code != 200 {
		t.Errorf("owner episode: %d", rec.Code)
	}
}

// Play queues and playlists render only titles the viewer may see.
func TestRestrictedViewerQueuesAndPlaylistsHideTitles(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Auth.(*fakeAuth).users["kid"] = core.User{ID: 30, Username: "kid", MaxContentRating: "PG"}
	pg := alien
	pg.ContentRating = "PG"
	f.catalog.titles[pg.Key] = pg
	uri := url.QueryEscape("server://x/com.plexapp.plugins.library/library/metadata/" + fightClub.Key.String())
	rec := f.request(t, http.MethodPost, "/playQueues?type=video&uri="+uri, map[string]string{"X-Plex-Token": "kid"})
	if strings.Contains(rec.Body.String(), "Fjord Harbor") {
		t.Errorf("play queue leaked a hidden title: %d %s", rec.Code, rec.Body.String())
	}
	// A playlist holding an item from before the restriction applied.
	pl, err := f.pls.Create(context.Background(), 30, "Mine", []core.RatingKey{fightClub.Key, alien.Key})
	if err != nil {
		t.Fatal(err)
	}
	rec = f.get(t, "/playlists/"+pl.Key.String()+"/items", "X-Plex-Token", "kid")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "Fjord Harbor") || !strings.Contains(rec.Body.String(), "Alien") {
		t.Errorf("playlist items: %d %s", rec.Code, rec.Body.String())
	}
}
