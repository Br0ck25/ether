package plex

import (
	"encoding/json"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// A show and its seasons count the episodes the viewer can list, not a
// stale or announced total, so watching every aired episode marks them
// watched: leafCount and viewedLeafCount come from the same list on the
// detail page, the season list and the All episodes entry.
func TestLeafCountsMatchListedEpisodes(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	show := breakingBad
	show.ChildCount, show.LeafCount = 3, 24 // e.g. TMDB's announced totals
	season := bbS1
	season.ChildCount = 12
	f.catalog.titles[show.Key] = show
	f.catalog.titles[season.Key] = season
	for _, k := range []core.RatingKey{bbE1.Key, bbE2.Key} {
		f.state.set(ownerUser.ID, core.WatchStatus{Key: k, ViewCount: 1, LastViewedAt: fixedNow, UpdatedAt: fixedNow})
	}

	type item struct {
		RatingKey       string `json:"ratingKey"`
		Title           string `json:"title"`
		LeafCount       *int   `json:"leafCount"`
		ViewedLeafCount *int   `json:"viewedLeafCount"`
		ViewCount       int    `json:"viewCount"`
	}
	var out struct {
		MediaContainer struct {
			Metadata  []item `json:"Metadata"`
			Directory []item `json:"Directory"`
		} `json:"MediaContainer"`
	}
	check := func(name string, it item) {
		t.Helper()
		if it.LeafCount == nil || it.ViewedLeafCount == nil {
			t.Fatalf("%s: leafCount or viewedLeafCount missing: %+v", name, it)
		}
		if *it.LeafCount != 2 || *it.ViewedLeafCount != 2 {
			t.Fatalf("%s: leafCount=%d viewedLeafCount=%d, want 2/2", name, *it.LeafCount, *it.ViewedLeafCount)
		}
	}
	get := func(path string) {
		t.Helper()
		rec := f.get(t, path, "Accept", "application/json")
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		out.MediaContainer.Metadata, out.MediaContainer.Directory = nil, nil
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v %s", path, err, rec.Body.String())
		}
	}

	get("/library/metadata/" + show.Key.String())
	if len(out.MediaContainer.Metadata) != 1 {
		t.Fatalf("show detail: %+v", out.MediaContainer)
	}
	check("show detail", out.MediaContainer.Metadata[0])
	if out.MediaContainer.Metadata[0].ViewCount == 0 {
		t.Fatal("fully watched show not marked watched")
	}

	get("/library/metadata/" + show.Key.String() + "/children")
	if len(out.MediaContainer.Metadata) != 1 || len(out.MediaContainer.Directory) != 1 {
		t.Fatalf("show children: %+v", out.MediaContainer)
	}
	check("season row", out.MediaContainer.Metadata[0])
	check("All episodes", out.MediaContainer.Directory[0])

	get("/library/metadata/" + season.Key.String())
	if len(out.MediaContainer.Metadata) != 1 {
		t.Fatalf("season detail: %+v", out.MediaContainer)
	}
	check("season detail", out.MediaContainer.Metadata[0])
}
