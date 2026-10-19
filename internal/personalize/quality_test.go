package personalize

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// obscure adds titles that look great on raw TMDB numbers but are not:
// few votes, inflated early ratings, an unknown release.
func addObscure(cat *fakeCatalog, now time.Time) (inflated, unknown, panned core.Title) {
	inflated = core.Title{Key: core.MovieKey(77001), Type: core.TypeMovie, Title: "Fan Favourite 2026", Genres: []string{"Science Fiction"},
		Rating: 9.4, VoteCount: 470, Popularity: 60, Runtime: 80 * time.Minute, Released: now.AddDate(0, -2, 0), ContentRating: "PG-13", Language: "en"}
	unknown = core.Title{Key: core.MovieKey(77002), Type: core.TypeMovie, Title: "Nobody Saw This", Genres: []string{"Science Fiction"},
		Rating: 9.0, VoteCount: 3, Popularity: 300, Runtime: 85 * time.Minute, Released: now.AddDate(0, 0, -5), ContentRating: "PG-13", Language: "en"}
	panned = core.Title{Key: core.MovieKey(77003), Type: core.TypeMovie, Title: "Panned Sequel", Genres: []string{"Science Fiction"},
		Rating: 4.1, VoteCount: 900, Popularity: 400, Runtime: 90 * time.Minute, Released: now.AddDate(0, 0, -10), ContentRating: "PG-13", Language: "en"}
	for _, t := range []core.Title{inflated, unknown, panned} {
		cat.add(t)
		cat.prependList("trending_week:movie", t.Key)
		cat.prependList("top_rated:movie", t.Key)
	}
	return
}

func TestQualityGuards(t *testing.T) {
	h := newHarness(t, nil)
	inflated, unknown, panned := addObscure(h.cat, testNow)
	m, err := h.e.buildModel(context.Background(), core.User{ID: genericID}, true)
	if err != nil {
		t.Fatal(err)
	}
	bad := map[core.RatingKey]string{inflated.Key: "inflated", unknown.Key: "unknown", panned.Key: "panned"}
	for _, r := range m.rows {
		for _, it := range r.items {
			name, isBad := bad[it.t.Key]
			if !isBad {
				continue
			}
			// The inflated title is a real, recent, popular film: it may
			// trend (Top 10 today), be new or be short, but never be
			// "acclaimed".
			if name == "inflated" && r.kind != KindTopRated && r.kind != KindShowcase {
				continue
			}
			t.Errorf("%s title %q in %s", name, it.t.Title, r.kind)
		}
	}
	top := rowsOfKind(m, KindTopRated)
	if len(top) != 1 {
		t.Fatal("no Top Rated row")
	}
	for _, it := range top[0].items {
		if float64(it.t.VoteCount) < 1.5*1000/10 {
			t.Fatalf("Top Rated has %s with %d votes", it.t.Title, it.t.VoteCount)
		}
	}
	// The weighted rating prefers the well-attested classic.
	classic := core.Title{Type: core.TypeMovie, Rating: 8.5, VoteCount: 30000}
	if weightedRating(&inflated) >= weightedRating(&classic) {
		t.Fatalf("weighted rating: inflated %v ≥ classic %v", weightedRating(&inflated), weightedRating(&classic))
	}
}

func TestVoteFloorScalesWithCatalogue(t *testing.T) {
	h := newHarness(t, nil)
	b := &builder{e: h.e, ctx: context.Background(), now: testNow, memo: map[string][]core.Title{}}
	if f := b.voteFloor(core.TypeMovie); f != baseFloorMovie {
		t.Fatalf("movie floor on a catalogue of well-voted films = %v", f)
	}
	// A small catalogue of thinly voted shows scales the floor down, but
	// never below a tenth of the base.
	if f := b.voteFloor(core.TypeShow); f >= baseFloorShow || f < baseFloorShow/10 {
		t.Fatalf("show floor = %v", f)
	}
}

func TestColdStartHome(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	u := core.User{ID: 9, Username: "new"}
	h.rebuild(u)
	p, err := h.e.PreviewHome(ctx, &u, hubContext(testNow, core.DeviceTV), "", 12)
	if err != nil {
		t.Fatal(err)
	}
	kinds := kindsOf(p.Rows)
	t.Logf("cold start: %v\n%s", kinds, describe(p.Rows))
	first := ""
	for _, r := range p.Rows {
		if r.Source != core.RowSourcePlex {
			first = r.Kind
			break
		}
	}
	if first != KindTop10Today {
		t.Fatalf("cold-start Home should lead with Top 10 today: %v", kinds)
	}
	top := rowByKind(p.Rows, KindTop10Today)
	if !strings.Contains(top.Title, "Test Server") || len(top.Items) != 10 {
		t.Fatalf("top 10 today = %q, %d items", top.Title, len(top.Items))
	}
	groups := map[string]bool{}
	showcases := 0
	for _, r := range p.Rows {
		if r.Kind != KindShowcase {
			continue
		}
		showcases++
		g := strings.TrimPrefix(r.HubID, "home.showcase.")
		for ci, cluster := range showcaseClusters {
			for _, name := range cluster {
				if genreSlug(name) == g {
					if groups[string(rune('0'+ci))] {
						t.Fatalf("two showcases from one genre family: %v", kinds)
					}
					groups[string(rune('0'+ci))] = true
				}
			}
		}
	}
	if showcases < 2 || showcases > 3 {
		t.Fatalf("%d showcase rows, want 2-3: %v", showcases, kinds)
	}
	if rowByKind(p.Rows, KindNewReleases) == nil || rowByKind(p.Rows, KindTop10) != nil {
		t.Fatalf("cold start wants New Releases and no Top 10 for You: %v", kinds)
	}
	for _, k := range []string{KindAiringToday, KindPopular} {
		if rowByKind(p.Rows, k) != nil {
			t.Fatalf("%s on a Home: %v", k, kinds)
		}
	}
	// Once the viewer has a real taste, the cold-start rows give way.
	sciFiFan(h, u.ID)
	h.rebuild(u)
	p, _ = h.e.PreviewHome(ctx, &u, hubContext(testNow, core.DeviceTV), "", 12)
	if rowByKind(p.Rows, KindShowcase) != nil || rowByKind(p.Rows, KindTop10) == nil {
		t.Fatalf("personal Home still cold: %v", kindsOf(p.Rows))
	}
}

func TestHiddenGemsExcludeWidelyRatedClassics(t *testing.T) {
	h := newHarness(t, nil)
	sciFiFan(h, 1)
	famous := h.cat.titles[movieKey(0, 11)]
	famous.Title = "Widely known classic"
	famous.Rating = 9
	famous.VoteCount = 500000
	famous.Popularity = 0.01
	h.cat.add(famous)
	m := h.rebuild(core.User{ID: 1})
	for _, r := range rowsOfKind(m, KindHiddenGems) {
		for _, it := range r.items {
			if it.t.Key == famous.Key || float64(it.t.VoteCount) > 5*priorVotes(it.t.Type) {
				t.Fatalf("famous title in hidden gems: %s (%d votes)", it.t.Title, it.t.VoteCount)
			}
		}
	}
}
