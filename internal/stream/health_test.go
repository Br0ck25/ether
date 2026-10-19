package stream

import (
	"context"
	"errors"
	"testing"

	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/nzb"
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
	if stats != 12 {
		t.Fatalf("STATs = %d, want 12", stats)
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
