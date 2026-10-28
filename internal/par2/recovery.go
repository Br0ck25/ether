package par2

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
	"sync"
)

// Main describes only the protected files, in PAR2 coefficient order.
type Main struct {
	SliceSize int64
	Files     []ID
	Invalid   bool
}

func sameMain(a, b Main) bool {
	if a.Invalid || a.SliceSize != b.SliceSize || len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			return false
		}
	}
	return true
}

type Block struct {
	File           *File
	Offset, Length int64
	Check          Slice
}
type Layout struct {
	ID        ID
	SliceSize int64
	Blocks    []Block
}

// RecoveryLayout rejects incomplete metadata rather than guessing coefficient
// order, treating unprotected files as protected, or allocating from huge sizes.
func (s *Set) RecoveryLayout(id ID, maxSlice int64) (*Layout, error) {
	m, ok := s.Main[id]
	if !ok || m.Invalid || m.SliceSize <= 0 || m.SliceSize > maxSlice {
		return nil, errors.New("par2: invalid or oversized recovery layout")
	}
	l := &Layout{ID: id, SliceSize: m.SliceSize}
	seen := map[ID]bool{}
	for _, fid := range m.Files {
		if seen[fid] {
			return nil, errors.New("par2: duplicate protected file")
		}
		seen[fid] = true
		var f *File
		for _, v := range s.Files {
			if v.SetID == id && v.ID == fid {
				f = v
				break
			}
		}
		if f == nil || f.SliceSize != m.SliceSize || int64(len(f.Slices)) != (f.Length+m.SliceSize-1)/m.SliceSize {
			return nil, errors.New("par2: incomplete slice checksums")
		}
		if len(l.Blocks)+len(f.Slices) > 32768 {
			return nil, errors.New("par2: too many input slices")
		}
		for i, c := range f.Slices {
			off := int64(i) * m.SliceSize
			l.Blocks = append(l.Blocks, Block{f, off, min(m.SliceSize, f.Length-off), c})
		}
	}
	if len(l.Blocks) == 0 {
		return nil, errors.New("par2: empty recovery set")
	}
	return l, nil
}

// Recovery contains one fully checksummed packet's recovery payload.
type Recovery struct {
	Exponent uint32
	Data     []byte
}

// RecoveryPackets tolerates lost packets, but never returns unverified payloads.
func RecoveryPackets(data []byte, id ID, size int64, limit int) []Recovery {
	var out []Recovery
	seen := map[uint32]bool{}
	for pos := 0; pos < len(data) && len(out) < limit; {
		k := bytes.Index(data[pos:], []byte(Magic))
		if k < 0 {
			break
		}
		at := pos + k
		p, ok := packetAt(data, at)
		if !ok {
			pos = at + 1
			continue
		}
		pos = at + len(p)
		if string(p[48:64]) != TypeRecovery || !bytes.Equal(p[32:48], id[:]) || int64(len(p)) != HeaderSize+4+size {
			continue
		}
		exp := binary.LittleEndian.Uint32(p[64:68])
		if seen[exp%65535] {
			continue
		}
		seen[exp%65535] = true
		out = append(out, Recovery{exp, bytes.Clone(p[68:])})
	}
	return out
}

// Field is PAR2's GF(2^16), constructed explicitly (no mutable global tables).
type Field struct {
	exp       []uint16
	log       []uint16
	constants []uint16
}

func NewField() *Field {
	f := &Field{exp: make([]uint16, 131070), log: make([]uint16, 65536)}
	x := uint32(1)
	for i := 0; i < 65535; i++ {
		f.exp[i] = uint16(x)
		f.log[x] = uint16(i)
		x <<= 1
		if x&65536 != 0 {
			x ^= 0x1100b
		}
	}
	copy(f.exp[65535:], f.exp[:65535])
	for i := 1; i < 65535; i++ {
		if i%3 != 0 && i%5 != 0 && i%17 != 0 && i%257 != 0 {
			f.constants = append(f.constants, f.exp[i])
		}
	}
	return f
}
func (f *Field) Mul(a, b uint16) uint16 {
	if a == 0 || b == 0 {
		return 0
	}
	return f.exp[int(f.log[a])+int(f.log[b])]
}
func (f *Field) inverse(a uint16) uint16 { return f.exp[65535-int(f.log[a])] }
func (f *Field) Coefficient(block int, exponent uint32) uint16 {
	return f.exp[(uint64(f.log[f.constants[block]])*uint64(exponent))%65535]
}
func (f *Field) addMul(dst, src []byte, c uint16) {
	if c == 0 {
		return
	}
	if c == 1 {
		for i, v := range src {
			dst[i] ^= v
		}
		return
	}
	var lo, hi [256]uint16
	for i := range lo {
		lo[i] = f.Mul(uint16(i), c)
		hi[i] = f.Mul(uint16(i)<<8, c)
	}
	for i := 0; i < len(src); i += 2 {
		v := lo[src[i]] ^ hi[src[i+1]]
		dst[i] ^= byte(v)
		dst[i+1] ^= byte(v >> 8)
	}
}
func (b Block) Verify(data []byte) bool {
	return md5.Sum(data) == b.Check.MD5 && crc32.ChecksumIEEE(data) == b.Check.CRC32
}

// Summaries retain verified contributions to fixed recovery equations. Caller
// serializes access. A coverage bit is committed only after the contribution.
// Duplicate observations are harmless. No raw source data is retained here.
type Summaries struct {
	Exponents []uint32
	Data      [][]byte
	Seen      []bool
	layout    *Layout
	field     *Field
}

func NewSummaries(l *Layout, f *Field, exponents []uint32, maxBytes int64) (*Summaries, error) {
	if len(exponents) == 0 || int64(len(exponents))*l.SliceSize > maxBytes {
		return nil, errors.New("par2: summary budget exceeded")
	}
	s := &Summaries{Exponents: append([]uint32(nil), exponents...), Seen: make([]bool, len(l.Blocks)), layout: l, field: f}
	for range exponents {
		s.Data = append(s.Data, make([]byte, l.SliceSize))
	}
	return s, nil
}
func (s *Summaries) Observe(block int, data []byte) bool {
	if block < 0 || block >= len(s.Seen) || int64(len(data)) != s.layout.SliceSize || s.Seen[block] || !s.layout.Blocks[block].Verify(data) {
		return false
	}
	for i, e := range s.Exponents {
		s.field.addMul(s.Data[i], data, s.field.Coefficient(block, e))
	}
	s.Seen[block] = true
	return true
}

// Hole is a known unavailable byte interval within an input slice.
type Hole struct {
	Block      int
	Begin, End int64
}

// ReadRange reads exactly len(dst) bytes within one padded input slice. Missing
// or corrupt data must wrap ErrErasure; transport failures must not.
type ReadRange func(context.Context, int, int64, []byte) error

var ErrErasure = errors.New("par2: missing or corrupt range")
var ErrInsufficient = errors.New("par2: insufficient independent recovery equations")

type RepairOptions struct {
	WholeSlices bool
	ChunkBytes  int
	MaxTargets  int
}
type RepairStats struct {
	InputBytes     int64
	SummaryBytes   int64
	RecoveredBytes int64
}

// Recover reconstructs and verifies complete target slices, but reads only the
// damaged intervals from other slices. Helpers missing at the same interval
// become additional unknowns. Nothing is returned until ALL targets verify.
func (l *Layout) Recover(ctx context.Context, f *Field, holes []Hole, read ReadRange, parity []Recovery, s *Summaries, opts RepairOptions) (map[int][]byte, RepairStats, error) {
	var st RepairStats
	if len(parity) == 0 || len(parity) > 64 {
		return nil, st, ErrInsufficient
	}
	if opts.ChunkBytes <= 0 {
		opts.ChunkBytes = 64 << 10
	}
	opts.ChunkBytes = max(2, opts.ChunkBytes&^1)
	if opts.MaxTargets <= 0 {
		opts.MaxTargets = 16
	}
	holes = append([]Hole(nil), holes...)
	targets := map[int][]byte{}
	for i, h := range holes {
		if h.Block < 0 || h.Block >= len(l.Blocks) || h.Begin < 0 || h.End <= h.Begin || h.End > l.SliceSize {
			return nil, st, errors.New("par2: invalid repair hole")
		}
		if opts.WholeSlices {
			h.Begin = 0
			h.End = l.SliceSize
		} else {
			h.Begin &= ^int64(1)
			h.End = (h.End + 1) &^ int64(1)
		}
		holes[i] = h
		if targets[h.Block] == nil {
			if len(targets) >= opts.MaxTargets {
				return nil, st, errors.New("par2: too many targets")
			}
			targets[h.Block] = make([]byte, l.SliceSize)
		}
	}
	if len(targets) == 0 {
		return nil, st, errors.New("par2: no repair targets")
	}
	for _, p := range parity {
		if int64(len(p.Data)) != l.SliceSize {
			return nil, st, errors.New("par2: wrong recovery size")
		}
	}
	// A summary cannot hide a currently requested target: it must be reconstructed
	// from raw data or from a different equation, not counted twice.
	if s != nil {
		if s.layout != l {
			return nil, st, errors.New("par2: foreign summary")
		}
		for b := range targets {
			if s.Seen[b] {
				s = nil
				break
			}
		}
	}
	bounds := []int64{0, l.SliceSize}
	for _, h := range holes {
		bounds = append(bounds, h.Begin, h.End)
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	bounds = uniqueOffsets(bounds)
	for k := 0; k < len(bounds)-1; k++ {
		for off := bounds[k]; off < bounds[k+1]; {
			if err := ctx.Err(); err != nil {
				return nil, st, err
			}
			end := min(bounds[k+1], off+int64(opts.ChunkBytes))
			n := int(end - off)
			missing := map[int]bool{}
			for _, h := range holes {
				if h.Begin <= off && h.End >= end {
					missing[h.Block] = true
				}
			}
			if len(missing) == 0 {
				for b, d := range targets {
					if err := read(ctx, b, off, d[off:end]); err != nil {
						return nil, st, err
					}
					st.InputBytes += int64(n)
				}
				off = end
				continue
			}
			rhs := make([][]byte, len(parity))
			summaryRows := make([]int, len(parity))
			for j, p := range parity {
				rhs[j] = bytes.Clone(p.Data[off:end])
				summaryRows[j] = -1
				if s != nil {
					for z, e := range s.Exponents {
						if e%65535 == p.Exponent%65535 {
							summaryRows[j] = z
							break
						}
					}
				}
			}
			useSummary := s != nil
			for _, z := range summaryRows {
				if z < 0 {
					useSummary = false
				}
			}
			if useSummary {
				for j, z := range summaryRows {
					f.addMul(rhs[j], s.Data[z][off:end], 1)
				}
			}
			skip := make([]bool, len(l.Blocks))
			for b := range l.Blocks {
				skip[b] = missing[b]
				if useSummary && s.Seen[b] {
					skip[b] = true
					st.SummaryBytes += int64(n)
				}
			}
			err := parallelRanges(ctx, skip, n, func(c context.Context, b int, d []byte) error { return read(c, b, off, d) }, func(b int, buf []byte, err error) error {
				if errors.Is(err, ErrErasure) {
					missing[b] = true
					if len(missing) > len(parity) {
						return ErrInsufficient
					}
					return nil
				}
				if err != nil {
					return err
				}
				st.InputBytes += int64(n)
				if dst, ok := targets[b]; ok {
					copy(dst[off:end], buf)
				}
				for j, p := range parity {
					f.addMul(rhs[j], buf, f.Coefficient(b, p.Exponent))
				}
				return nil
			})
			if err != nil {
				return nil, st, err
			}

			unknown := make([]int, 0, len(missing))
			for b := range missing {
				unknown = append(unknown, b)
			}
			sort.Ints(unknown)
			if len(unknown) > len(parity) {
				return nil, st, ErrInsufficient
			}
			matrix := make([][]uint16, len(parity))
			for j, p := range parity {
				matrix[j] = make([]uint16, len(unknown))
				for z, b := range unknown {
					matrix[j][z] = f.Coefficient(b, p.Exponent)
				}
			}
			// Rectangular elimination chooses independent rows, including nonconsecutive
			// recovery exponents, without assuming a count alone guarantees rank.
			for col := range unknown {
				pivot := col
				for pivot < len(matrix) && matrix[pivot][col] == 0 {
					pivot++
				}
				if pivot == len(matrix) {
					return nil, st, ErrInsufficient
				}
				matrix[col], matrix[pivot] = matrix[pivot], matrix[col]
				rhs[col], rhs[pivot] = rhs[pivot], rhs[col]
				inv := f.inverse(matrix[col][col])
				for j := col; j < len(unknown); j++ {
					matrix[col][j] = f.Mul(matrix[col][j], inv)
				}
				scaled := make([]byte, n)
				f.addMul(scaled, rhs[col], inv)
				rhs[col] = scaled
				for row := range matrix {
					if row == col {
						continue
					}
					c := matrix[row][col]
					if c == 0 {
						continue
					}
					for j := col; j < len(unknown); j++ {
						matrix[row][j] ^= f.Mul(c, matrix[col][j])
					}
					f.addMul(rhs[row], rhs[col], c)
				}
			}
			for z, b := range unknown {
				// A one-pass whole-slice solve also reconstructs helper erasures.
				// Keep them (within the same target bound) to avoid downloading
				// the set again when the reader reaches the next missing article.
				if opts.WholeSlices && off == 0 && end == l.SliceSize && targets[b] == nil && len(targets) < opts.MaxTargets {
					targets[b] = make([]byte, l.SliceSize)
				}
				if dst, ok := targets[b]; ok {
					copy(dst[off:end], rhs[z])
					st.RecoveredBytes += int64(n)
				}
			}
			off = end
		}
	}
	for b, d := range targets {
		if !l.Blocks[b].Verify(d) {
			return nil, st, fmt.Errorf("par2: reconstructed slice %d checksum mismatch", b)
		}
	}
	return targets, st, nil
}
func uniqueOffsets(a []int64) []int64 {
	n := 0
	for _, v := range a {
		if n == 0 || a[n-1] != v {
			a[n] = v
			n++
		}
	}
	return a[:n]
}

// A small bounded pipeline overlaps NNTP dependency reads. It joins every
// worker on errors/cancellation; only the caller mutates equations and output.
func parallelRanges(parent context.Context, skip []bool, n int, read func(context.Context, int, []byte) error, visit func(int, []byte, error) error) error {
	ctx, cancel := context.WithCancel(parent)
	type result struct {
		index int
		data  []byte
		err   error
	}
	jobs := make(chan int)
	// Keep several NNTP articles in flight even when each contains many
	// small PAR2 slices. Four slice workers can otherwise coalesce onto one
	// article and accidentally serialize the provider connection pool.
	workers := min(64, max(4, (16<<20)/max(1, n)))
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				d := make([]byte, n)
				err := read(ctx, b, d)
				select {
				case results <- result{b, d, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		for b, no := range skip {
			if no {
				continue
			}
			select {
			case jobs <- b:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(results); close(done) }()
	defer func() { cancel(); <-done }()
	for r := range results {
		if err := visit(r.index, r.data, r.err); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// RecoveryScanner accepts consecutive decoded article bytes and releases valid
// recovery packets incrementally. Reset at every unavailable/nonconsecutive
// article; complete packets on either side of a hole remain usable.
type RecoveryScanner struct {
	ID        ID
	SliceSize int64
	buffer    []byte
}

func (s *RecoveryScanner) Reset() { s.buffer = nil }
func (s *RecoveryScanner) Push(data []byte, limit int) []Recovery {
	s.buffer = append(s.buffer, data...)
	var out []Recovery
	for len(s.buffer) > 0 && len(out) < limit {
		at := bytes.Index(s.buffer, []byte(Magic))
		if at < 0 {
			if len(s.buffer) > 7 {
				s.buffer = append([]byte(nil), s.buffer[len(s.buffer)-7:]...)
			}
			break
		}
		s.buffer = s.buffer[at:]
		if len(s.buffer) < HeaderSize {
			break
		}
		n := binary.LittleEndian.Uint64(s.buffer[8:16])
		if n < HeaderSize || n%4 != 0 || n > uint64(max(s.SliceSize+68, 4<<20)) {
			s.buffer = s.buffer[1:]
			continue
		}
		if n > uint64(len(s.buffer)) {
			break
		}
		if _, ok := packetAt(s.buffer, 0); !ok {
			s.buffer = s.buffer[1:]
			continue
		}
		out = append(out, RecoveryPackets(s.buffer[:n], s.ID, s.SliceSize, limit-len(out))...)
		s.buffer = s.buffer[n:]
	}
	return out
}
