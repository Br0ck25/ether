package recommend

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type fakeScorer struct{ scores map[core.RatingKey]float64 }

func (f fakeScorer) ScoreTitles(_ context.Context, _ *core.User, titles []core.Title) (map[core.RatingKey]float64, error) {
	out := map[core.RatingKey]float64{}
	for _, t := range titles {
		out[t.Key] = f.scores[t.Key]
	}
	return out, nil
}

func TestRelatedPersonalOrder(t *testing.T) {
	ctx := context.Background()
	r, _, user, cat := fixtureWith(t, nil)
	cat.similar[sever.Key] = []core.RatingKey{dune2.Key, comedy.Key, sicario.Key}
	order := func() []core.RatingKey {
		rel, err := r.Related(ctx, user, sever.Key, 10)
		if err != nil {
			t.Fatal(err)
		}
		h := findHub(rel, "tv.similar")
		if h == nil {
			t.Fatalf("no similar row: %v", hubIDs(rel))
		}
		return keysOf(h.Items)
	}
	plain := order()
	if len(plain) != 3 || plain[0] != dune2.Key {
		t.Fatalf("TMDB order = %v", plain)
	}
	// A viewer who loves thrillers sees Sicario lifted above Comedy; TMDB's
	// top match keeps its place.
	r.SetScorer(fakeScorer{map[core.RatingKey]float64{dune2.Key: 0.1, comedy.Key: 0.0, sicario.Key: 0.9}})
	got := order()
	if got[0] != dune2.Key || got[1] != sicario.Key || got[2] != comedy.Key {
		t.Fatalf("personal order = %v", got)
	}
	r.SetScorer(nil)
	if got := order(); got[0] != plain[0] {
		t.Fatal("clearing the scorer should restore TMDB order")
	}
}
