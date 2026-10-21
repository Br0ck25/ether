package stream

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/braedonsaunders/ether/internal/archive/archivetest"
	"github.com/braedonsaunders/ether/internal/core"
)

// obfuscatedRARPost posts an unnumbered RAR4 set of n volumes under
// obfuscated names in a shuffled order, with a PAR2 index naming every
// volume, and returns the volumes in archive order with the NZB.
func obfuscatedRARPost(t *testing.T, f *fakeFetcher, seed uint64, n int) (video []byte, vols [][]byte, doc []byte) {
	t.Helper()
	const volSize = 40_000
	video = mkvBytes(seed, n*volSize-volSize/2)
	vols = buildRAR(t, []archivetest.RARFile{{Name: "Real.Name.mkv", Data: video}},
		archivetest.RAROptions{Version: 4, VolumeSize: volSize, OmitEndVolumeNumber: true})
	if len(vols) != n {
		t.Fatalf("built %d volumes, want %d", len(vols), n)
	}
	order := rand.New(rand.NewPCG(seed, seed)).Perm(len(vols))
	idx := par2Index(65_536, rarPar2Files(vols)...)
	return video, vols, postShuffled(t, f, vols, order, postFile{name: "a1b2c3d4e5f6.par2", data: idx})
}

// Friends S07E01 (2026-10-21): 85 obfuscated volumes, 64 probed, and the
// 21 unprobed volumes were reported as "lacks volumes ...: article missing
// on all providers" although every probed head was present.
func TestObfuscatedSetBeyondProbeBudgetIsIdentified(t *testing.T) {
	f := newFakeFetcher()
	video, vols, doc := obfuscatedRARPost(t, f, 51, 85)
	e := newTestEngine(t, f, nil, Options{})
	if e.opts.MaxProbes != 64 {
		t.Fatalf("default MaxProbes %d, fixture assumes 64", e.opts.MaxProbes)
	}
	src := openSource(t, e, "beyondBudget", doc)
	if src.Name() != "Real.Name.mkv" || src.Size() != int64(len(video)) {
		t.Fatalf("name %q size %d", src.Name(), src.Size())
	}
	// Each head is read from the provider once: the continuation round
	// re-reads the first 64 from the hot buffer.
	for k := range vols {
		if n := f.calls(postID(obfName(10+k), 1)); n != 1 {
			t.Fatalf("volume %d head fetched %d times", k, n)
		}
	}
	checkRandomReads(t, src, video, 6, 40)
	ok, err := e.CheckRelease(context.Background(), "beyondBudget", doc)
	if !ok || err != nil {
		t.Fatalf("Check = %v %v", ok, err)
	}
}

func TestObfuscatedSetShortOfVolumesIsNotMissing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  Options
		setup func(f *fakeFetcher)
	}{
		// 20 volumes, at most 4*4 heads probed: 4 never read.
		{"probe budget exhausted", Options{MaxProbes: 4}, nil},
		// One head keeps failing transiently (beyond the retries).
		{"unreadable head", Options{}, func(f *fakeFetcher) { f.failures[postID(obfName(10+7), 1)] = 1000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFetcher()
			_, _, doc := obfuscatedRARPost(t, f, 52, 20)
			if tc.setup != nil {
				tc.setup(f)
			}
			e := newTestEngine(t, f, nil, tc.opts)
			ok, err := e.CheckRelease(context.Background(), "short", doc)
			if ok || !errors.Is(err, errUnidentifiedVolumes) {
				t.Fatalf("Check = %v %v, want errUnidentifiedVolumes", ok, err)
			}
			// The resolver treats these as permanent source failures.
			if errors.Is(err, core.ErrArticleMissing) || errors.Is(err, core.ErrReleaseRejected) || IsRejection(err) {
				t.Fatalf("probe shortfall classified as a permanent failure: %v", err)
			}
		})
	}
}

func TestObfuscatedSetWithMissingHeadStaysMissing(t *testing.T) {
	f := newFakeFetcher()
	_, _, doc := obfuscatedRARPost(t, f, 53, 20)
	f.setMissing(postID(obfName(10+7), 1))
	e := newTestEngine(t, f, nil, Options{})
	ok, err := e.CheckRelease(context.Background(), "missing", doc)
	if ok || !errors.Is(err, core.ErrArticleMissing) || errors.Is(err, errUnidentifiedVolumes) {
		t.Fatalf("Check = %v %v, want a missing article", ok, err)
	}
}
