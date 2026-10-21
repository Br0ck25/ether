package stream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/archive/archivetest"
	"github.com/braedonsaunders/ether/internal/core"
)

// The first article identifies an obfuscated video but omits the file size.
// Planning must therefore read the last article. Requiring the health sample
// to start before allowing that read proves overlap without a timing race.
type overlappingHealthFetcher struct {
	*fakeFetcher
	first, last string
	sampling    chan struct{}
	once        sync.Once
}

func (f *overlappingHealthFetcher) BodyPart(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	if id == f.last {
		select {
		case <-f.sampling:
		case <-ctx.Done():
			return nil, core.ArticlePart{}, ctx.Err()
		}
	}
	b, part, err := f.fakeFetcher.BodyPart(ctx, id, groups, prio)
	if id == f.first {
		part.FileSize = 0
	}
	return b, part, err
}

func (f *overlappingHealthFetcher) StatStream(ctx context.Context, ids []string, report func(string, bool) bool) error {
	f.once.Do(func() { close(f.sampling) })
	for _, id := range ids {
		present, err := f.fakeFetcher.Stat(ctx, id)
		if err != nil {
			return err
		}
		if !report(id, present) {
			return nil
		}
	}
	return nil
}

func TestObfuscatedHealthSamplingOverlapsPlan(t *testing.T) {
	base := newFakeFetcher()
	name := obfName(1)
	doc := post(t, base, 10_000, postFile{name: name, data: mkvBytes(73, 200_000)})
	f := &overlappingHealthFetcher{fakeFetcher: base, first: postID(name, 1), last: postID(name, 20), sampling: make(chan struct{})}
	e := newTestEngine(t, f, nil, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if healthy, err := e.Check(ctx, doc); !healthy || err != nil {
		t.Fatalf("health sampling did not overlap planning: healthy=%v err=%v", healthy, err)
	}
	trace := e.HealthTimings()
	if trace.Samples == 0 || trace.BodyProbes != 2 || trace.BodyOK != 2 {
		t.Fatalf("validation was skipped: %+v", trace)
	}
	if got := base.calls(f.last); got != 1 {
		t.Fatalf("plan did not fetch required tail: calls=%d", got)
	}
}

func TestObfuscatedHealthCancellationBeforeIdentification(t *testing.T) {
	f := newFakeFetcher()
	f.latency = time.Hour
	doc := post(t, f, 10_000, postFile{name: obfName(1), data: mkvBytes(74, 200_000)})
	e := newTestEngine(t, f, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if healthy, err := e.Check(ctx, doc); healthy || !errors.Is(err, context.Canceled) {
		t.Fatalf("Check = %v, %v", healthy, err)
	}
	if active := f.inflight.Load(); active != 0 {
		t.Fatalf("fetches outlived canceled check: %d", active)
	}
}

func TestObfuscatedHealthSamplesFinalCandidateAfterFallthrough(t *testing.T) {
	f := newFakeFetcher()
	archive := buildRAR(t, []archivetest.RARFile{{Name: "notes.nfo", Data: randBytes(75, 2_000_000)}}, archivetest.RAROptions{Version: 5})
	name := obfName(2)
	doc := post(t, f, 50_000,
		postFile{name: name, data: archive[0]},
		postFile{name: "movie.mkv", data: mkvBytes(76, 200_000)},
	)
	// This sampled article belongs to the larger non-video archive. Its
	// missing payload must not reject the smaller playable candidate.
	f.setMissing(postID(name, 7))
	e := newTestEngine(t, f, nil, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if healthy, err := e.Check(ctx, doc); !healthy || err != nil {
		t.Fatalf("speculative archive sample rejected final video: healthy=%v err=%v", healthy, err)
	}
	if trace := e.HealthTimings(); trace.FailReason != "" || trace.Samples != 4 {
		t.Fatalf("trace retained discarded candidate: %+v", trace)
	}
}
