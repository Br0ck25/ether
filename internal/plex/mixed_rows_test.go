package plex

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

func TestMixedHomeRowKeepsItemIdentityAndViewerRestrictions(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Recommender = &fakeRecommender{home: []core.Hub{{
		ID: "home.mixed", Title: "Friday Night Picks", Promoted: true,
		Items: []core.Title{fightClub, breakingBad},
	}}}
	f.srv.d.Auth.(*fakeAuth).users["movies-only"] = core.User{ID: 88, AllowedSections: []int{1}}
	for _, token := range []string{"", "movies-only"} {
		headers := map[string]string{"Accept": "application/json", "X-Plex-Product": "Plex for Apple TV"}
		if token != "" {
			headers["X-Plex-Token"] = token
		}
		var body struct{ MediaContainer model.MediaContainer }
		rec := f.request(t, http.MethodGet, "/hubs?excludeContinueWatching=1", headers)
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var row *model.Hub
		for _, h := range body.MediaContainer.Hub {
			if h.HubIdentifier == "home.mixed" {
				row = &h
				break
			}
		}
		if row == nil || row.Type != "mixed" {
			t.Fatalf("missing mixed shelf: %s", rec.Body.String())
		}
		want := 2
		if token != "" {
			want = 1
		}
		if len(row.Metadata) != want || row.Metadata[0].Type != "movie" || row.Metadata[0].RatingKey != fightClub.Key.String() || row.Metadata[0].LibrarySectionID != 1 {
			t.Fatalf("movie identity or restrictions lost: %+v", row.Metadata)
		}
		if want == 2 && (row.Metadata[1].Type != "show" || row.Metadata[1].RatingKey != breakingBad.Key.String() || row.Metadata[1].LibrarySectionID != 2) {
			t.Fatalf("show identity lost: %+v", row.Metadata[1])
		}
		fetched := f.request(t, http.MethodGet, row.Key, headers)
		if err := json.Unmarshal(fetched.Body.Bytes(), &body); err != nil || len(body.MediaContainer.Metadata) != want {
			t.Fatalf("shelf expansion: %s", fetched.Body.String())
		}
	}
	// Selecting a show in the same row still expands its episodes; a movie
	// remains a movie rather than inheriting the row's other item's type.
	for _, tc := range []struct {
		key, first core.RatingKey
		typ        string
	}{{fightClub.Key, fightClub.Key, "movie"}, {breakingBad.Key, bbE1.Key, "episode"}} {
		uri := "server://m/com.plexapp.plugins.library/library/metadata/" + tc.key.String()
		rec := f.request(t, http.MethodPost, "/playQueues?type=video&uri="+url.QueryEscape(uri), map[string]string{"Accept": "application/json"})
		body := decodeJSON(t, rec)
		items, _ := body["Metadata"].([]any)
		if rec.Code != http.StatusOK || len(items) == 0 {
			t.Fatalf("selected %s: HTTP %d %s", tc.typ, rec.Code, rec.Body.String())
		}
		first := items[0].(map[string]any)
		if first["ratingKey"] != tc.first.String() || first["type"] != tc.typ {
			t.Fatalf("selected %s: %+v", tc.typ, first)
		}
	}
}

type fakeForYouRec struct {
	fakeRecommender
	feed []core.Title
}

func (r *fakeForYouRec) SectionHubs(_ interface{}, _ *core.User, t core.MediaType, _ int) ([]core.Hub, error) {
	if t == "" {
		return r.home, nil
	}
	return r.section[t], nil
}

func (r *fakeForYouRec) ForYouFeed(_ interface{}, _ *core.User, _ core.HubContext, _ int) ([]core.Title, error) {
	return r.feed, nil
}

func TestForYouPseudoLibrarySectionMixesMoviesAndShows(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.srv.d.Sections = []Section{
		{ID: 3, Type: core.TypeMovie, Title: "For You", Mixed: true},
		{ID: 1, Type: core.TypeMovie, Title: "Movies"},
		{ID: 2, Type: core.TypeShow, Title: "TV Shows"},
	}
	f.srv.sections = f.srv.buildSections()
	f.srv.d.Recommender = &fakeRecommender{
		home: []core.Hub{{
			ID: "home.top10", Title: "Top 10 for You", Promoted: true,
			Items: []core.Title{fightClub, breakingBad},
		}},
		section: map[core.MediaType][]core.Hub{
			"": {{
				ID: "home.top10", Title: "Top 10 for You", Promoted: true,
				Items: []core.Title{fightClub, breakingBad},
			}},
		},
	}

	headers := map[string]string{"Accept": "application/json", "X-Plex-Product": "Plex for Apple TV"}
	rec := f.request(t, http.MethodGet, "/hubs/sections/3", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("section 3 hubs status=%d: %s", rec.Code, rec.Body.String())
	}
	var body struct{ MediaContainer model.MediaContainer }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var top *model.Hub
	for _, h := range body.MediaContainer.Hub {
		if h.HubIdentifier == "home.top10" {
			top = &h
			break
		}
	}
	if top == nil || top.Type != "mixed" || len(top.Metadata) != 2 {
		t.Fatalf("expected mixed Top 10 for You row in section 3, got %+v", body.MediaContainer.Hub)
	}
	if top.Metadata[0].Type != "movie" || top.Metadata[0].LibrarySectionID != 1 ||
		top.Metadata[1].Type != "show" || top.Metadata[1].LibrarySectionID != 2 {
		t.Fatalf("expected canonical section IDs on mixed items, got %+v", top.Metadata)
	}
}
