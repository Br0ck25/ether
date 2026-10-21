package archive

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

func TestReaderRejectsOverflowingLayout(t *testing.T) {
	vols := []Volume{{R: bytes.NewReader(nil), Size: math.MaxInt64}, {R: bytes.NewReader(nil), Size: 1}}
	if _, err := NewMultiReader(vols).ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("overflowing concatenation: %v", err)
	}
	m := Member{Name: "overflow", Stored: true, Size: math.MinInt64, Extents: []Extent{{Size: math.MaxInt64}, {Volume: 1, Size: 1}}}
	if _, err := NewMemberReader(m, vols); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("overflowing member accepted: %v", err)
	}
	if _, err := SplitExtents(vols, []Extent{{Size: 1}}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("overflowing volume index accepted: %v", err)
	}
}

func TestMultiReaderMissingReader(t *testing.T) {
	r := NewMultiReader([]Volume{{Size: 1}})
	if _, err := r.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrMissingVolume) {
		t.Fatalf("missing reader: %v", err)
	}
}

func TestMemberReaderRejectsNegativeVolumeBeforeSubtraction(t *testing.T) {
	m := Member{Name: "invalid", Stored: true, Size: 1, Extents: []Extent{{Offset: 1, Size: 1}}}
	_, err := NewMemberReader(m, []Volume{{R: bytes.NewReader(nil), Size: math.MinInt64}})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("negative volume accepted: %v", err)
	}
}

func TestSplitExtentsSparseAndUnordered(t *testing.T) {
	vols := []Volume{{Size: 2}, {}, {Size: 3}, {Size: 4}}
	extents := []Extent{{Offset: 6, Size: 3}, {Offset: 1, Size: 6}}
	got, err := SplitExtents(vols, extents)
	if err != nil {
		t.Fatal(err)
	}
	want := []Extent{{Volume: 3, Offset: 1, Size: 3}, {Volume: 0, Offset: 1, Size: 1}, {Volume: 2, Size: 3}, {Volume: 3, Size: 2}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

type countedReader struct {
	io.ReaderAt
	calls int
}

func (r *countedReader) ReadAt(p []byte, off int64) (int, error) {
	r.calls++
	return r.ReaderAt.ReadAt(p, off)
}

func TestMemberReaderCoalescesAdjacentExtents(t *testing.T) {
	r := &countedReader{ReaderAt: bytes.NewReader([]byte("0123456789"))}
	m := Member{Name: "movie", Stored: true, Size: 6, Extents: []Extent{{Offset: 1, Size: 2}, {Offset: 3, Size: 2}, {Offset: 5, Size: 2}}}
	mr, err := NewMemberReader(m, []Volume{{R: r, Size: 10}})
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	if _, err := mr.ReadAt(b, 0); err != nil {
		t.Fatal(err)
	}
	if string(b) != "123456" || r.calls != 1 {
		t.Fatalf("data %q, upstream calls %d", b, r.calls)
	}
}

func BenchmarkSplitExtentsLargePack(b *testing.B) {
	const count = 4096
	vols := make([]Volume, count)
	extents := make([]Extent, count)
	for i := range vols {
		vols[i].Size = 128 << 20
		extents[i] = Extent{Offset: int64(i) * (128 << 20), Size: 1 << 20}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := SplitExtents(vols, extents); err != nil {
			b.Fatal(err)
		}
	}
}
