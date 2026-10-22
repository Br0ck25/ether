package stream

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
	"time"
)

type budgetFetcher struct{ *fakeFetcher }

func (f budgetFetcher) BodyPartBurst(context.Context, string, []string, core.Priority, time.Time) ([]byte, core.ArticlePart, error) {
	return nil, core.ArticlePart{}, errors.New("background health attempted a playback repair burst")
}

func (f budgetFetcher) BodyPart(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	if err := core.SpendBackground(ctx, "bytes", "fake", 100); err != nil {
		return nil, core.ArticlePart{}, err
	}
	return f.fakeFetcher.BodyPart(ctx, id, groups, prio)
}
func TestSharedSegmentWorkerRetainsBackgroundBudget(t *testing.T) {
	f := newFakeFetcher()
	doc := post(t, f, 10000, postFile{name: "test.mkv", data: mkvBytes(1, 100000)})
	e := newTestEngine(t, budgetFetcher{f}, nil, Options{})
	rel, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(rel)
	ctx := core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error { return core.ErrBudgetExhausted })
	if _, err = e.segment(ctx, rel, rel.files[0], 0, core.PriorityLive, fetchOpts{burstUntil: time.Now().Add(time.Second)}); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("lost budget in shared worker: %v", err)
	}
	if f.totalCalls() != 0 {
		t.Fatal("denied validation fetched article")
	}
	if _, err = e.segment(t.Context(), rel, rel.files[0], 0, core.PriorityLive, fetchOpts{}); err != nil {
		t.Fatal("background denial contaminated playback", err)
	}
	if f.totalCalls() != 1 {
		t.Fatal("foreground failed to fetch")
	}
}
