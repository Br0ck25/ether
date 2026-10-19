// Package archivetest builds small RAR 4.x / RAR 5.0 archives (and Copy-mode
// 7z archives) for tests. It is for tests only: production code must not
// import it.
//
// The archives are structurally valid with correct header CRCs. File data is
// written verbatim: Method, Encrypted and Solid only set the corresponding
// header flags (nothing is compressed or encrypted), which is all a header
// parser can observe.
package archivetest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"unicode/utf16"
)

// Signatures.
var (
	RAR4Signature = []byte("Rar!\x1a\x07\x00")
	RAR5Signature = []byte("Rar!\x1a\x07\x01\x00")
)

// RAR 4.x constants.
const (
	rar4Main   = 0x73
	rar4File   = 0x74
	rar4NewSub = 0x7a
	rar4End    = 0x7b

	rar4LongBlock = 0x8000

	// RAR 4.x main header flags (for RAR4MainHeader).
	RAR4MainVolume       = 0x0001
	RAR4MainSolid        = 0x0008
	RAR4MainNewNumbering = 0x0010
	RAR4MainPassword     = 0x0080
	RAR4MainFirstVolume  = 0x0100

	rar4FileSplitBefore = 0x0001
	rar4FileSplitAfter  = 0x0002
	rar4FilePassword    = 0x0004
	rar4FileSolid       = 0x0010
	rar4FileDirMask     = 0x00e0
	rar4FileLarge       = 0x0100
	rar4FileUnicode     = 0x0200

	rar4EndNextVolume = 0x0001
	rar4EndVolNumber  = 0x0008
)

// RAR 5.0 constants.
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
)

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Header describes one RAR file header (a whole file or one volume's part of
// it) for RAR4FileHeader / RAR5FileHeader.
type Header struct {
	Name        string
	PackSize    int64 // data bytes following the header in this volume
	UnpackSize  int64 // whole-file size
	Method      int   // 0 = store .. 5 (RAR4 writes 0x30+Method)
	CRC         uint32
	SplitBefore bool
	SplitAfter  bool
	Encrypted   bool
	Solid       bool
	Dir         bool
	UnknownSize bool // unpacked size unknown
	Large       bool // RAR4: write the 64-bit size fields even if not needed
	UTF8Name    bool // RAR4: set LHD_UNICODE and store the name as plain UTF-8
	Service     bool // RAR4 new sub-block (0x7a) / RAR5 service header (type 3)

	dataSizePad int // RAR5: redundant bytes in the DataSize vint
}

// RAR4Block serialises a RAR 4.x block with a correct HEAD_SIZE and CRC.
func RAR4Block(typ byte, flags uint16, body []byte) []byte {
	h := make([]byte, 7+len(body))
	h[2] = typ
	binary.LittleEndian.PutUint16(h[3:], flags)
	binary.LittleEndian.PutUint16(h[5:], uint16(len(h)))
	copy(h[7:], body)
	binary.LittleEndian.PutUint16(h, uint16(crc32.ChecksumIEEE(h[2:])))
	return h
}

// RAR4MainHeader returns a RAR 4.x main archive header (RAR4Main* flags).
func RAR4MainHeader(flags uint16) []byte { return RAR4Block(rar4Main, flags, make([]byte, 6)) }

// RAR4EndHeader returns a RAR 4.x end-of-archive block; volNum < 0 omits
// the volume number.
func RAR4EndHeader(more bool, volNum int) []byte {
	var flags uint16
	var body []byte
	if more {
		flags |= rar4EndNextVolume
	}
	if volNum >= 0 {
		flags |= rar4EndVolNumber
		body = le16(uint16(volNum))
	}
	return RAR4Block(rar4End, flags, body)
}

// RAR4FileHeader returns a RAR 4.x file header (or new sub-block if
// h.Service). Names containing non-ASCII characters are written with RAR's
// unicode name encoding.
func RAR4FileHeader(h Header) []byte {
	typ := byte(rar4File)
	if h.Service {
		typ = rar4NewSub
	}
	flags := uint16(rar4LongBlock)
	if h.SplitBefore {
		flags |= rar4FileSplitBefore
	}
	if h.SplitAfter {
		flags |= rar4FileSplitAfter
	}
	if h.Encrypted {
		flags |= rar4FilePassword
	}
	if h.Solid {
		flags |= rar4FileSolid
	}
	if h.Dir {
		flags |= rar4FileDirMask
	}
	large := h.Large || h.PackSize > 0xffffffff || h.UnpackSize > 0xffffffff
	if large {
		flags |= rar4FileLarge
	}
	name, uni := RAR4EncodeName(h.Name)
	if uni || h.UTF8Name {
		flags |= rar4FileUnicode
	}
	unp := uint64(h.UnpackSize)
	if h.UnknownSize {
		unp = 0xffffffffffffffff
		if !large {
			unp = 0xffffffff
		}
	}
	attr := uint32(0x20)
	if h.Dir {
		attr = 0x10
	}
	body := cat(
		le32(uint32(h.PackSize)), le32(uint32(unp)), []byte{2}, le32(h.CRC),
		le32(0x5a8e3c21), []byte{29, byte(0x30 + h.Method)}, le16(uint16(len(name))), le32(attr),
	)
	if large {
		body = cat(body, le32(uint32(uint64(h.PackSize)>>32)), le32(uint32(unp>>32)))
	}
	return RAR4Block(typ, flags, cat(body, name))
}

// RAR4EncodeName returns the RAR 4.x name field for name and whether it
// needs LHD_UNICODE. Non-ASCII names get an OEM fallback, a NUL and RAR's
// compact UTF-16 encoding (using all four opcodes: run copy, low byte,
// high-byte page and full 16-bit).
func RAR4EncodeName(name string) ([]byte, bool) {
	u := utf16.Encode([]rune(name))
	ascii := true
	for _, c := range u {
		if c >= 0x80 {
			ascii = false
		}
	}
	if ascii {
		return []byte(name), false
	}
	oem := make([]byte, len(u))
	var high byte
	highSet := false
	for i, c := range u {
		if c < 0x80 {
			oem[i] = byte(c)
		} else {
			oem[i] = '_'
		}
		if c >= 0x100 && !highSet {
			high, highSet = byte(c>>8), true
		}
	}
	type op struct {
		kind byte
		data []byte
	}
	var ops []op
	for i := 0; i < len(u); {
		run := 0
		for i+run < len(u) && u[i+run] < 0x80 && run < 0x7f+2 {
			run++
		}
		if run >= 2 {
			ops = append(ops, op{3, []byte{byte(run - 2)}})
			i += run
			continue
		}
		c := u[i]
		switch {
		case c < 0x100:
			ops = append(ops, op{0, []byte{byte(c)}})
		case byte(c>>8) == high:
			ops = append(ops, op{1, []byte{byte(c)}})
		default:
			ops = append(ops, op{2, []byte{byte(c), byte(c >> 8)}})
		}
		i++
	}
	enc := []byte{high}
	for k := 0; k < len(ops); k += 4 {
		var fl byte
		for j := 0; j < 4 && k+j < len(ops); j++ {
			fl |= ops[k+j].kind << (6 - 2*j)
		}
		enc = append(enc, fl)
		for j := k; j < k+4 && j < len(ops); j++ {
			enc = append(enc, ops[j].data...)
		}
	}
	return cat(oem, []byte{0}, enc), true
}

// Vint encodes a RAR 5.0 variable-length integer.
func Vint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

// RAR5Block serialises a RAR 5.0 block: CRC32, HeaderSize, type, flags,
// optional extra-area and data sizes, the type-specific fields and the
// extra area.
func RAR5Block(typ, flags uint64, fields, extra []byte, hasData bool, dataSize int64) []byte {
	return rar5Block(typ, flags, fields, extra, hasData, dataSize, 0)
}

// paddedVint is Vint(v) padded with pad redundant continuation bytes (still
// a valid encoding of v).
func paddedVint(v uint64, pad int) []byte {
	out := Vint(v)
	for ; pad > 0; pad-- {
		out[len(out)-1] |= 0x80
		out = append(out, 0)
	}
	return out
}

func rar5Block(typ, flags uint64, fields, extra []byte, hasData bool, dataSize int64, pad int) []byte {
	if len(extra) > 0 {
		flags |= rar5HdrExtra
	}
	if hasData {
		flags |= rar5HdrData
	}
	body := cat(Vint(typ), Vint(flags))
	if len(extra) > 0 {
		body = cat(body, Vint(uint64(len(extra))))
	}
	if hasData {
		body = cat(body, paddedVint(uint64(dataSize), pad))
	}
	body = cat(body, fields, extra)
	h := cat(Vint(uint64(len(body))), body)
	return cat(le32(crc32.ChecksumIEEE(h)), h)
}

// RAR5Record serialises one extra-area record.
func RAR5Record(typ uint64, data []byte) []byte {
	rec := cat(Vint(typ), data)
	return cat(Vint(uint64(len(rec))), rec)
}

// RAR5FileHeader returns a RAR 5.0 file header (or service header if
// h.Service). A file-time extra record is always present; Encrypted adds a
// file encryption record.
func RAR5FileHeader(h Header) []byte {
	typ := uint64(rar5File)
	if h.Service {
		typ = rar5Service
	}
	var hf uint64
	if h.SplitBefore {
		hf |= rar5HdrSplitBefore
	}
	if h.SplitAfter {
		hf |= rar5HdrSplitAfter
	}
	ff := uint64(rar5FileMtime | rar5FileCRC)
	if h.Dir {
		ff = rar5FileDir | rar5FileMtime
	}
	if h.UnknownSize {
		ff |= rar5FileUnknownSize
	}
	fields := cat(Vint(ff), Vint(uint64(h.UnpackSize)), Vint(0x20), le32(0x63a1b2c3))
	if ff&rar5FileCRC != 0 {
		fields = cat(fields, le32(h.CRC))
	}
	ci := uint64(h.Method) << 7
	if h.Solid {
		ci |= rar5CompSolid
	}
	fields = cat(fields, Vint(ci), Vint(1), Vint(uint64(len(h.Name))), []byte(h.Name))
	extra := RAR5Record(3, cat(Vint(0x3), le32(0x63a1b2c3)))
	if h.Encrypted {
		extra = cat(extra, RAR5Record(1, cat(Vint(0), Vint(0), []byte{15}, bytes.Repeat([]byte{0xa5}, 32))))
	}
	return rar5Block(typ, hf, fields, extra, !h.Dir, h.PackSize, h.dataSizePad)
}

// RAR5MainHeader returns a RAR 5.0 main archive header. volNum > 0 writes
// the volume-number field (absent in the first volume).
func RAR5MainHeader(multi bool, volNum int, solid bool) []byte {
	var af uint64
	if multi {
		af |= rar5ArcVolume
	}
	if solid {
		af |= rar5ArcSolid
	}
	var num []byte
	if volNum > 0 {
		af |= rar5ArcVolNumber
		num = Vint(uint64(volNum))
	}
	// Locator record, as WinRAR writes it (offset 0 = absent).
	return RAR5Block(rar5Main, 0, cat(Vint(af), num), RAR5Record(1, Vint(0)), false, 0)
}

// RAR5EndHeader returns a RAR 5.0 end-of-archive header.
func RAR5EndHeader(more bool) []byte {
	var ef uint64
	if more {
		ef = 1
	}
	return RAR5Block(rar5End, 0, Vint(ef), nil, false, 0)
}

// RARFile is one input file for BuildRAR.
type RARFile struct {
	Name        string // '/'-separated; RAR4 writes it as given
	Data        []byte
	Method      int // 0 = store; RAR4 writes 0x30+Method
	Encrypted   bool
	Solid       bool
	Dir         bool
	UnknownSize bool // header says the unpacked size is unknown
	Large       bool // RAR4: 64-bit size fields
}

// RAROptions controls BuildRAR.
type RAROptions struct {
	Version int // 4 or 5 (0 means 5)
	// VolumeSize is the size of every volume except the last, like rar -v.
	// 0 builds a single (non-volume) archive. Each volume holds headers,
	// as much file data as fits, and the end-of-archive block. A volume is
	// closed early only when the next file header plus one data byte would
	// not fit; a volume ending in a split file is always exactly
	// VolumeSize (RAR5 pads the DataSize vint if a vint length step would
	// otherwise leave a byte over).
	VolumeSize      int64
	HeaderEncrypted bool // RAR4 MHD_PASSWORD / RAR5 archive encryption header (single volume, garbage after it)
	SolidArchive    bool // archive-level solid flag
	NewNumbering    bool // RAR4 MHD_NEWNUMBERING
	// OmitEndVolumeNumber leaves EARC_VOLNUMBER out of RAR4 end blocks
	// (RAR 3.x+ writes it in volumes).
	OmitEndVolumeNumber bool
	// ServiceBlocks adds blocks a parser must skip after the main header:
	// a comment service header with data, an old-style block (RAR4) or an
	// unknown block type (RAR5).
	ServiceBlocks bool
	OmitEndBlock  bool
}

// ErrVolumeTooSmall is returned when VolumeSize cannot hold a header.
var ErrVolumeTooSmall = errors.New("archivetest: volume size too small")

// BuildRAR returns the bytes of each volume in order.
func BuildRAR(files []RARFile, opts RAROptions) ([][]byte, error) {
	v := opts.Version
	if v == 0 {
		v = 5
	}
	if v != 4 && v != 5 {
		return nil, fmt.Errorf("archivetest: RAR version %d", opts.Version)
	}
	multi := opts.VolumeSize > 0
	fileHeader := RAR5FileHeader
	sig := RAR5Signature
	if v == 4 {
		fileHeader, sig = RAR4FileHeader, RAR4Signature
	}
	endBlock := func(vi int, more bool) []byte {
		if opts.OmitEndBlock {
			return nil
		}
		if v == 4 {
			num := -1
			if multi && !opts.OmitEndVolumeNumber {
				num = vi
			}
			return RAR4EndHeader(more, num)
		}
		return RAR5EndHeader(more)
	}

	var vols [][]byte
	var buf bytes.Buffer
	headersInVol := 0
	start := func() {
		vi := len(vols)
		buf.Reset()
		headersInVol = 0
		buf.Write(sig)
		if v == 4 {
			var mf uint16
			if multi {
				mf |= RAR4MainVolume
				if vi == 0 {
					mf |= RAR4MainFirstVolume
				}
			}
			if opts.SolidArchive {
				mf |= RAR4MainSolid
			}
			if opts.NewNumbering {
				mf |= RAR4MainNewNumbering
			}
			if opts.HeaderEncrypted {
				mf |= RAR4MainPassword
			}
			buf.Write(RAR4MainHeader(mf))
		} else {
			if opts.HeaderEncrypted {
				// The main header and everything after it are encrypted.
				buf.Write(RAR5Block(rar5Encryption, 0, cat(Vint(0), Vint(1), []byte{15}, bytes.Repeat([]byte{7}, 28)), nil, false, 0))
				return
			}
			buf.Write(RAR5MainHeader(multi, vi, opts.SolidArchive))
		}
		if opts.ServiceBlocks && vi == 0 {
			cmt := []byte("archive comment")
			buf.Write(fileHeader(Header{Name: "CMT", PackSize: int64(len(cmt)), UnpackSize: int64(len(cmt)), Service: true, CRC: crc32.ChecksumIEEE(cmt)}))
			buf.Write(cmt)
			junk := []byte("skip me")
			if v == 4 {
				// Old protect-style block: LONG_BLOCK with ADD_SIZE data.
				buf.Write(RAR4Block(0x78, rar4LongBlock, cat(le32(uint32(len(junk))), []byte{9, 9})))
			} else {
				buf.Write(RAR5Block(9, 0x0004, Vint(42), nil, true, int64(len(junk))))
			}
			buf.Write(junk)
		}
	}
	finish := func(more bool) {
		buf.Write(endBlock(len(vols), more))
		vols = append(vols, bytes.Clone(buf.Bytes()))
	}
	start()
	if opts.HeaderEncrypted {
		// Everything after the (encryption/main) header is ciphertext.
		buf.Write(bytes.Repeat([]byte{0x5c}, 64))
		return [][]byte{bytes.Clone(buf.Bytes())}, nil
	}
	endLen := int64(len(endBlock(0, true)))
	for i := range files {
		f := &files[i]
		hdrPad := func(pack int64, before, after bool, part []byte, pad int) []byte {
			crc := crc32.ChecksumIEEE(f.Data)
			if after {
				crc = crc32.ChecksumIEEE(part)
			}
			return fileHeader(Header{
				Name: f.Name, PackSize: pack, UnpackSize: int64(len(f.Data)), Method: f.Method, CRC: crc,
				SplitBefore: before, SplitAfter: after, Encrypted: f.Encrypted, Solid: f.Solid,
				Dir: f.Dir, UnknownSize: f.UnknownSize, Large: f.Large, dataSizePad: pad,
			})
		}
		hdr := func(pack int64, before, after bool, part []byte) []byte {
			return hdrPad(pack, before, after, part, 0)
		}
		rem, first := f.Data, true
		for {
			whole := hdr(int64(len(rem)), !first, false, nil)
			if !multi {
				buf.Write(whole)
				buf.Write(rem)
				break
			}
			space := opts.VolumeSize - int64(buf.Len()) - endLen
			if int64(len(whole))+int64(len(rem)) <= space {
				buf.Write(whole)
				buf.Write(rem)
				headersInVol++
				break
			}
			// Split: find the largest data part n whose header fits too.
			n := space - int64(len(hdr(max(space, 0), !first, true, nil)))
			for n >= 0 && n+1 < int64(len(rem)) && int64(len(hdr(n+1, !first, true, nil)))+n+1 <= space {
				n++
			}
			if n < 1 || len(rem) == 0 || int64(len(rem)) <= n {
				// Not even a header plus one byte fits: close this volume.
				if headersInVol == 0 {
					return nil, fmt.Errorf("%w: %d", ErrVolumeTooSmall, opts.VolumeSize)
				}
				finish(true)
				start()
				continue
			}
			part := rem[:n]
			// RAR5: when a vint length step makes an exact fit impossible,
			// pad the DataSize vint so the volume is exactly VolumeSize.
			pad := 0
			if v == 5 {
				for gap := int(space - n - int64(len(hdr(n, !first, true, part)))); gap > 0 && pad == 0; gap-- {
					if int64(len(hdrPad(n, !first, true, part, gap)))+n == space {
						pad = gap
					}
				}
			}
			buf.Write(hdrPad(n, !first, true, part, pad))
			buf.Write(part)
			rem, first = rem[n:], false
			finish(true)
			start()
		}
	}
	finish(false)
	return vols, nil
}

// SevenZipFile is one input file for Build7zCopy. Empty Data without Dir is
// an empty file.
type SevenZipFile struct {
	Name string
	Data []byte
	Dir  bool
}

func num7z(v uint64) []byte {
	for i := 0; i < 8; i++ {
		if v>>(8*uint(i)) < 1<<(7-uint(i)) {
			b := []byte{byte(uint(0xff00)>>uint(i)) | byte(v>>(8*uint(i)))}
			for j := 0; j < i; j++ {
				b = append(b, byte(v>>(8*uint(j))))
			}
			return b
		}
	}
	return append([]byte{0xff}, binary.LittleEndian.AppendUint64(nil, v)...)
}

// Number7z encodes a 7z NUMBER.
func Number7z(v uint64) []byte { return num7z(v) }

// Build7zCopy writes a 7z archive with a plain (unencoded) header whose
// non-empty files live in Copy folders: one folder holding every file as a
// substream when solid (7-Zip itself never does that for Copy), else one
// folder per file.
func Build7zCopy(files []SevenZipFile, solid bool) []byte {
	const (
		kEnd = iota
		kHeader
		_
		_
		kMainStreamsInfo
		kFilesInfo
		kPackInfo
		kUnpackInfo
		kSubStreamsInfo
		kSize
		kCRC
		kFolder
		kCodersUnpackSize
		kNumUnpackStream
		kEmptyStream
		kEmptyFile
		_
		kName
	)
	var packed []byte
	var sizes []uint64
	for _, f := range files {
		if len(f.Data) > 0 {
			packed = append(packed, f.Data...)
			sizes = append(sizes, uint64(len(f.Data)))
		}
	}
	var h []byte
	add := func(b ...[]byte) {
		for _, x := range b {
			h = append(h, x...)
		}
	}
	add([]byte{kHeader})
	if len(sizes) > 0 {
		add([]byte{kMainStreamsInfo})
		folderSizes := sizes
		if solid {
			folderSizes = []uint64{uint64(len(packed))}
		}
		add([]byte{kPackInfo}, num7z(0), num7z(uint64(len(folderSizes))), []byte{kSize})
		for _, s := range folderSizes {
			add(num7z(s))
		}
		add([]byte{kEnd, kUnpackInfo, kFolder}, num7z(uint64(len(folderSizes))), []byte{0})
		for range folderSizes {
			add(num7z(1), []byte{0x01, 0x00}) // one simple coder, method 00 (Copy)
		}
		add([]byte{kCodersUnpackSize})
		for _, s := range folderSizes {
			add(num7z(s))
		}
		add([]byte{kEnd})
		if solid {
			add([]byte{kSubStreamsInfo, kNumUnpackStream}, num7z(uint64(len(sizes))), []byte{kSize})
			for _, s := range sizes[:len(sizes)-1] {
				add(num7z(s))
			}
			add([]byte{kCRC, 1})
			for _, f := range files {
				if len(f.Data) > 0 {
					add(le32(crc32.ChecksumIEEE(f.Data)))
				}
			}
			add([]byte{kEnd})
		}
		add([]byte{kEnd})
	}
	add([]byte{kFilesInfo}, num7z(uint64(len(files))))
	bits := func(v []bool) []byte {
		out := make([]byte, (len(v)+7)/8)
		for i, b := range v {
			if b {
				out[i/8] |= 0x80 >> (i % 8)
			}
		}
		return out
	}
	empty := make([]bool, len(files))
	var emptyFile []bool
	anyEmpty := false
	for i, f := range files {
		if len(f.Data) == 0 {
			empty[i], anyEmpty = true, true
			emptyFile = append(emptyFile, !f.Dir)
		}
	}
	if anyEmpty {
		bf := bits(empty)
		add([]byte{kEmptyStream}, num7z(uint64(len(bf))), bf)
		ef := bits(emptyFile)
		add([]byte{kEmptyFile}, num7z(uint64(len(ef))), ef)
	}
	add([]byte{0x19}, num7z(3), []byte{0, 0, 0}) // kDummy padding
	var names []byte
	for _, f := range files {
		for _, c := range utf16.Encode([]rune(f.Name)) {
			names = append(names, le16(c)...)
		}
		names = append(names, 0, 0)
	}
	add([]byte{kName}, num7z(uint64(len(names)+1)), []byte{0}, names)
	add([]byte{kEnd, kEnd})

	sh := make([]byte, 32)
	copy(sh, "7z\xbc\xaf\x27\x1c")
	sh[7] = 4
	binary.LittleEndian.PutUint64(sh[12:], uint64(len(packed)))
	binary.LittleEndian.PutUint64(sh[20:], uint64(len(h)))
	binary.LittleEndian.PutUint32(sh[28:], crc32.ChecksumIEEE(h))
	binary.LittleEndian.PutUint32(sh[8:], crc32.ChecksumIEEE(sh[12:]))
	return cat(sh, packed, h)
}
