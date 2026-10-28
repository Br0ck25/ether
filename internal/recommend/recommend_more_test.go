package recommend

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"sync"
	"testing"
)

type fakeLLM struct {
	rows  []core.Hub
	more  []core.Title
	seeds []core.Title
	mu    sync.Mutex
}

func (f *fakeLLM) ThemedRows(_ context.Context, _ *core.User, seeds []core.Title, _ int) ([]core.Hub, error) {
	f.mu.Lock()
	f.seeds = seeds
	f.mu.Unlock()
	return f.rows, nil
}
func (f *fakeLLM) MoreLikeThis(context.Context, *core.Title, int) ([]core.Title, error) {
	return f.more, nil
}

func TestRelatedEpisodeAndLLMInterleave(t *testing.T) {
	ctx := context.Background()
	ll := &fakeLLM{more: []core.Title{sicario}}
	r, _, user, cat := fixtureWith(t, func(o *Options) { o.LLM = ll })
	cat.similar[sever.Key] = []core.RatingKey{arrival.Key, dune2.Key}
	ep := ep3
	ep.ShowKey = sever.Key
	cat.add(ep)
	rel, err := r.Related(ctx, user, ep.Key, 10)
	if err != nil {
		t.Fatal(err)
	}
	h := findHub(rel, "tv.similar")
	if h == nil || h.Context != "hub.tv.similar" || h.Type != core.TypeShow {
		t.Fatalf("episode related: %+v", rel)
	}
	keys := map[core.RatingKey]bool{}
	for _, it := range h.Items {
		keys[it.Key] = true
	}
	if !keys[dune2.Key] || !keys[sicario.Key] || keys[arrival.Key] {
		t.Fatalf("similar via show + llm, minus watched: %+v", h.Items)
	}
	if _, err := r.Related(ctx, user, core.MovieKey(424242), 10); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown title: %v", err)
	}
}

func TestContentRatingPushedToBrowse(t *testing.T) {
	ctx := context.Background()
	r, _, user, cat := fixtureWith(t, nil)
	if _, err := r.Related(ctx, user, dune.Key, 5); err != nil {
		t.Fatal(err)
	}
	cat.mu.Lock()
	defer cat.mu.Unlock()
	pushed := false
	for _, f := range cat.filters {
		if f.MaxContentRating == "PG-13" {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("MaxContentRating not pushed down: %+v", cat.filters)
	}
}
