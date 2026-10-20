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
	"time"
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

// Up Next and a show's On Deck must not surface an episode of a show the
// viewer's policy blocks, even when the episode lacks the show's keywords.
func TestBlockedShowEpisodesStayOutOfUpNextAndOnDeck(t *testing.T) {
	f := newFixture(func(d *Deps) { d.AccessPolicy = keywordPolicy{blocked: "blocked"} })
	defer f.srv.Close()
	show := breakingBad
	show.Keywords = []string{"blocked"}
	f.catalog.titles[show.Key] = show
	rec := f.get(t, "/hubs/metadata/"+bbE1.Key.String()+"/postplay", "X-Plex-Token", friendToken)
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "Second Watch") || strings.Contains(rec.Body.String(), "tv.upnext") {
		t.Errorf("postplay: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.get(t, "/library/metadata/"+show.Key.String()+"/onDeck", "X-Plex-Token", friendToken)
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "Pilot") {
		t.Errorf("show onDeck: %d %s", rec.Code, rec.Body.String())
	}
	// The owner still gets both.
	if rec := f.get(t, "/hubs/metadata/"+bbE1.Key.String()+"/postplay"); !strings.Contains(rec.Body.String(), "Second Watch") {
		t.Errorf("owner postplay lost Up Next: %s", rec.Body.String())
	}
	if rec := f.get(t, "/library/metadata/"+show.Key.String()+"/onDeck"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Pilot") {
		t.Errorf("owner onDeck: %d %s", rec.Code, rec.Body.String())
	}
}

type historyState struct {
	*fakeState
	hist []core.HistoryEntry
}

func (h historyState) History(context.Context, int64, time.Time, int) ([]core.HistoryEntry, error) {
	return h.hist, nil
}

// Watch history must not list titles the viewer may no longer see.
func TestHistoryHidesRestrictedTitles(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Auth.(*fakeAuth).users["kid"] = core.User{ID: 30, Username: "kid", MaxContentRating: "PG"}
	pg := alien
	pg.ContentRating = "PG"
	f.catalog.titles[pg.Key] = pg
	f.srv.d.State = historyState{fakeState: f.state, hist: []core.HistoryEntry{
		{UserID: 30, Key: fightClub.Key, ViewedAt: fixedNow}, {UserID: 30, Key: pg.Key, ViewedAt: fixedNow},
	}}
	rec := f.get(t, "/status/sessions/history/all", "X-Plex-Token", "kid")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "Fjord Harbor") || !strings.Contains(rec.Body.String(), "Alien") {
		t.Errorf("history: %d %s", rec.Code, rec.Body.String())
	}
}

// A collection without its own poster falls back to an item's poster, but
// never to one the viewer may not see.
func TestCollectionPosterFallbackSkipsHiddenItems(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Auth.(*fakeAuth).users["kid"] = core.User{ID: 30, Username: "kid", MaxContentRating: "PG"}
	pg := alien
	pg.ContentRating = "PG"
	f.catalog.titles[pg.Key] = pg
	key := core.CollectionKey(77)
	f.cols.cols[key] = core.Collection{Key: key, Title: "Mixed", Kind: "franchise", Type: core.TypeMovie, ChildCount: 2}
	f.cols.items[key] = []core.Title{fightClub, pg}
	rec := f.get(t, "/library/collections/"+key.String()+"/thumb/1", "X-Plex-Token", "kid")
	if rec.Code != 200 {
		t.Fatalf("collection thumb: %d", rec.Code)
	}
	if f.art.count("/t/p/w500/poster550.jpg") != 0 || f.art.count("/t/p/w500/alien.jpg") != 1 {
		t.Fatalf("fallback poster fetched %v", f.art.hits)
	}
}

// On Deck pairs rendered items with their titles: a hidden in-progress
// title must not shift another item's section attributes.
func TestOnDeckHiddenTitleKeepsSectionAttributes(t *testing.T) {
	f := newFixture(func(d *Deps) { d.AccessPolicy = keywordPolicy{blocked: "blocked"} })
	defer f.srv.Close()
	movie := fightClub
	movie.Keywords = []string{"blocked"}
	f.catalog.titles[movie.Key] = movie
	f.state.set(friendUser.ID, core.WatchStatus{Key: movie.Key, ViewOffset: time.Minute})
	f.state.set(friendUser.ID, core.WatchStatus{Key: bbE1.Key, ViewOffset: time.Minute})
	rec := f.get(t, "/library/onDeck", "X-Plex-Token", friendToken, "Accept", "application/json")
	mc := decodeJSON(t, rec)
	items, _ := mc["Metadata"].([]any)
	if len(items) != 1 {
		t.Fatalf("onDeck items: %s", rec.Body.String())
	}
	tv, _ := f.srv.sectionFor(core.TypeEpisode)
	if got := items[0].(map[string]any)["librarySectionUUID"]; got != tv.UUID {
		t.Fatalf("episode librarySectionUUID = %v, want %s", got, tv.UUID)
	}
}

// A viewer limited to some sections must not open a collection of another.
func TestCollectionOfHiddenSectionIsNotFound(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Auth.(*fakeAuth).users["tvonly"] = core.User{ID: 31, Username: "tvonly", AllowedSections: []int{2}}
	key := core.CollectionKey(8091).String()
	for _, path := range []string{"/library/collections/" + key, "/library/collections/" + key + "/children", "/library/metadata/" + key, "/library/collections/" + key + "/thumb/1"} {
		if rec := f.get(t, path, "X-Plex-Token", "tvonly"); rec.Code != 404 {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := f.get(t, "/library/collections/"+key); rec.Code != 200 {
		t.Errorf("owner collection: %d", rec.Code)
	}
}

// Related, similar and post-play rows of a hidden title are not served: they
// are built from that title's credits and collection.
func TestRelatedRowsOfHiddenTitleAreNotFound(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Auth.(*fakeAuth).users["kid"] = core.User{ID: 30, Username: "kid", MaxContentRating: "PG"}
	key := fightClub.Key.String()
	for _, path := range []string{"/hubs/metadata/" + key, "/hubs/metadata/" + key + "/related", "/library/metadata/" + key + "/related", "/library/metadata/" + key + "/similar", "/hubs/metadata/" + key + "/postplay"} {
		if rec := f.get(t, path, "X-Plex-Token", "kid"); rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
		if rec := f.get(t, path); rec.Code != 200 {
			t.Errorf("owner %s: %d", path, rec.Code)
		}
	}
}

// allLeaves pages over the episodes the viewer may see.
func TestAllLeavesPagesVisibleEpisodes(t *testing.T) {
	f := newFixture(func(d *Deps) { d.AccessPolicy = keywordPolicy{blocked: "blocked"} })
	defer f.srv.Close()
	ep := bbE1
	ep.Keywords = []string{"blocked"}
	f.catalog.titles[ep.Key] = ep
	rec := f.get(t, "/library/metadata/"+breakingBad.Key.String()+"/allLeaves?X-Plex-Container-Start=0&X-Plex-Container-Size=1", "X-Plex-Token", friendToken, "Accept", "application/json")
	mc := decodeJSON(t, rec)
	items, _ := mc["Metadata"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["title"] != bbE2.Title || mc["totalSize"].(float64) != 1 {
		t.Fatalf("allLeaves page: %s", rec.Body.String())
	}
}
