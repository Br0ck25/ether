package personalize

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestTitleSimilarity(t *testing.T) {
	a := &core.Title{Genres: []string{"Science Fiction", "Drama"}, Keywords: []string{"space", "robot"},
		Year: 2014, CollectionID: 9, Directors: []core.Person{{TMDB: 1}}, Cast: []core.Person{{TMDB: 2}}}
	same := &core.Title{Genres: []string{"Science Fiction", "Drama"}, Keywords: []string{"space", "robot"},
		Year: 2016, CollectionID: 9, Directors: []core.Person{{TMDB: 1}}, Cast: []core.Person{{TMDB: 2}}}
	far := &core.Title{Genres: []string{"Comedy"}, Keywords: []string{"wedding"}, Year: 1985,
		Directors: []core.Person{{TMDB: 8}}, Cast: []core.Person{{TMDB: 9}}}
	if got := titleSimilarity(a, same); got < 0.9 {
		t.Errorf("similarity(near-identical) = %v, want >0.9", got)
	}
	if got := titleSimilarity(a, far); got != 0 {
		t.Errorf("similarity(disjoint) = %v, want 0", got)
	}
	// Franchise alone is a real but bounded signal.
	fr := &core.Title{Genres: []string{"Comedy"}, Year: 1985, CollectionID: 9}
	if got := titleSimilarity(a, fr); got != 0.15 {
		t.Errorf("similarity(franchise only) = %v, want 0.15", got)
	}
}

func TestRuntimeFitByHour(t *testing.T) {
	long := &core.Title{Type: core.TypeMovie, Runtime: 180 * time.Minute}
	short := &core.Title{Type: core.TypeShow, Runtime: 25 * time.Minute}
	evening := hubContext(time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC), core.DeviceTV)
	late := hubContext(time.Date(2026, 10, 14, 23, 40, 0, 0, time.UTC), core.DeviceTV)
	ts := buildTaste(tasteInput{now: testNow, loc: time.UTC})
	bEve := ts.stopBudgetMinutes(evening, evening.Now)
	bLate := ts.stopBudgetMinutes(late, late.Now)
	if bLate >= bEve {
		t.Errorf("budgets: evening %v late %v, want late smaller", bEve, bLate)
	}
	if runtimeFit(long, bLate, late) >= runtimeFit(long, bEve, evening) {
		t.Error("a 3h film should fit worse late at night")
	}
	if runtimeFit(short, bLate, late) <= runtimeFit(long, bLate, late) {
		t.Error("late at night a short episode must beat a long film")
	}
	if runtimeFit(short, bEve, evening) != 1 {
		t.Error("short title inside the evening budget should fit perfectly")
	}
	if runtimeFit(&core.Title{}, bEve, evening) != 0.7 {
		t.Error("unknown runtime should be neutral")
	}
}

// The hook queue: ranked by taste × similarity × runtime, watched titles
// excluded, franchise-next first with its reason, cached per session.
func TestPostPlayRanking(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 41, Username: "scifi"}
	h.users.users = []core.User{u}
	// Strong sci-fi/Vera Villeneuve taste, including saga part 1 (0,4).
	for _, j := range []int{14, 15, 16, 17} {
		h.finish(u.ID, movieKey(0, j), testNow.AddDate(0, 0, -j))
	}
	h.finish(u.ID, movieKey(0, 4), testNow.Add(-2*time.Hour))
	h.rebuild(u)
	ctx := context.Background()
	hc := hubContext(testNow, core.DeviceTV)

	picks, err := h.e.PostPlay(ctx, &u, movieKey(0, 4), hc, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(picks) == 0 {
		t.Fatal("no picks")
	}
	for i := 1; i < len(picks); i++ {
		if picks[i].Score > picks[i-1].Score {
			t.Fatalf("picks not sorted: %v before %v", picks[i-1].Score, picks[i].Score)
		}
	}
	// Franchise-next leads, explained, with a forced-high similarity.
	if picks[0].Title.Key != movieKey(0, 5) {
		t.Fatalf("#1 = %s, want saga part 2 %d", picks[0].Title.Title, movieKey(0, 5))
	}
	if picks[0].Reason != "Next in Star Saga Collection" || picks[0].Similarity < 0.95 {
		t.Errorf("#1 reason/sim = %q/%v", picks[0].Reason, picks[0].Similarity)
	}
	// Nothing already watched, nothing but sci-fi at the head.
	for _, p := range picks {
		for _, j := range []int{4, 14, 15, 16, 17} {
			if p.Title.Key == movieKey(0, j) {
				t.Errorf("watched title %d in picks", j)
			}
		}
	}
	// Deterministic and session-cached: a second call serves the same
	// ranking without another Similar round-trip.
	simCalls := h.cat.callCount("similar")
	again, err := h.e.PostPlay(ctx, &u, movieKey(0, 4), hc, 12)
	if err != nil || len(again) != len(picks) || again[0].Title.Key != picks[0].Title.Key {
		t.Fatalf("cached ranking differs: %v", err)
	}
	if got := h.cat.callCount("similar"); got != simCalls {
		t.Errorf("similar calls %d -> %d; ranking was not cached", simCalls, got)
	}
}

// Content restrictions hold in the queue.
func TestPostPlayRestrictions(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 42, Username: "kid", MaxContentRating: "PG"}
	h.users.users = []core.User{u}
	h.finish(u.ID, movieKey(5, 20), testNow.Add(-time.Hour)) // a Family film
	h.rebuild(u)
	picks, err := h.e.PostPlay(context.Background(), &u, movieKey(5, 20), hubContext(testNow, core.DeviceTV), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range picks {
		if p.Title.ContentRating == "R" || p.Title.ContentRating == "TV-MA" {
			t.Errorf("restricted title %s (%s) in picks", p.Title.Title, p.Title.ContentRating)
		}
	}
}

// A viewer with no taste model still gets a sane, quality-led queue.
func TestPostPlayColdViewer(t *testing.T) {
	h := newHarness(t, nil)
	u := core.User{ID: 43, Username: "new"}
	h.users.users = []core.User{u}
	h.rebuild(u)
	picks, err := h.e.PostPlay(context.Background(), &u, movieKey(1, 30), hubContext(testNow, core.DeviceTV), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(picks) == 0 {
		t.Fatal("cold viewer got no picks")
	}
	for _, p := range picks {
		if p.Score <= 0 || p.RuntimeFit <= 0 {
			t.Errorf("degenerate pick %+v", p)
		}
	}
}
