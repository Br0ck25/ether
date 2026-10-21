package stream

import (
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
)

func twoPartFile() *postedFile {
	f := &nzb.File{Name: "x.mkv", Segments: []nzb.Segment{{Number: 1, MessageID: "a"}, {Number: 2, MessageID: "b"}}}
	return newPostedFile(0, f, 0, nil)
}

// A yEnc header is posted by strangers: a short article claiming a huge
// part must not be zero-filled into a process-killing allocation.
func TestNormalizeIgnoresImplausiblePartHeader(t *testing.T) {
	e := newTestEngine(t, newFakeFetcher(), nil, Options{})
	pf := twoPartFile()
	data := []byte("short article")
	seg, err := e.normalize(pf, 0, data, core.ArticlePart{Name: "x.mkv", FileSize: 1 << 40, Part: 1, Total: 2, Begin: 1, End: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.data) != len(data) || seg.fileSize != 0 || seg.begin != 0 {
		t.Fatalf("implausible header applied: len=%d fileSize=%d begin=%d", len(seg.data), seg.fileSize, seg.begin)
	}
	// A damaged decoded body is never silently turned into container bytes.
	for _, data := range [][]byte{data, make([]byte, 200)} {
		_, err = e.normalize(pf, 0, data, core.ArticlePart{FileSize: 1000, Begin: 1, End: 100})
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("length mismatch: %v", err)
		}
	}
}

func TestLearnIgnoresAbsurdPartTotal(t *testing.T) {
	pf := twoPartFile()
	pf.learn(0, 0, 100, 150, 1<<30)
	if n := pf.numSlots(); n != 2 {
		t.Fatalf("slots = %d after an absurd total, want 2", n)
	}
	pf.learn(0, 0, 100, 350, 4)
	if n := pf.numSlots(); n != 4 {
		t.Fatalf("slots = %d after a plausible total, want 4", n)
	}
	// Growth is bounded by the NZB listing, so headers cannot compound it.
	pf.learn(1, 100, 200, 350, 2000)
	if n := pf.numSlots(); n != 4 {
		t.Fatalf("slots = %d after a compounding total, want 4", n)
	}
}
