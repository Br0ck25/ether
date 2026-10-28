package stream

import (
	"errors"
	"math"
	"testing"

	"github.com/braedonsaunders/ether/internal/archive"
)

func TestArchiveLayoutRejectsOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind planKind
		size int64
		info []volInfo
		ext  []archive.Extent
	}{
		{"volume total", kind7z, 1, []volInfo{{size: math.MaxInt64}, {size: 1}}, []archive.Extent{{Size: 1}}},
		{"extent sum", kind7z, math.MaxInt64, []volInfo{{size: math.MaxInt64}}, []archive.Extent{{Size: math.MaxInt64}, {Size: 1}}},
		{"negative extent", kind7z, 1, []volInfo{{size: 10}}, []archive.Extent{{Offset: -1, Size: 1}}},
		{"range overflow", kindRAR, 100, []volInfo{{size: math.MaxInt64, dataOff: math.MaxInt64 - 1, dataSize: 100}}, nil},
		{"negative layout", kindPlain, -1, []volInfo{{size: 1, dataSize: 1}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vols := make([]*postedFile, len(tc.info))
			for i := range vols {
				vols[i] = &postedFile{}
			}
			p := newPlan(nil, tc.kind, "test", tc.size, vols, tc.info, make([]bool, len(vols)))
			p.concatExt = tc.ext
			if err := p.init(); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted invalid layout: %v", err)
			}
		})
	}
}

func TestLayoutPiecesOverflowingLength(t *testing.T) {
	l := layout{size: 20, ext: []extent{{inner: 0, size: 20, vol: 0, off: 5}}}
	p := l.pieces(10, math.MaxInt64)
	if len(p) != 1 || p[0].n != 10 || p[0].off != 15 {
		t.Fatalf("overflowing read: %+v", p)
	}
	if got := l.pieces(-1, 1); len(got) != 0 {
		t.Fatalf("negative offset: %+v", got)
	}
}

func BenchmarkArchiveLayoutLargePack(b *testing.B) {
	const count = 4096
	vols, info, verified := make([]*postedFile, count), make([]volInfo, count), make([]bool, count)
	for i := range vols {
		vols[i] = &postedFile{}
		info[i].size = 128 << 20
		verified[i] = true
	}
	p := newPlan(nil, kind7z, "movie.mkv", count*(128<<20), vols, info, verified)
	p.concatExt = []archive.Extent{{Size: p.size}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := p.buildLocked(1); err != nil {
			b.Fatal(err)
		}
	}
}
