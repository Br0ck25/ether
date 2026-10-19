package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func readFixture(t testing.TB, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func memVols(bufs ...[]byte) []Volume {
	vols := make([]Volume, len(bufs))
	for i, b := range bufs {
		vols[i] = Volume{R: bytes.NewReader(b), Size: int64(len(b))}
	}
	return vols
}

func randBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// readAll reads a ReaderAt of known size with ReadAt in one go.
func readAll(t testing.TB, r io.ReaderAt, size int64) []byte {
	t.Helper()
	buf := make([]byte, size)
	n, err := r.ReadAt(buf, 0)
	if n != len(buf) || (err != nil && err != io.EOF) {
		t.Fatalf("ReadAt full: n=%d err=%v", n, err)
	}
	return buf
}

// checkRandomReads compares many ReadAt calls (random offsets and lengths,
// plus reads straddling every boundary in cuts) against want.
func checkRandomReads(t testing.TB, r io.ReaderAt, want []byte, cuts []int64, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 1))
	size := int64(len(want))
	check := func(off int64, n int) {
		t.Helper()
		buf := make([]byte, n)
		got, err := r.ReadAt(buf, off)
		end := min(off+int64(n), size)
		wantN := int(max(end-off, 0))
		if got != wantN {
			t.Fatalf("ReadAt(%d, len %d) = %d bytes, want %d (err %v)", off, n, got, wantN, err)
		}
		if off+int64(n) > size {
			if err != io.EOF {
				t.Fatalf("ReadAt(%d, len %d) past end: err %v, want io.EOF", off, n, err)
			}
		} else if err != nil {
			t.Fatalf("ReadAt(%d, len %d): %v", off, n, err)
		}
		if wantN > 0 && !bytes.Equal(buf[:got], want[off:end]) {
			t.Fatalf("ReadAt(%d, len %d): bytes differ", off, n)
		}
	}
	for i := 0; i < 300 && size > 0; i++ {
		off := rng.Int64N(size)
		check(off, 1+rng.IntN(4096))
	}
	for _, c := range cuts {
		for _, d := range []int64{-3, -1, 0} {
			if c+d >= 0 && c+d < size {
				check(c+d, 7)
			}
		}
	}
	check(0, int(size))
	check(size, 1)
	check(max(size-5, 0), 10)
}

// extentCuts returns the member-relative offsets where extents change.
func extentCuts(m Member) []int64 {
	var cuts []int64
	var pos int64
	for _, e := range m.Extents {
		pos += e.Size
		cuts = append(cuts, pos)
	}
	return cuts
}

// recReader records every ReadAt call.
type recReader struct {
	r     io.ReaderAt
	mu    sync.Mutex
	reads [][2]int64 // off, len
}

func (r *recReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	r.reads = append(r.reads, [2]int64{off, int64(len(p))})
	r.mu.Unlock()
	return r.r.ReadAt(p, off)
}

func (r *recReader) bytesRead() int64 {
	var n int64
	for _, rd := range r.reads {
		n += rd[1]
	}
	return n
}

// maxEnd returns the highest offset touched by any read.
func (r *recReader) maxEnd() int64 {
	var m int64
	for _, rd := range r.reads {
		m = max(m, rd[0]+rd[1])
	}
	return m
}

// assertNoDataReads fails if any recorded read overlaps [off, off+n).
func (r *recReader) assertNoDataReads(t testing.TB, off, n int64, what string) {
	t.Helper()
	for _, rd := range r.reads {
		if rd[0] < off+n && off < rd[0]+rd[1] {
			t.Fatalf("read [%d,+%d) overlaps %s data [%d,+%d)", rd[0], rd[1], what, off, n)
		}
	}
}

var errSentinel = errors.New("test: network down")

// failReader returns a wrapped sentinel error for reads at or after failAt.
type failReader struct {
	r      io.ReaderAt
	failAt int64
}

func (f failReader) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.failAt {
		n := 0
		if off < f.failAt {
			n, _ = f.r.ReadAt(p[:f.failAt-off], off)
		}
		return n, fmt.Errorf("fetch segment at %d: %w", off, errSentinel)
	}
	return f.r.ReadAt(p, off)
}

// sparseReader serves header bytes from pieces and fails the test on any
// read that touches a hole (file data that must never be read).
type sparseReader struct {
	t      testing.TB
	pieces []sparsePiece
	size   int64
}

type sparsePiece struct {
	off  int64
	data []byte
}

func newSparse(t testing.TB) *sparseReader { return &sparseReader{t: t} }

func (s *sparseReader) add(b []byte) *sparseReader {
	if n := len(s.pieces); n > 0 && s.pieces[n-1].off+int64(len(s.pieces[n-1].data)) == s.size {
		s.pieces[n-1].data = append(bytes.Clone(s.pieces[n-1].data), b...)
		s.size += int64(len(b))
		return s
	}
	s.pieces = append(s.pieces, sparsePiece{s.size, b})
	s.size += int64(len(b))
	return s
}

func (s *sparseReader) hole(n int64) *sparseReader { s.size += n; return s }

func (s *sparseReader) ReadAt(p []byte, off int64) (int, error) {
	for _, pc := range s.pieces {
		if off >= pc.off && off+int64(len(p)) <= pc.off+int64(len(pc.data)) {
			return copy(p, pc.data[off-pc.off:]), nil
		}
	}
	s.t.Errorf("sparse read [%d,+%d) outside header pieces", off, len(p))
	return 0, errors.New("sparse: read of a hole")
}
