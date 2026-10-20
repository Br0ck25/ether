package stream

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type mixedSampleFetcher struct {
	*fakeFetcher
	probes  atomic.Int32
	missing bool
}

func (f *mixedSampleFetcher) CheckBodyPart(ctx context.Context, _ string, _ []string, _ core.Priority, _ core.ArticlePart) error {
	if f.probes.Add(1) == 1 {
		return context.DeadlineExceeded
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Millisecond):
		if f.missing {
			return core.ErrArticleMissing
		}
		return nil
	}
}

func TestConfirmedMissingSampleWinsOverEarlierTransientSample(t *testing.T) {
	for _, missing := range []bool{true, false} {
		f := &mixedSampleFetcher{fakeFetcher: newFakeFetcher(), missing: missing}
		doc := post(t, f.fakeFetcher, 10_000, postFile{name: "movie.mkv", data: mkvBytes(1, 1_000_000)})
		e := newTestEngine(t, f, nil, Options{HealthSamples: 12, HealthParallel: 12})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ok, err := e.Check(ctx, doc)
		cancel()
		if ok || err == nil {
			t.Fatalf("missing=%v: healthy=%v error=%v", missing, ok, err)
		}
		if missing && !errors.Is(err, core.ErrArticleMissing) {
			t.Fatalf("timeout hid a confirmed missing article: %v", err)
		}
		if !missing && (!errors.Is(err, context.DeadlineExceeded) || errors.Is(err, core.ErrArticleMissing)) {
			t.Fatalf("transient failure became a false missing verdict: %v", err)
		}
	}
}
