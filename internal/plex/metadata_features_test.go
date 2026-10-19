package plex

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/episodeorder"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
	"net/http"
	"strings"
	"testing"
)

type orderedSource struct{}

func (orderedSource) EpisodeGroups(context.Context, int64) ([]tmdb.EpisodeGroupSummary, error) {
	return []tmdb.EpisodeGroupSummary{{ID: "dvd", Type: 3}}, nil
}
func (orderedSource) EpisodeGroup(context.Context, string) (*tmdb.EpisodeGroup, error) {
	return &tmdb.EpisodeGroup{Groups: []tmdb.EpisodeSubgroup{{Name: "Disc 1", Order: 0, Episodes: []tmdb.OrderedEpisode{{Episode: tmdb.Episode{SeasonNumber: 1, EpisodeNumber: 2}, Order: 0}, {Episode: tmdb.Episode{SeasonNumber: 1, EpisodeNumber: 1}, Order: 1}}}}}, nil
}
func TestAlternateOrderPlexTreeAndCanonicalPlayback(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	db, e := store.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := episodeorder.New(db, orderedSource{}, f.catalog)
	f.srv.d.EpisodeOrder = s
	if e = s.Save(context.Background(), 0, breakingBad.Key, episodeorder.Preference{GroupID: "dvd", Specials: "last"}); e != nil {
		t.Fatal(e)
	}
	virtual := core.SeasonKey(breakingBad.Key.TMDB(), 9000)
	rec := f.get(t, "/library/metadata/"+breakingBad.Key.String()+"/children?excludeAllLeaves=1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), virtual.String()) || strings.Contains(rec.Body.String(), `ratingKey="`+bbS1.Key.String()+`"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = f.get(t, "/library/metadata/"+virtual.String()+"/children")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `parentRatingKey="`+virtual.String()+`"`) {
		t.Fatal(rec.Code, body)
	}
	if strings.Index(body, `ratingKey="`+bbE2.Key.String()+`"`) > strings.Index(body, `ratingKey="`+bbE1.Key.String()+`"`) {
		t.Fatal("order not applied", body)
	}
	// The chosen episode keeps canonical s01e02 identity and part path.
	rec = f.get(t, "/library/metadata/"+bbE2.Key.String())
	if !strings.Contains(rec.Body.String(), `index="1"`) || !strings.Contains(rec.Body.String(), `ratingKey="`+bbE2.Key.String()+`"`) {
		t.Fatal(rec.Body.String())
	}
	pr := &Request{User: &ownerUser}
	keys, err := f.srv.continuousKeys(context.Background(), pr, "/library/metadata/"+bbE2.Key.String(), true)
	if err != nil || len(keys) != 2 || keys[0] != bbE2.Key || keys[1] != bbE1.Key {
		t.Fatal("continuous order", keys, err)
	}
	keys, err = f.srv.expandKey(context.Background(), pr, virtual)
	if err != nil || len(keys) != 2 || keys[0] != bbE2.Key {
		t.Fatal("virtual container expansion", keys, err)
	}
	rec = f.get(t, "/library/metadata/"+bbE2.Key.String()+"/parent")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `ratingKey="`+virtual.String()+`"`) {
		t.Fatal("parent", rec.Code, rec.Body.String())
	}
	canonical, _ := f.catalog.Get(context.Background(), bbE2.Key)
	if canonical.Season != 1 || canonical.Episode != 2 {
		t.Fatal("canonical changed", canonical)
	}
	f.srv.d.Auth.(*fakeAuth).users["movie-only"] = core.User{ID: 88, AllowedSections: []int{1}}
	for _, path := range []string{"/library/metadata/" + virtual.String(), "/library/metadata/" + virtual.String() + "/children", "/library/metadata/" + breakingBad.Key.String() + "/allLeaves", "/library/metadata/" + bbE2.Key.String() + "/parent"} {
		rec = f.request(t, http.MethodGet, path, map[string]string{"X-Plex-Token": "movie-only"})
		if rec.Code != 404 {
			t.Fatalf("section leakage %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
