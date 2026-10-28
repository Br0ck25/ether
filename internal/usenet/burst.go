package usenet

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const maxBurstWindow = 500 * time.Millisecond

type burstKey struct{}

func burstUntil(ctx context.Context) time.Time {
	u, _ := ctx.Value(burstKey{}).(time.Time)
	return u
}

func (f *Fetcher) BodyPartBurst(ctx context.Context, id string, groups []string, prio core.Priority, until time.Time) ([]byte, core.ArticlePart, error) {
	if limit := time.Now().Add(maxBurstWindow); until.After(limit) {
		until = limit
	}
	if prio == core.PriorityReadahead && time.Now().Before(until) {
		ctx = context.WithValue(ctx, burstKey{}, until)
	}
	return f.BodyPart(ctx, id, groups, prio)
}
