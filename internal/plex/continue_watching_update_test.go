package plex

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestMarkOneWatchedRetainsOtherContinueWatchingItems(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	var keys []core.RatingKey
	for n := range 4 {
		title := fightClub
		title.Key = core.MovieKey(9000 + int64(n))
		title.Title = fmt.Sprintf("Resume test %d", n)
		keys = append(keys, title.Key)
		f.catalog.titles[title.Key] = title
		f.state.set(ownerUser.ID, ws(title.Key, 600000+int64(n)*60000, 0))
	}
	count := func(path string, want int) {
		t.Helper()
		mc := decodeJSON(t, f.get(t, path, jsonAccept...))
		if hubs, ok := mc["Hub"].([]any); ok {
			var found bool
			for _, h := range hubs {
				hub := h.(map[string]any)
				if hub["title"] == "Continue Watching" {
					mc = hub
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s omitted Continue Watching", path)
			}
		}
		items, _ := mc["Metadata"].([]any)
		if len(items) != want {
			t.Fatalf("%s has %d resume items, want %d", path, len(items), want)
		}
	}
	paths := []string{"/hubs/continueWatching", "/hubs/home/continueWatching", "/hubs/sections/1/continueWatching/items", "/hubs/sections/1", "/hubs?identifier=home.continue,home.ondeck"}
	for _, p := range paths {
		count(p, 4)
	}
	if rec := f.get(t, "/:/scrobble?key="+keys[0].String()+"&identifier=com.plexapp.plugins.library"); rec.Code != http.StatusOK {
		t.Fatalf("scrobble: HTTP %d", rec.Code)
	}
	for _, p := range paths {
		count(p, 3)
	}
	for n, k := range keys[1:] {
		st, err := f.state.Get(context.Background(), ownerUser.ID, k)
		if err != nil || st.ViewOffset.Milliseconds() != 600000+int64(n+1)*60000 {
			t.Fatalf("unrelated resume state changed: %+v %v", st, err)
		}
	}
}
