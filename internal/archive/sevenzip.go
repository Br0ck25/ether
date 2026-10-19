package archive

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"unicode/utf16"

	"github.com/ulikunitz/xz/lzma"
)

// 7z property IDs (7zFormat.txt).
const (
	sz7End                   = 0x00
	sz7Header                = 0x01
	sz7ArchiveProperties     = 0x02
	sz7AdditionalStreamsInfo = 0x03
	sz7MainStreamsInfo       = 0x04
	sz7FilesInfo             = 0x05
	sz7PackInfo              = 0x06
	sz7UnpackInfo            = 0x07
	sz7SubStreamsInfo        = 0x08
	sz7Size                  = 0x09
	sz7CRC                   = 0x0a
	sz7Folder                = 0x0b
	sz7CodersUnpackSize      = 0x0c
	sz7NumUnpackStream       = 0x0d
	sz7EmptyStream           = 0x0e
	sz7EmptyFile             = 0x0f
	sz7Anti                  = 0x10
	sz7Name                  = 0x11
	sz7WinAttributes         = 0x15
	sz7EncodedHeader         = 0x17
)

const (
	sz7SigHeaderLen = 32
	// sz7MaxHeader caps the (packed or unpacked) header size we will load.
	sz7MaxHeader = 64 << 20
	// sz7MaxCoders caps coders and streams per folder.
	sz7MaxCoders = 64
	// sz7MaxNesting caps how many encoded headers may wrap each other.
	sz7MaxNesting = 4

	attrDirectory = 0x10
)

// 7z method IDs used for decisions.
var (
	sz7MethodCopy  = []byte{0x00}
	sz7MethodLZMA  = []byte{0x03, 0x01, 0x01}
	sz7MethodLZMA2 = []byte{0x21}
	sz7MethodAES   = []byte{0x06, 0xf1, 0x07, 0x01}
)

var sz7MethodNames = map[string]string{
	"00":       "copy",
	"03":       "delta",
	"04":       "bcj",
	"05":       "ppc",
	"06":       "ia64",
	"07":       "arm",
	"08":       "armt",
	"09":       "sparc",
	"0a":       "arm64",
	"0b":       "riscv",
	"21":       "lzma2",
	"030101":   "lzma",
	"03030103": "bcj",
	"0303011b": "bcj2",
	"03030205": "ppc",
	"03030401": "ia64",
	"03030501": "arm",
	"03030701": "armt",
	"03030805": "sparc",
	"030401":   "ppmd",
	"040108":   "deflate",
	"040109":   "deflate64",
	"040202":   "bzip2",
	"06f10701": "aes",
}

type sz7Coder struct {
	id     []byte
	numIn  int
	numOut int
	props  []byte
}

func (c *sz7Coder) name() string {
	h := hex.EncodeToString(c.id)
	if n, ok := sz7MethodNames[h]; ok {
		return n
	}
	return "id:" + h
}

type sz7Fold struct {
	coders      []sz7Coder
	bindOut     []uint64 // out-stream index of each bind pair
	numPacked   int
	unpackSizes []uint64
	crc         uint32
	hasCRC      bool
}

// unpackSize returns the size of the folder's final output stream: the one
// not consumed by a bind pair.
func (f *sz7Fold) unpackSize() uint64 {
outer:
	for i := len(f.unpackSizes) - 1; i >= 0; i-- {
		for _, o := range f.bindOut {
			if o == uint64(i) {
				continue outer
			}
		}
		return f.unpackSizes[i]
	}
	return 0
}

func (f *sz7Fold) hasMethod(id []byte) bool {
	for _, c := range f.coders {
		if bytes.Equal(c.id, id) {
			return true
		}
	}
	return false
}

func (f *sz7Fold) stored() bool {
	return len(f.coders) == 1 && bytes.Equal(f.coders[0].id, sz7MethodCopy) &&
		f.coders[0].numIn == 1 && f.coders[0].numOut == 1
}

func (f *sz7Fold) method() string {
	names := make([]string, len(f.coders))
	for i := range f.coders {
		names[i] = f.coders[i].name()
	}
	return strings.Join(names, "+")
}

type sz7Streams struct {
	packPos   uint64
	packSizes []uint64
	folders   []sz7Fold
	subSizes  [][]uint64 // per folder; nil until SubStreamsInfo is read
}

// sz7Buf is a cursor over 7z header bytes with a sticky error.
type sz7Buf struct {
	b   []byte
	err error
}

func (s *sz7Buf) fail(format string, a ...any) {
	if s.err == nil {
		s.err = fmt.Errorf("archive: 7z header: "+format+": %w", append(a, ErrCorrupt)...)
	}
}

func (s *sz7Buf) byte() byte {
	if s.err != nil {
		return 0
	}
	if len(s.b) == 0 {
		s.fail("truncated")
		return 0
	}
	c := s.b[0]
	s.b = s.b[1:]
	return c
}

// number reads a 7z NUMBER (length given by the leading one bits).
func (s *sz7Buf) number() uint64 {
	first := s.byte()
	var v uint64
	mask := byte(0x80)
	for i := 0; i < 8; i++ {
		if first&mask == 0 {
			return v | uint64(first&(mask-1))<<(8*i)
		}
		v |= uint64(s.byte()) << (8 * i)
		mask >>= 1
	}
	return v
}

// count reads a NUMBER used as an element count; it must not exceed limit
// nor the remaining header bytes times perByte (a cheap sanity bound that
// keeps corrupt input from forcing huge allocations).
func (s *sz7Buf) count(limit uint64, what string) int {
	v := s.number()
	if s.err != nil {
		return 0
	}
	if v > limit || v > uint64(len(s.b))+1 {
		s.fail("%s count %d out of range", what, v)
		return 0
	}
	return int(v)
}

func (s *sz7Buf) u32() uint32 {
	b := s.bytes(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (s *sz7Buf) bytes(n uint64) []byte {
	if s.err != nil {
		return nil
	}
	if n > uint64(len(s.b)) {
		s.fail("field of %d bytes overflows header", n)
		return nil
	}
	v := s.b[:n:n]
	s.b = s.b[n:]
	return v
}

// bits reads a BitField of n items (most significant bit first).
func (s *sz7Buf) bits(n int) []bool {
	raw := s.bytes(uint64((n + 7) / 8))
	if raw == nil && n > 0 {
		return nil
	}
	out := make([]bool, n)
	for i := range out {
		out[i] = raw[i/8]&(0x80>>(i%8)) != 0
	}
	return out
}

// defined reads an AllAreDefined byte and, if zero, the BitField that
// follows.
func (s *sz7Buf) defined(n int) []bool {
	if s.byte() != 0 {
		out := make([]bool, n)
		for i := range out {
			out[i] = true
		}
		return out
	}
	return s.bits(n)
}

// digests reads Digests for n items; it returns the CRCs and which are
// defined.
func (s *sz7Buf) digests(n int) ([]uint32, []bool) {
	def := s.defined(n)
	if s.err != nil {
		return nil, nil
	}
	crcs := make([]uint32, n)
	for i := range crcs {
		if def[i] {
			crcs[i] = s.u32()
		}
	}
	return crcs, def
}

// skipData skips a property payload prefixed by its size.
func (s *sz7Buf) skipData() {
	s.bytes(s.number())
}

func (s *sz7Buf) packInfo(st *sz7Streams) {
	st.packPos = s.number()
	n := s.count(1<<20, "pack stream")
	st.packSizes = make([]uint64, n)
	for {
		switch id := s.byte(); id {
		case sz7End:
			return
		case sz7Size:
			for i := range st.packSizes {
				st.packSizes[i] = s.number()
			}
		case sz7CRC:
			s.digests(n)
		default:
			s.fail("unexpected property %#x in PackInfo", id)
			return
		}
	}
}

func (s *sz7Buf) folder() sz7Fold {
	var f sz7Fold
	numCoders := s.count(sz7MaxCoders, "coder")
	if s.err == nil && numCoders == 0 {
		s.fail("folder without coders")
	}
	totalIn, totalOut := 0, 0
	for i := 0; i < numCoders && s.err == nil; i++ {
		flag := s.byte()
		if flag&0x80 != 0 {
			s.err = fmt.Errorf("archive: 7z alternative coder methods: %w", ErrUnsupported)
			break
		}
		c := sz7Coder{id: s.bytes(uint64(flag & 0x0f)), numIn: 1, numOut: 1}
		if flag&0x10 != 0 {
			c.numIn = s.count(sz7MaxCoders, "coder in-stream")
			c.numOut = s.count(sz7MaxCoders, "coder out-stream")
		}
		if flag&0x20 != 0 {
			c.props = s.bytes(s.number())
		}
		totalIn += c.numIn
		totalOut += c.numOut
		f.coders = append(f.coders, c)
	}
	if s.err != nil {
		return f
	}
	if totalOut == 0 || totalIn > sz7MaxCoders || totalOut > sz7MaxCoders {
		s.fail("folder with %d in and %d out streams", totalIn, totalOut)
		return f
	}
	numBind := totalOut - 1
	for i := 0; i < numBind; i++ {
		in, out := s.number(), s.number()
		if in >= uint64(totalIn) || out >= uint64(totalOut) {
			s.fail("bind pair out of range")
			return f
		}
		f.bindOut = append(f.bindOut, out)
	}
	f.numPacked = totalIn - numBind
	if f.numPacked < 1 {
		s.fail("folder without packed streams")
		return f
	}
	if f.numPacked > 1 {
		for i := 0; i < f.numPacked; i++ {
			if s.number() >= uint64(totalIn) {
				s.fail("packed stream index out of range")
				return f
			}
		}
	}
	f.unpackSizes = make([]uint64, totalOut)
	return f
}

func (s *sz7Buf) unpackInfo(st *sz7Streams) {
	if id := s.byte(); id != sz7Folder {
		s.fail("expected Folder, got %#x", id)
		return
	}
	n := s.count(1<<20, "folder")
	if s.byte() != 0 {
		if s.err == nil {
			s.err = fmt.Errorf("archive: 7z external folders: %w", ErrUnsupported)
		}
		return
	}
	st.folders = make([]sz7Fold, 0, n)
	for i := 0; i < n && s.err == nil; i++ {
		st.folders = append(st.folders, s.folder())
	}
	if id := s.byte(); id != sz7CodersUnpackSize {
		s.fail("expected CodersUnpackSize, got %#x", id)
		return
	}
	for i := range st.folders {
		for j := range st.folders[i].unpackSizes {
			st.folders[i].unpackSizes[j] = s.number()
		}
	}
	for s.err == nil {
		switch id := s.byte(); id {
		case sz7End:
			return
		case sz7CRC:
			crcs, def := s.digests(len(st.folders))
			for i := range crcs {
				st.folders[i].crc, st.folders[i].hasCRC = crcs[i], def[i]
			}
		default:
			s.fail("unexpected property %#x in UnpackInfo", id)
		}
	}
}

func (s *sz7Buf) subStreamsInfo(st *sz7Streams) {
	nf := len(st.folders)
	numSub := make([]int, nf)
	for i := range numSub {
		numSub[i] = 1
	}
	var id byte
	for s.err == nil {
		id = s.byte()
		if id == sz7NumUnpackStream {
			for i := range numSub {
				numSub[i] = s.count(1<<24, "substream")
			}
			continue
		}
		if id == sz7CRC || id == sz7Size || id == sz7End {
			break
		}
		s.skipData()
	}
	st.subSizes = make([][]uint64, nf)
	for i := 0; i < nf && s.err == nil; i++ {
		if numSub[i] == 0 {
			continue
		}
		if numSub[i] > 1 && id != sz7Size {
			s.fail("folder %d has %d substreams but no sizes", i, numSub[i])
			return
		}
		if uint64(numSub[i]-1) > uint64(len(s.b)) {
			s.fail("substream count %d out of range", numSub[i])
			return
		}
		sizes := make([]uint64, 0, numSub[i])
		var sum uint64
		for j := 1; j < numSub[i]; j++ {
			v := s.number()
			sum += v
			if sum < v {
				s.fail("substream sizes overflow")
				return
			}
			sizes = append(sizes, v)
		}
		total := st.folders[i].unpackSize()
		if sum > total {
			s.fail("substreams exceed folder %d size", i)
			return
		}
		st.subSizes[i] = append(sizes, total-sum)
	}
	if id == sz7Size {
		id = s.byte()
	}
	for s.err == nil {
		switch id {
		case sz7End:
			return
		case sz7CRC:
			n := 0
			for i, f := range st.folders {
				if !(numSub[i] == 1 && f.hasCRC) {
					n += numSub[i]
				}
			}
			if uint64(n) > uint64(len(s.b))*8+8 {
				s.fail("digest count %d out of range", n)
				return
			}
			s.digests(n)
		default:
			s.skipData()
		}
		id = s.byte()
	}
}

func (s *sz7Buf) streamsInfo() *sz7Streams {
	st := &sz7Streams{}
	for s.err == nil {
		switch id := s.byte(); id {
		case sz7End:
			if st.subSizes == nil {
				st.subSizes = make([][]uint64, len(st.folders))
				for i := range st.folders {
					st.subSizes[i] = []uint64{st.folders[i].unpackSize()}
				}
			}
			return st
		case sz7PackInfo:
			s.packInfo(st)
		case sz7UnpackInfo:
			s.unpackInfo(st)
		case sz7SubStreamsInfo:
			s.subStreamsInfo(st)
		default:
			s.fail("unexpected property %#x in StreamsInfo", id)
		}
	}
	return st
}

type sz7File struct {
	name        string
	emptyStream bool
	emptyFile   bool
	anti        bool
	attr        uint32
	hasAttr     bool
}

func (s *sz7Buf) filesInfo() []sz7File {
	n := s.count(1<<20, "file")
	if s.err != nil {
		return nil
	}
	files := make([]sz7File, n)
	numEmpty := 0
	for s.err == nil {
		t := s.byte()
		if t == sz7End || s.err != nil {
			break
		}
		p := sz7Buf{b: s.bytes(s.number())}
		if s.err != nil {
			break
		}
		switch t {
		case sz7EmptyStream:
			bits := p.bits(n)
			numEmpty = 0
			for i, b := range bits {
				files[i].emptyStream = b
				if b {
					numEmpty++
				}
			}
		case sz7EmptyFile, sz7Anti:
			bits := p.bits(numEmpty)
			j := 0
			for i := range files {
				if files[i].emptyStream && j < len(bits) {
					if t == sz7EmptyFile {
						files[i].emptyFile = bits[j]
					} else {
						files[i].anti = bits[j]
					}
					j++
				}
			}
		case sz7Name:
			if p.byte() != 0 {
				s.err = fmt.Errorf("archive: 7z external file names: %w", ErrUnsupported)
				return nil
			}
			raw := p.b
			if len(raw)%2 != 0 {
				p.fail("odd-length names")
				break
			}
			i := 0
			u := make([]uint16, 0, 64)
			for k := 0; k+1 < len(raw) && i < n; k += 2 {
				c := binary.LittleEndian.Uint16(raw[k:])
				if c == 0 {
					files[i].name = string(utf16.Decode(u))
					u = u[:0]
					i++
					continue
				}
				u = append(u, c)
			}
			if i != n {
				p.fail("%d names for %d files", i, n)
			}
		case sz7WinAttributes:
			def := p.defined(n)
			if p.byte() != 0 {
				s.err = fmt.Errorf("archive: 7z external attributes: %w", ErrUnsupported)
				return nil
			}
			for i := range files {
				if i < len(def) && def[i] {
					files[i].attr = p.u32()
					files[i].hasAttr = true
				}
			}
		default:
			// Times, start positions, padding, unknown: not needed.
		}
		if p.err != nil {
			s.err = p.err
		}
	}
	return files
}

// List7z parses a 7z archive. r is the full archive (for a multi-volume
// .7z.001/.002… set pass the concatenation, e.g. a MultiReader; SplitExtents
// maps the result back onto the individual volumes). Extents use Volume 0
// and offsets in r. Only stored (Copy-only) members have extents.
func List7z(r io.ReaderAt, size int64) ([]Member, error) {
	var sh [sz7SigHeaderLen]byte
	if size < int64(len(sig7z)) {
		return nil, fmt.Errorf("archive: 7z: %w", ErrNotArchive)
	}
	if size < sz7SigHeaderLen {
		if err := readAt(r, sh[:len(sig7z)], 0, size); err != nil {
			return nil, err
		}
		if Detect(sh[:]) != Format7z {
			return nil, fmt.Errorf("archive: 7z: %w", ErrNotArchive)
		}
		return nil, fmt.Errorf("archive: 7z signature header truncated: %w", ErrCorrupt)
	}
	if err := readAt(r, sh[:], 0, size); err != nil {
		return nil, err
	}
	if Detect(sh[:]) != Format7z {
		return nil, fmt.Errorf("archive: 7z: %w", ErrNotArchive)
	}
	if sh[6] != 0 {
		return nil, fmt.Errorf("archive: 7z major version %d: %w", sh[6], ErrUnsupported)
	}
	if got, want := crc32.ChecksumIEEE(sh[12:]), binary.LittleEndian.Uint32(sh[8:]); got != want {
		return nil, fmt.Errorf("archive: 7z start header crc %#08x, want %#08x: %w", got, want, ErrCorrupt)
	}
	nextOff := binary.LittleEndian.Uint64(sh[12:])
	nextSize := binary.LittleEndian.Uint64(sh[20:])
	nextCRC := binary.LittleEndian.Uint32(sh[28:])
	if nextSize == 0 {
		return nil, nil // empty archive
	}
	avail := uint64(size - sz7SigHeaderLen)
	if nextSize > sz7MaxHeader || nextOff > avail || nextSize > avail-nextOff {
		return nil, fmt.Errorf("archive: 7z header (%d bytes at %d) outside archive of %d bytes: %w",
			nextSize, nextOff, size, ErrCorrupt)
	}
	hdr := make([]byte, nextSize)
	if err := readAt(r, hdr, int64(sz7SigHeaderLen+nextOff), size); err != nil {
		return nil, err
	}
	if got := crc32.ChecksumIEEE(hdr); got != nextCRC {
		return nil, fmt.Errorf("archive: 7z header crc %#08x, want %#08x: %w", got, nextCRC, ErrCorrupt)
	}
	for depth := 0; ; depth++ {
		if len(hdr) == 0 {
			return nil, fmt.Errorf("archive: 7z empty header: %w", ErrCorrupt)
		}
		switch hdr[0] {
		case sz7Header:
			return parse7zHeader(hdr[1:], size)
		case sz7EncodedHeader:
			if depth >= sz7MaxNesting {
				return nil, fmt.Errorf("archive: 7z encoded headers nested too deep: %w", ErrCorrupt)
			}
			b := sz7Buf{b: hdr[1:]}
			st := b.streamsInfo()
			if b.err != nil {
				return nil, b.err
			}
			var err error
			if hdr, err = decode7zHeader(r, size, st); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("archive: 7z header starts with %#x: %w", hdr[0], ErrCorrupt)
		}
	}
}

// decode7zHeader unpacks an encoded header described by st.
func decode7zHeader(r io.ReaderAt, size int64, st *sz7Streams) ([]byte, error) {
	if len(st.folders) != 1 || len(st.packSizes) < 1 {
		return nil, fmt.Errorf("archive: 7z encoded header with %d folders: %w", len(st.folders), ErrCorrupt)
	}
	f := &st.folders[0]
	if f.hasMethod(sz7MethodAES) {
		return nil, fmt.Errorf("archive: 7z header is encrypted: %w", ErrEncrypted)
	}
	if len(f.coders) != 1 || f.numPacked != 1 {
		return nil, fmt.Errorf("archive: 7z header coder chain %s: %w", f.method(), ErrUnsupported)
	}
	unpack := f.unpackSize()
	packSize := st.packSizes[0]
	avail := uint64(size - sz7SigHeaderLen)
	if unpack > sz7MaxHeader || packSize > sz7MaxHeader || st.packPos > avail || packSize > avail-st.packPos {
		return nil, fmt.Errorf("archive: 7z encoded header (%d packed, %d unpacked) out of range: %w", packSize, unpack, ErrCorrupt)
	}
	packed := make([]byte, packSize)
	if err := readAt(r, packed, int64(sz7SigHeaderLen+st.packPos), size); err != nil {
		return nil, err
	}
	c := &f.coders[0]
	var out []byte
	switch {
	case bytes.Equal(c.id, sz7MethodCopy):
		if packSize != unpack {
			return nil, fmt.Errorf("archive: 7z copy header size mismatch: %w", ErrCorrupt)
		}
		out = packed
	case bytes.Equal(c.id, sz7MethodLZMA):
		if len(c.props) != 5 {
			return nil, fmt.Errorf("archive: 7z lzma properties: %w", ErrCorrupt)
		}
		h := make([]byte, lzma.HeaderLen)
		copy(h, c.props)
		binary.LittleEndian.PutUint64(h[5:], unpack)
		zr, err := lzma.NewReader(io.MultiReader(bytes.NewReader(h), bytes.NewReader(packed)))
		if err != nil {
			return nil, fmt.Errorf("archive: 7z lzma header: %v: %w", err, ErrCorrupt)
		}
		out = make([]byte, unpack)
		if _, err := io.ReadFull(zr, out); err != nil {
			return nil, fmt.Errorf("archive: 7z lzma header: %v: %w", err, ErrCorrupt)
		}
	case bytes.Equal(c.id, sz7MethodLZMA2):
		if len(c.props) != 1 || c.props[0] > 40 {
			return nil, fmt.Errorf("archive: 7z lzma2 properties: %w", ErrCorrupt)
		}
		dict := uint64(lzma.MinDictCap)
		if unpack > dict {
			dict = unpack
		}
		zr, err := lzma.Reader2Config{DictCap: int(dict)}.NewReader2(bytes.NewReader(packed))
		if err != nil {
			return nil, fmt.Errorf("archive: 7z lzma2 header: %v: %w", err, ErrCorrupt)
		}
		out = make([]byte, unpack)
		if _, err := io.ReadFull(zr, out); err != nil {
			return nil, fmt.Errorf("archive: 7z lzma2 header: %v: %w", err, ErrCorrupt)
		}
	default:
		return nil, fmt.Errorf("archive: 7z header coder %s: %w", c.name(), ErrUnsupported)
	}
	if f.hasCRC && crc32.ChecksumIEEE(out) != f.crc {
		return nil, fmt.Errorf("archive: 7z decoded header crc mismatch: %w", ErrCorrupt)
	}
	return out, nil
}

// parse7zHeader parses a plain Header (after its kHeader byte) into members.
func parse7zHeader(hdr []byte, size int64) ([]Member, error) {
	b := sz7Buf{b: hdr}
	id := b.byte()
	if id == sz7ArchiveProperties {
		for b.err == nil {
			if b.byte() == 0 {
				break
			}
			b.skipData()
		}
		id = b.byte()
	}
	if id == sz7AdditionalStreamsInfo {
		b.streamsInfo()
		id = b.byte()
	}
	st := &sz7Streams{}
	if id == sz7MainStreamsInfo {
		st = b.streamsInfo()
		id = b.byte()
	}
	var files []sz7File
	if id == sz7FilesInfo {
		files = b.filesInfo()
		id = b.byte()
	}
	if b.err != nil {
		return nil, b.err
	}
	if id != sz7End {
		return nil, fmt.Errorf("archive: 7z header: unexpected property %#x: %w", id, ErrCorrupt)
	}

	// Where each folder's first pack stream starts.
	packStart := make([]uint64, len(st.folders))
	firstPack := make([]int, len(st.folders))
	pi := 0
	pos := uint64(sz7SigHeaderLen) + st.packPos
	if pos < st.packPos {
		return nil, fmt.Errorf("archive: 7z pack position overflows: %w", ErrCorrupt)
	}
	for i := range st.folders {
		packStart[i] = pos
		firstPack[i] = pi
		for j := 0; j < st.folders[i].numPacked; j++ {
			if pi >= len(st.packSizes) {
				return nil, fmt.Errorf("archive: 7z folder %d has no pack stream: %w", i, ErrCorrupt)
			}
			pos += st.packSizes[pi]
			if pos < st.packSizes[pi] {
				return nil, fmt.Errorf("archive: 7z pack sizes overflow: %w", ErrCorrupt)
			}
			pi++
		}
	}

	members := make([]Member, 0, len(files))
	fi, si := 0, 0 // folder, substream within folder
	var inFolder uint64
	for _, f := range files {
		if f.emptyStream {
			if f.anti {
				continue
			}
			dir := !f.emptyFile || (f.hasAttr && f.attr&attrDirectory != 0)
			m := Member{Name: f.name, Stored: true, Dir: dir, Method: "empty"}
			if dir {
				m.Method = "dir"
			}
			members = append(members, m)
			continue
		}
		for fi < len(st.folders) && si >= len(st.subSizes[fi]) {
			fi, si, inFolder = fi+1, 0, 0
		}
		if fi >= len(st.folders) {
			return nil, fmt.Errorf("archive: 7z file %q has no stream: %w", f.name, ErrCorrupt)
		}
		fo := &st.folders[fi]
		sz := st.subSizes[fi][si]
		if sz > 1<<62 {
			return nil, fmt.Errorf("archive: 7z file %q size out of range: %w", f.name, ErrCorrupt)
		}
		m := Member{
			Name:      f.name,
			Size:      int64(sz),
			Stored:    fo.stored(),
			Encrypted: fo.hasMethod(sz7MethodAES),
			Dir:       f.hasAttr && f.attr&attrDirectory != 0,
			Method:    fo.method(),
		}
		if m.Stored {
			if fo.numPacked != 1 || st.packSizes[firstPack[fi]] != fo.unpackSize() {
				return nil, fmt.Errorf("archive: 7z copy folder %d size mismatch: %w", fi, ErrCorrupt)
			}
			off := packStart[fi] + inFolder
			if off < packStart[fi] || off > uint64(size) || sz > uint64(size)-off {
				return nil, fmt.Errorf("archive: 7z file %q data outside archive: %w", f.name, ErrCorrupt)
			}
			if sz > 0 {
				m.Extents = []Extent{{Volume: 0, Offset: int64(off), Size: int64(sz)}}
			}
		}
		members = append(members, m)
		inFolder += sz
		si++
	}
	return members, nil
}
