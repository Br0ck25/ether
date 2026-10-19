package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
)

func TestHealthSampleSpread(t *testing.T) {
	mk := func(n int) *postedFile {
		f := &nzb.File{Name: "v"}
		for i := 1; i <= n; i++ {
			f.Segments = append(f.Segments, nzb.Segment{Number: i, MessageID: "x"})
		}
		return newPostedFile(0, f, 0, nil)
	}
	a, b, c := mk(40), mk(40), mk(20)
	refs := healthSample([]*postedFile{a, b, c}, 12)
	if len(refs) != 12 {
		t.Fatalf("samples = %d", len(refs))
	}
	if refs[0].pf != a || refs[0].slot != 0 {
		t.Fatalf("first sample = %+v", refs[0])
	}
	if last := refs[len(refs)-1]; last.pf != c || last.slot != 19 {
		t.Fatalf("last sample = %+v", last)
	}
	seen := map[*postedFile]bool{}
	for _, r := range refs {
		seen[r.pf] = true
	}
	if len(seen) != 3 {
		t.Fatal("not every volume sampled")
	}
	// Fewer slots than samples: all of them, once.
	if refs := healthSample([]*postedFile{mk(5)}, 12); len(refs) != 5 {
		t.Fatalf("small sample = %d", len(refs))
	}
	if refs := healthSample([]*postedFile{mk(100)}, 1); len(refs) != 2 {
		t.Fatalf("minimum sample = %d", len(refs))
	}
}

func TestCheckHealthy(t *testing.T) {
	f := newFakeFetcher()
	doc := post(t, f, 10_000, postFile{name: "h.mkv", data: mkvBytes(1, 1_000_000)})
	e := newTestEngine(t, f, nil, Options{HealthSamples: 12})
	ok, err := e.Check(context.Background(), doc)
	if !ok || err != nil {
		t.Fatalf("Check = %v %v", ok, err)
	}
	f.mu.Lock()
	stats := f.statCalls
	f.mu.Unlock()
	if stats != 10 {
		t.Fatalf("STATs = %d, want 10 plus two BODY samples", stats)
	}
	// Open after Check reuses the index: no further header fetch.
	f.resetCounts()
	src := openSource(t, e, "h", doc)
	if f.totalCalls() != 0 {
		t.Fatalf("Open after Check fetched %d", f.totalCalls())
	}
	_ = src
}

func TestCheckMissingSample(t *testing.T) {
	f := newFakeFetcher()
	doc := post(t, f, 10_000, postFile{name: "h.mkv", data: mkvBytes(1, 1_000_000)})
	f.setMissing(postID("h.mkv", 100)) // the last part is always sampled
	e := newTestEngine(t, f, nil, Options{HealthSamples: 5})
	ok, err := e.Check(context.Background(), doc)
	if ok || !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("Check = %v %v", ok, err)
	}
}

func TestCheckRejections(t *testing.T) {
	f := newFakeFetcher()
	e := newTestEngine(t, f, nil, Options{})
	noVideo := post(t, f, 1000, postFile{name: "x.nfo", data: []byte("x")}, postFile{name: "x.sample.mkv", data: mkvBytes(1, 2000)})
	if ok, err := e.Check(context.Background(), noVideo); ok || !errors.Is(err, ErrNoPlayableFile) || !IsRejection(err) {
		t.Fatalf("no video: %v %v", ok, err)
	}
	gap := post(t, f, 1000, postFile{name: "gap.mkv", data: mkvBytes(2, 5000), omit: []int{2}})
	if ok, err := e.Check(context.Background(), gap); ok || !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("gap: %v %v", ok, err)
	}
	if ok, err := e.Check(context.Background(), []byte("<nzb")); ok || !errors.Is(err, ErrInvalidNZB) {
		t.Fatalf("invalid: %v %v", ok, err)
	}
	missingRAR := post(t, f, 1000,
		postFile{name: "m.part01.rar", data: randBytes(1, 3000)},
		postFile{name: "m.part03.rar", data: randBytes(2, 3000)},
	)
	if ok, err := e.Check(context.Background(), missingRAR); ok || !errors.Is(err, ErrIncomplete) {
		t.Fatalf("missing volume: %v %v", ok, err)
	}
}

type bodyUnavailableFetcher struct{ *fakeFetcher }

func (f bodyUnavailableFetcher) Stat(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}
func (f bodyUnavailableFetcher) CheckBody(context.Context, string, []string, core.Priority) error {
	return core.ErrArticleMissing
}
func (f bodyUnavailableFetcher) BodyPart(ctx context.Context, _ string, _ []string, _ core.Priority) ([]byte, core.ArticlePart, error) {
	<-ctx.Done()
	return nil, core.ArticlePart{}, ctx.Err()
}
func TestBodyTakedownCancelsSlowHeaderAndStatProbes(t *testing.T) {
	f := newFakeFetcher()
	doc := post(t, f, 10000, postFile{name: "taken.mkv", data: mkvBytes(1, 1000000)})
	e := newTestEngine(t, bodyUnavailableFetcher{f}, nil, Options{HealthSamples: 12, HealthParallel: 12})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	ok, err := e.Check(ctx, doc)
	if ok || !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("BODY takedown: %v %v", ok, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("waited for slow header/STAT instead of cancelling")
	}
}
