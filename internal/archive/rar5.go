package archive

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// RAR 5.0 header types and flags.
const (
	rar5Main       = 1
	rar5File       = 2
	rar5Service    = 3
	rar5Encryption = 4
	rar5End        = 5

	rar5HdrExtra       = 0x0001
	rar5HdrData        = 0x0002
	rar5HdrSplitBefore = 0x0008
	rar5HdrSplitAfter  = 0x0010

	rar5ArcVolume    = 0x0001
	rar5ArcVolNumber = 0x0002
	rar5ArcSolid     = 0x0004

	rar5FileDir         = 0x0001
	rar5FileMtime       = 0x0002
	rar5FileCRC         = 0x0004
	rar5FileUnknownSize = 0x0008

	rar5CompSolid = 0x0040

	rar5ExtraEncryption = 0x01

	rar5EndNotLast = 0x0001

	// rar5MaxHeader is the format's limit on a header's size.
	rar5MaxHeader = 2 << 20
	// rar5Prefix is how much of a block is read first: CRC32 plus enough
	// for the HeaderSize vint (3 bytes cover rar5MaxHeader) and one more.
	rar5Prefix = 8
	// rar5MinBlock is the smallest possible block: CRC32, HeaderSize,
	// HeaderType and HeaderFlags.
	rar5MinBlock = 7
)

// uvarint decodes a RAR5 vint (little-endian base-128) from b, returning the
// value and the number of bytes used (0 if b ends first or it overflows).
func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		if i == 9 && c > 1 {
			return 0, 0
		}
		v |= uint64(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// rar5Buf is a cursor over header bytes with a sticky error.
type rar5Buf struct {
	b   []byte
	err error
}

func (p *rar5Buf) vint() uint64 {
	if p.err != nil {
		return 0
	}
	v, n := uvarint(p.b)
	if n == 0 {
		p.err = fmt.Errorf("bad vint: %w", ErrCorrupt)
		return 0
	}
	p.b = p.b[n:]
	return v
}

// size reads a vint that must fit an int64.
func (p *rar5Buf) size() int64 {
	v := p.vint()
	if v > 1<<62 {
		if p.err == nil {
			p.err = fmt.Errorf("size %d out of range: %w", v, ErrCorrupt)
		}
		return 0
	}
	return int64(v)
}

func (p *rar5Buf) u32() uint32 {
	if p.err != nil {
		return 0
	}
	if len(p.b) < 4 {
		p.err = fmt.Errorf("header truncated: %w", ErrCorrupt)
		return 0
	}
	v := binary.LittleEndian.Uint32(p.b)
	p.b = p.b[4:]
	return v
}

func (p *rar5Buf) bytes(n uint64) []byte {
	if p.err != nil {
		return nil
	}
	if n > uint64(len(p.b)) {
		p.err = fmt.Errorf("field of %d bytes overflows header: %w", n, ErrCorrupt)
		return nil
	}
	v := p.b[:n]
	p.b = p.b[n:]
	return v
}

func readRAR5(r io.ReaderAt, size int64, o RAROptions) (*RARVolume, error) {
	v := &RARVolume{Format: FormatRAR5}
	pos := int64(len(sigRAR5))
	var prefix [rar5Prefix]byte
	mainSeen := false
	for pos < size {
		if size-pos < rar5MinBlock {
			return nil, fmt.Errorf("archive: rar5 block at %d: %d trailing bytes: %w", pos, size-pos, ErrCorrupt)
		}
		pre := prefix[:min(int64(rar5Prefix), size-pos)]
		if err := readAt(r, pre, pos, size); err != nil {
			return nil, fmt.Errorf("archive: rar5 block at %d: %w", pos, err)
		}
		hsize, n := uvarint(pre[4:])
		if n == 0 || hsize == 0 || hsize > rar5MaxHeader {
			return nil, fmt.Errorf("archive: rar5 block at %d: bad header size: %w", pos, ErrCorrupt)
		}
		total := 4 + n + int(hsize)
		if int64(total) > size-pos {
			return nil, fmt.Errorf("archive: rar5 block at %d: header (%d bytes) runs past end of volume: %w", pos, total, ErrCorrupt)
		}
		h := make([]byte, total)
		got := copy(h, pre)
		if got < total {
			if err := readAt(r, h[got:], pos+int64(got), size); err != nil {
				return nil, fmt.Errorf("archive: rar5 block at %d: %w", pos, err)
			}
		}
		if c, want := crc32.ChecksumIEEE(h[4:]), binary.LittleEndian.Uint32(h); c != want {
			return nil, fmt.Errorf("archive: rar5 block at %d: header crc %#08x, want %#08x: %w", pos, c, want, ErrCorrupt)
		}
		body := h[4+n:]
		b := rar5Buf{b: body}
		typ := b.vint()
		flags := b.vint()
		var extraSize, dataSize int64
		if flags&rar5HdrExtra != 0 {
			extraSize = b.size()
		}
		if flags&rar5HdrData != 0 {
			dataSize = b.size()
		}
		if b.err != nil {
			return nil, fmt.Errorf("archive: rar5 block at %d: %w", pos, b.err)
		}
		if extraSize > int64(len(b.b)) {
			return nil, fmt.Errorf("archive: rar5 block at %d: extra area overflows header: %w", pos, ErrCorrupt)
		}
		fields := rar5Buf{b: b.b[:int64(len(b.b))-extraSize]}
		extra := b.b[int64(len(b.b))-extraSize:]
		dataOff := pos + int64(total)
		if dataSize > size-dataOff {
			return nil, fmt.Errorf("archive: rar5 block at %d: data (%d bytes) runs past end of volume: %w", pos, dataSize, ErrCorrupt)
		}
		next := dataOff + dataSize

		if !mainSeen && typ != rar5Main && typ != rar5Encryption {
			return nil, fmt.Errorf("archive: rar5 block at %d: type %d before main header: %w", pos, typ, ErrCorrupt)
		}
		switch typ {
		case rar5Encryption:
			return nil, fmt.Errorf("archive: rar5 headers are encrypted: %w", ErrEncrypted)
		case rar5Main:
			if mainSeen {
				break
			}
			mainSeen = true
			af := fields.vint()
			var num uint64
			if af&rar5ArcVolNumber != 0 {
				num = fields.vint()
			}
			if fields.err != nil {
				return nil, fmt.Errorf("archive: rar5 main header: %w", fields.err)
			}
			if num > 1<<20 {
				return nil, fmt.Errorf("archive: rar5 volume number %d: %w", num, ErrCorrupt)
			}
			v.MultiVolume = af&rar5ArcVolume != 0
			v.First = af&rar5ArcVolNumber == 0
			v.Number = int(num)
			v.Solid = af&rar5ArcSolid != 0
			v.DataStart = next
		case rar5File:
			f, err := parseRAR5File(&fields, extra, flags)
			if err != nil {
				return nil, fmt.Errorf("archive: rar5 file header at %d: %w", pos, err)
			}
			f.HeaderOffset = pos
			f.DataOffset = dataOff
			f.PackedSize = dataSize
			v.Files = append(v.Files, f)
			if f.SplitAfter || (o.MaxFiles > 0 && len(v.Files) >= o.MaxFiles) {
				return v, nil
			}
		case rar5End:
			ef := fields.vint()
			if fields.err != nil {
				return nil, fmt.Errorf("archive: rar5 end header: %w", fields.err)
			}
			v.EndFound = true
			v.MoreVolumes = ef&rar5EndNotLast != 0
			return v, nil
		default:
			// Service headers (comments, quick open, recovery record…)
			// and unknown types are skipped with their data.
		}
		pos = next
	}
	if !mainSeen {
		return nil, fmt.Errorf("archive: rar5 main header missing: %w", ErrCorrupt)
	}
	return v, nil
}

func parseRAR5File(p *rar5Buf, extra []byte, hflags uint64) (RARFile, error) {
	var f RARFile
	ff := p.vint()
	unp := p.size()
	p.vint() // attributes
	if ff&rar5FileMtime != 0 {
		p.u32()
	}
	if ff&rar5FileCRC != 0 {
		f.CRC32 = p.u32()
		f.HasCRC = true
	}
	ci := p.vint()
	p.vint() // host OS
	nameLen := p.vint()
	name := p.bytes(nameLen)
	if p.err != nil {
		return f, p.err
	}
	f.Name = string(name)
	f.UnpackedSize = unp
	if ff&rar5FileUnknownSize != 0 {
		f.UnpackedSize = -1
	}
	f.Method = int(ci >> 7 & 0x7)
	f.Stored = f.Method == 0
	f.Solid = ci&rar5CompSolid != 0
	f.Dir = ff&rar5FileDir != 0
	f.SplitBefore = hflags&rar5HdrSplitBefore != 0
	f.SplitAfter = hflags&rar5HdrSplitAfter != 0
	x := rar5Buf{b: extra}
	for len(x.b) > 0 {
		recSize := x.vint()
		rec := x.bytes(recSize)
		if x.err != nil {
			return f, fmt.Errorf("extra area: %w", x.err)
		}
		rb := rar5Buf{b: rec}
		if t := rb.vint(); rb.err == nil && t == rar5ExtraEncryption {
			f.Encrypted = true
		}
	}
	return f, nil
}
