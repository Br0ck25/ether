package archive

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

// span is one piece of a logical byte stream backed by a ReaderAt.
type span struct {
	r     io.ReaderAt
	start int64 // logical offset of the first byte
	off   int64 // offset inside r
	size  int64
}

// spanReader is an io.ReaderAt over consecutive spans.
type spanReader struct {
	spans []span
	size  int64
}

func (s *spanReader) readAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("archive: negative read offset %d", off)
	}
	if off >= s.size {
		if len(p) == 0 {
			return 0, nil
		}
		return 0, io.EOF
	}
	want := len(p)
	if rem := s.size - off; int64(want) > rem {
		want = int(rem)
	}
	// First span whose end is past off.
	i := sort.Search(len(s.spans), func(i int) bool {
		return s.spans[i].start+s.spans[i].size > off
	})
	n := 0
	for n < want && i < len(s.spans) {
		sp := s.spans[i]
		rel := off + int64(n) - sp.start
		chunk := want - n
		if avail := sp.size - rel; int64(chunk) > avail {
			chunk = int(avail)
		}
		if chunk > 0 {
			m, err := sp.r.ReadAt(p[n:n+chunk], sp.off+rel)
			n += m
			if m < chunk {
				if err == nil || errors.Is(err, io.EOF) {
					err = io.ErrUnexpectedEOF
				}
				return n, err
			}
		}
		i++
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// MultiReader is an io.ReaderAt over the concatenation of volumes, e.g. a
// split .7z.001/.7z.002… set.
type MultiReader struct {
	sr  spanReader
	err error
}

// NewMultiReader returns a reader over the concatenation of vols in order.
// Volumes with Size <= 0 contribute no bytes.
func NewMultiReader(vols []Volume) *MultiReader {
	m := &MultiReader{}
	var pos int64
	for _, v := range vols {
		if v.Size <= 0 {
			continue
		}
		if v.R == nil {
			m.err = fmt.Errorf("archive: volume has no reader: %w", ErrMissingVolume)
			return m
		}
		if v.Size > math.MaxInt64-pos {
			m.err = fmt.Errorf("archive: concatenated volume size overflows: %w", ErrCorrupt)
			return m
		}
		m.sr.spans = append(m.sr.spans, span{r: v.R, start: pos, off: 0, size: v.Size})
		pos += v.Size
	}
	m.sr.size = pos
	return m
}

// ReadAt implements io.ReaderAt.
func (m *MultiReader) ReadAt(p []byte, off int64) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.sr.readAt(p, off)
}

// Size returns the total size of all volumes.
func (m *MultiReader) Size() int64 { return m.sr.size }

// SplitExtents maps extents expressed as offsets in the concatenated stream
// (Volume must be 0, as returned by List7z over a MultiReader) onto extents
// of the individual volumes passed to NewMultiReader. Volumes with Size <= 0
// keep their index but never receive extents.
func SplitExtents(vols []Volume, exts []Extent) ([]Extent, error) {
	// Index once instead of scanning from volume zero for each member. Large
	// season packs can have thousands of both volumes and members. Keep empty
	// volumes out of the search while preserving the original volume indexes.
	type volumeSpan struct {
		index      int
		start, end int64
	}
	index := make([]volumeSpan, 0, len(vols))
	var total int64
	for vi, v := range vols {
		if v.Size <= 0 {
			continue
		}
		if v.Size > math.MaxInt64-total {
			return nil, fmt.Errorf("archive: concatenated volume size overflows: %w", ErrCorrupt)
		}
		index = append(index, volumeSpan{vi, total, total + v.Size})
		total += v.Size
	}
	out := make([]Extent, 0, len(exts))
	for _, e := range exts {
		if e.Volume != 0 || e.Offset < 0 || e.Size < 0 {
			return nil, fmt.Errorf("archive: extent %+v is not a concatenated-stream extent: %w", e, ErrCorrupt)
		}
		if e.Offset > total || e.Size > total-e.Offset {
			return nil, fmt.Errorf("archive: extent %+v runs past the last volume: %w", e, ErrMissingVolume)
		}
		off, rem := e.Offset, e.Size
		vi := sort.Search(len(index), func(i int) bool { return index[i].end > off })
		for rem > 0 {
			v := index[vi]
			n := min(v.end-off, rem)
			out = append(out, Extent{Volume: v.index, Offset: off - v.start, Size: n})
			off += n
			rem -= n
			vi++
		}
	}
	return out, nil
}

// MemberReader is an io.ReaderAt over a stored member's bytes.
type MemberReader struct {
	sr        spanReader
	encrypted io.ReaderAt
}

// NewMemberReader returns a reader over the bytes of a stored member. It
// fails with the Streamable error if the member cannot be read directly, and
// with ErrMissingVolume or ErrCorrupt if the extents do not fit vols.
func NewMemberReader(m Member, vols []Volume) (*MemberReader, error) {
	if m.Encryption != nil {
		r, err := newEncryptedMemberReader(vols, m)
		if err != nil {
			return nil, err
		}
		return &MemberReader{sr: spanReader{size: m.Size}, encrypted: r}, nil
	}
	if err := m.Streamable(); err != nil {
		return nil, err
	}
	mr := &MemberReader{}
	var pos int64
	lastVolume := -1
	if m.Size < 0 {
		return nil, fmt.Errorf("archive: member %q has negative size: %w", m.Name, ErrCorrupt)
	}
	for _, e := range m.Extents {
		if e.Volume < 0 || e.Volume >= len(vols) || vols[e.Volume].R == nil {
			return nil, fmt.Errorf("archive: member %q needs volume %d: %w", m.Name, e.Volume, ErrMissingVolume)
		}
		if e.Offset < 0 || e.Size < 0 || e.Offset > vols[e.Volume].Size || e.Size > vols[e.Volume].Size-e.Offset {
			return nil, fmt.Errorf("archive: member %q extent %+v outside volume of %d bytes: %w",
				m.Name, e, vols[e.Volume].Size, ErrCorrupt)
		}
		if e.Size == 0 {
			continue
		}
		if e.Size > math.MaxInt64-pos {
			return nil, fmt.Errorf("archive: member %q size overflows: %w", m.Name, ErrCorrupt)
		}
		if n := len(mr.sr.spans); n > 0 && lastVolume == e.Volume && mr.sr.spans[n-1].off+mr.sr.spans[n-1].size == e.Offset {
			// One upstream range read can cover physically contiguous pieces.
			mr.sr.spans[n-1].size += e.Size
		} else {
			mr.sr.spans = append(mr.sr.spans, span{r: vols[e.Volume].R, start: pos, off: e.Offset, size: e.Size})
		}
		lastVolume = e.Volume
		pos += e.Size
	}
	if pos != m.Size {
		return nil, fmt.Errorf("archive: member %q extents cover %d bytes, want %d: %w", m.Name, pos, m.Size, ErrCorrupt)
	}
	mr.sr.size = pos
	return mr, nil
}

// ReadAt implements io.ReaderAt: it returns io.EOF when fewer than len(p)
// bytes are available at off.
func (r *MemberReader) ReadAt(p []byte, off int64) (int, error) {
	if r.encrypted != nil {
		return r.encrypted.ReadAt(p, off)
	}
	return r.sr.readAt(p, off)
}

// Size returns the member size.
func (r *MemberReader) Size() int64 { return r.sr.size }
