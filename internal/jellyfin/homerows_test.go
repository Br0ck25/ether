package jellyfin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type fakeRecommender struct{ hubs []core.Hub }

func (r fakeRecommender) HomeHubs(context.Context, *core.User, int) ([]core.Hub, error) {
	return r.hubs, nil
}
func (r fakeRecommender) SectionHubs(context.Context, *core.User, core.MediaType, int) ([]core.Hub, error) {
	return nil, nil
}
func (r fakeRecommender) Related(context.Context, *core.User, core.RatingKey, int) ([]core.Hub, error) {
	return nil, nil
}

func TestHomeRows(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)

	// No recommender: an empty list, not an error.
	w := f.call("GET", "/Ether/HomeRows", tok, nil)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("no recommender: %d %q", w.Code, w.Body)
	}
	if f.call("GET", "/Ether/HomeRows", "", nil).Code != http.StatusUnauthorized {
		t.Fatal("home rows served without signing in")
	}

	f.srv.d.Recommender = fakeRecommender{hubs: []core.Hub{
		{ID: "home.continue", Title: "Continue Watching", Items: []core.Title{alien}},
		{ID: "home.trending", Title: "Trending", Items: []core.Title{fightClub, alien}},
		{ID: "home.empty", Title: "Nothing here"},
	}}
	rows := decode[[]homeRow](t, f.call("GET", "/Ether/HomeRows", tok, nil))
	if len(rows) != 1 || rows[0].Name != "Trending" || len(rows[0].Items) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	// The web client gets the script, and its index page loads it.
	if w := f.call("GET", "/Ether/home.js", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "HomeRows") {
		t.Fatalf("script: %d", w.Code)
	}
}
