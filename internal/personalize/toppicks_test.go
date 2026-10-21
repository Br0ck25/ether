package personalize

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func row(id, kind string, n int) core.HomeRow {
	r := core.HomeRow{HubID: id, Kind: kind}
	for i := 0; i < n; i++ {
		r.Items = append(r.Items, core.Title{Key: core.RatingKey(1000 + i), Type: core.TypeMovie})
	}
	return r
}

func order(rows []core.HomeRow) []string {
	var out []string
	for _, r := range rows {
		if !r.Hidden {
			out = append(out, r.HubID)
		}
	}
	return out
}

func TestTopPicksOpenEveryPage(t *testing.T) {
	hc := hubContext(testNow, core.DeviceTV)
	// The order a section page is assembled in: the front end's rows first.
	rows := []core.HomeRow{
		row("movie.inprogress.1", KindContinue, 3),
		row("movie.recentlyadded.1", KindRecentlyAdded, 12),
		row("movie.newreleases", KindNewReleases, 10),
		row("movie.cluster.a", KindTasteCluster, 10),
		row("movie.cluster.b", KindTasteCluster, 10),
		row("movie.top10", KindTop10, 10),
		row("movie.era.1990", KindEra, 10),
		row("movie.acclaimed", KindAcclaimed, 10),
		row("movie.world.ko", KindWorld, 10),
		row("movie.toprated", KindTopRated, 10),
	}
	got := order(arrangeLayout(rows, core.HomeLayout{}, 1, hc, "movies", 16))
	if got[0] != "movie.top10" || got[1] != "movie.inprogress.1" {
		t.Fatalf("page head = %v", got)
	}
	for i, id := range got {
		switch id {
		case "movie.recentlyadded.1":
			if i+1 < recentlyAddedMinSlot {
				t.Fatalf("recently added at row %d: %v", i+1, got)
			}
		case "movie.newreleases":
			if i+1 < newReleasesMinSlot {
				t.Fatalf("new releases at row %d: %v", i+1, got)
			}
		}
	}

	// A viewer the taste model doesn't know yet gets the server's Top 10.
	cold := append([]core.HomeRow{}, rows[:5]...)
	cold = append(cold, row("movie.top10.today", KindTop10Today, 10))
	if got := order(arrangeLayout(cold, core.HomeLayout{}, 1, hc, "movies", 16)); got[0] != "movie.top10.today" {
		t.Fatalf("cold page head = %v", got)
	}
	// An empty Top 10 can't lead.
	empty := append([]core.HomeRow{}, rows...)
	empty[5] = row("movie.top10", KindTop10, 0)
	if got := order(arrangeLayout(empty, core.HomeLayout{}, 1, hc, "movies", 16)); got[0] == "movie.top10" {
		t.Fatalf("empty top picks led: %v", got)
	}

	// The owner's layout wins: an anchor at slot 1, or a policy on the row.
	anchored := core.HomeLayout{Global: []core.LayoutPage{{Section: "movies", Rows: []core.RowPolicy{{Row: KindEra, Status: "always", Slot: 1}}}}}
	if got := order(arrangeLayout(rows, anchored, 1, hc, "movies", 16)); got[0] != "movie.era.1990" {
		t.Fatalf("owner anchor lost slot 1: %v", got)
	}
	ra := core.HomeLayout{Global: []core.LayoutPage{{Section: "movies", Rows: []core.RowPolicy{{Row: KindRecentlyAdded, Status: "always", Slot: 2}}}}}
	if got := order(arrangeLayout(rows, ra, 1, hc, "movies", 16)); got[0] != "movie.top10" || got[1] != "movie.recentlyadded.1" {
		t.Fatalf("owner policy on Recently Added ignored: %v", got)
	}
	// A short page still keeps the feed at the bottom rather than dropping the picks.
	if got := order(arrangeLayout(rows, core.HomeLayout{}, 1, hc, "movies", 4)); got[0] != "movie.top10" || len(got) != 4 {
		t.Fatalf("short page = %v", got)
	}
}

func TestHeadlineAndFeedCuration(t *testing.T) {
	ts := &Taste{Language: map[string]float64{"en": 1, "fr": 0.4, "de": 0.05}}
	movie := func(lang string, votes int) core.Title {
		return core.Title{Type: core.TypeMovie, Language: lang, VoteCount: votes, Rating: 7.5}
	}
	for _, tc := range []struct {
		t    core.Title
		want bool
	}{
		{movie("en", 5000), true},
		{movie("fr", 5000), true},
		{movie("de", 5000), false}, // barely watched language
		{movie("ja", 5000), false}, // never watched
		{movie("en", 40), false},   // too few ratings to trust
		{movie("", 5000), true},
	} {
		if got := ts.headlineOK(&tc.t); got != tc.want {
			t.Errorf("headlineOK(%s, %d votes) = %v", tc.t.Language, tc.t.VoteCount, got)
		}
	}
	if !(&Taste{}).headlineOK(&core.Title{Type: core.TypeMovie, Language: "ja", VoteCount: 5000}) {
		t.Error("a viewer with no language history should accept every language")
	}

	feed := core.HomeRow{HubID: "movie.recentlyadded.1", Kind: KindRecentlyAdded}
	feed.Items = []core.Title{movie("en", 3000), movie("ko", 3000), movie("en", 3), movie("fr", 800), movie("en", 200)}
	got := curateFeed(feed, ts)
	if got.Hidden || len(got.Items) != 3 || got.Items[0].VoteCount != 3000 || got.Items[1].Language != "fr" {
		t.Fatalf("curated feed = %+v", got.Items)
	}
	feed.Items = []core.Title{movie("ko", 3000), movie("en", 1)}
	if got := curateFeed(feed, ts); !got.Hidden {
		t.Fatal("a feed with nothing for the viewer should be hidden")
	}
}

func TestRowsNeverShowAHandfulOfTitles(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MinRowItems = 0 }) // the production default (12)
	u := core.User{ID: 1}
	sciFiFan(h, u.ID)
	h.rebuild(u)
	for _, section := range []string{"home", "movies", "shows"} {
		hc := hubContext(testNow, core.DeviceTV)
		if section == "shows" {
			hc.SectionID = 2
		}
		p, err := h.e.PreviewHome(context.Background(), &u, hc, section, 12)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range p.Rows {
			if r.Hidden || r.Source == core.RowSourcePlex || r.Kind == KindUpNext {
				continue
			}
			floor := 12
			if r.Kind == KindTop10 || r.Kind == KindTop10Today {
				floor = 10
			}
			if len(r.Items) < floor {
				t.Errorf("%s: %s shows only %d titles", section, r.HubID, len(r.Items))
			}
			if r.Kind == KindFranchise {
				t.Errorf("%s: franchise row shown by default", section)
			}
		}
	}
}
