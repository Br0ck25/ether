// Package par2 parses the metadata packets of PAR2 2.0 files.
//
// Ether never repairs with PAR2: a recovery block is a linear combination of
// every slice of the set, so rebuilding one missing article would mean
// reading the whole file, which defeats streaming. What it does use is the
// cheap part of a PAR2 set, the index file: File Description packets give
// every file's real name, exact length, MD5 of the whole file and MD5 of its
// first 16 KiB, and Input File Slice Checksum packets give the MD5 and CRC32
// of each slice. That lets the stream engine name obfuscated posts from the
// first article of each file and check that what it serves is the content
// the poster hashed.
//
// The parser is defensive: packets are located by scanning for the magic,
// every packet's MD5 is verified, corrupt, truncated and unknown packets are
// skipped and repeated packets (PAR2 files repeat critical packets) are
// de-duplicated.
package par2

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"path"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Magic starts every PAR2 packet.
const Magic = "PAR2\x00PKT"

// HeaderSize is the size of a packet header.
const HeaderSize = 64

// Hash16kSize is how many leading bytes the "first 16k" MD5 covers.
const Hash16kSize = 16 * 1024

// Packet types.
const (
	TypeMain     = "PAR 2.0\x00Main\x00\x00\x00\x00"
	TypeFileDesc = "PAR 2.0\x00FileDesc"
	TypeIFSC     = "PAR 2.0\x00IFSC\x00\x00\x00\x00"
	TypeCreator  = "PAR 2.0\x00Creator\x00"
	TypeUniName  = "PAR 2.0\x00UniFileN"
)

// ErrNoFiles is returned by Parse when the data holds no valid File
// Description packet.
var ErrNoFiles = errors.New("par2: no valid file description packets")

// ID is a 16-byte PAR2 identifier (recovery set or file ID).
type ID [16]byte

// Slice is the checksum pair of one input slice. The last slice of a file
// is hashed zero-padded to the slice size.
type Slice struct {
	MD5   [16]byte
	CRC32 uint32
}

// File is one file of a recovery set.
type File struct {
	SetID ID
	ID    ID
	// Name is the file name as stored (the Unicode name when the set has
	// one). It may contain a relative path; use path.Base for display.
	Name    string
	Length  int64
	MD5     [16]byte // MD5 of the whole file
	MD5_16k [16]byte // MD5 of the first min(16 KiB, Length) bytes
	// SliceSize is the recovery set's slice size, 0 if the Main packet was
	// not seen. Slices are the IFSC checksums, nil if absent.
	SliceSize int64
	Slices    []Slice
}

// Set is everything usable found in one or more PAR2 files. Files from
// several recovery sets may be present.
type Set struct {
	// Files in order of first appearance.
	Files []*File
	// Creator is the first Creator packet's text, for diagnostics.
	Creator string
	// Packets counts valid, distinct packets; Skipped counts packet
	// headers that were corrupt, truncated or of an unusable type.
	Packets int
	Skipped int
}

type fileKey struct{ set, file ID }

// Parse scans data (one or more concatenated PAR2 files, possibly with junk
// or gaps from missing articles) for packets. It returns ErrNoFiles if no
// valid File Description packet is found.
func Parse(data []byte) (*Set, error) {
	s := &Set{}
	seen := map[[16]byte]bool{}
	descs := map[fileKey]*File{}
	var order []fileKey
	slices := map[fileKey][]Slice{}
	uni := map[fileKey]string{}
	sliceSize := map[ID]int64{}

	pos := 0
	for {
		i := bytes.Index(data[pos:], []byte(Magic))
		if i < 0 {
			break
		}
		at := pos + i
		pkt, ok := packetAt(data, at)
		if !ok {
			s.Skipped++
			pos = at + 1
			continue
		}
		pos = at + len(pkt)
		var sum [16]byte
		copy(sum[:], pkt[16:32])
		if seen[sum] {
			continue
		}
		seen[sum] = true
		var setID ID
		copy(setID[:], pkt[32:48])
		typ, body := string(pkt[48:64]), pkt[64:]
		used := true
		switch typ {
		case TypeMain:
			if len(body) < 12 || (len(body)-12)%16 != 0 {
				used = false
				break
			}
			ss := binary.LittleEndian.Uint64(body[0:8])
			if ss == 0 || ss%4 != 0 || ss > 1<<40 {
				used = false
				break
			}
			if _, dup := sliceSize[setID]; !dup {
				sliceSize[setID] = int64(ss)
			}
		case TypeFileDesc:
			if len(body) < 56 {
				used = false
				break
			}
			f := &File{SetID: setID}
			copy(f.ID[:], body[0:16])
			copy(f.MD5[:], body[16:32])
			copy(f.MD5_16k[:], body[32:48])
			n := binary.LittleEndian.Uint64(body[48:56])
			name := decodeName(bytes.TrimRight(body[56:], "\x00"))
			if n > 1<<62 || name == "" {
				used = false
				break
			}
			f.Length, f.Name = int64(n), name
			k := fileKey{setID, f.ID}
			if _, dup := descs[k]; !dup {
				descs[k] = f
				order = append(order, k)
			}
		case TypeIFSC:
			if len(body) < 16 || (len(body)-16)%20 != 0 {
				used = false
				break
			}
			var fid ID
			copy(fid[:], body[0:16])
			k := fileKey{setID, fid}
			if _, dup := slices[k]; dup {
				break
			}
			n := (len(body) - 16) / 20
			sl := make([]Slice, n)
			for j := range sl {
				e := body[16+20*j:]
				copy(sl[j].MD5[:], e[0:16])
				sl[j].CRC32 = binary.LittleEndian.Uint32(e[16:20])
			}
			slices[k] = sl
		case TypeUniName:
			if len(body) < 18 {
				used = false
				break
			}
			var fid ID
			copy(fid[:], body[0:16])
			if name := decodeUTF16(body[16:]); name != "" {
				k := fileKey{setID, fid}
				if _, dup := uni[k]; !dup {
					uni[k] = name
				}
			}
		case TypeCreator:
			if s.Creator == "" {
				s.Creator = strings.TrimRight(string(body), "\x00")
			}
		default:
			used = false // recovery slices and unknown packets
		}
		if used {
			s.Packets++
		} else {
			s.Skipped++
		}
	}
	for _, k := range order {
		f := descs[k]
		f.SliceSize = sliceSize[k.set]
		if sl, ok := slices[k]; ok && f.SliceSize > 0 && int64(len(sl)) == (f.Length+f.SliceSize-1)/f.SliceSize {
			f.Slices = sl
		}
		if n, ok := uni[k]; ok {
			f.Name = n
		}
		s.Files = append(s.Files, f)
	}
	if len(s.Files) == 0 {
		return nil, ErrNoFiles
	}
	return s, nil
}

// packetAt returns the packet at data[at:] if its header is sane and its
// MD5 verifies.
func packetAt(data []byte, at int) ([]byte, bool) {
	if len(data)-at < HeaderSize {
		return nil, false
	}
	n := binary.LittleEndian.Uint64(data[at+8 : at+16])
	if n < HeaderSize || n%4 != 0 || n > uint64(len(data)-at) {
		return nil, false
	}
	pkt := data[at : at+int(n)]
	sum := md5.Sum(pkt[32:])
	if !bytes.Equal(sum[:], pkt[16:32]) {
		return nil, false
	}
	return pkt, true
}

// decodeName turns the stored name bytes into a string. PAR2 says ASCII;
// in practice clients write UTF-8 or a legacy code page, which is read as
// Latin-1.
func decodeName(b []byte) string {
	if utf8.Valid(b) {
		return cleanName(string(b))
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return cleanName(string(r))
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return cleanName(string(utf16.Decode(u)))
}

// cleanName drops control characters, which no file name should carry.
func cleanName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// Hash16k returns the MD5 PAR2 records for a file of the given length whose
// leading bytes are prefix, and whether prefix is long enough to compute it.
func Hash16k(prefix []byte, length int64) ([16]byte, bool) {
	need := int64(Hash16kSize)
	if length < need {
		need = length
	}
	if need < 0 || int64(len(prefix)) < need {
		return [16]byte{}, false
	}
	return md5.Sum(prefix[:need]), true
}

// Match16k returns the files whose first-16k MD5 is sum.
func (s *Set) Match16k(sum [16]byte) []*File {
	var out []*File
	for _, f := range s.Files {
		if f.MD5_16k == sum {
			out = append(out, f)
		}
	}
	return out
}

// ByLength returns the files of exactly n bytes.
func (s *Set) ByLength(n int64) []*File {
	var out []*File
	for _, f := range s.Files {
		if f.Length == n {
			out = append(out, f)
		}
	}
	return out
}

// Named returns the files whose base name is name: exact matches, or, if
// there are none, case-insensitive ones.
func (s *Set) Named(name string) []*File {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	var exact, fold []*File
	for _, f := range s.Files {
		fb := f.BaseName()
		switch {
		case fb == base:
			exact = append(exact, f)
		case strings.EqualFold(fb, base):
			fold = append(fold, f)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return fold
}

// Identify finds the file whose content starts with prefix and, if size >
// 0, is size bytes long. The first-16k MD5 decides when prefix covers it
// (and must then be unique among the files of that length); otherwise, or
// when no hash matches, a unique exact length does. byHash reports which
// rule matched. A file found by length alone whose 16k hash did not match is
// still returned, so the caller can name it and report the mismatch.
func (s *Set) Identify(prefix []byte, size int64) (f *File, byHash bool) {
	if size > 0 {
		if sum, ok := Hash16k(prefix, size); ok {
			var hits []*File
			for _, c := range s.Match16k(sum) {
				if c.Length == size {
					hits = append(hits, c)
				}
			}
			if uniqueContent(hits) {
				return hits[0], true
			}
		}
		if hits := s.ByLength(size); uniqueContent(hits) {
			return hits[0], false
		}
		return nil, false
	}
	// Unknown size: the hash of the first 16 KiB, if every candidate is at
	// least that long, and only if exactly one file has it.
	if len(prefix) >= Hash16kSize {
		var hits []*File
		for _, c := range s.Match16k(md5.Sum(prefix[:Hash16kSize])) {
			if c.Length >= Hash16kSize {
				hits = append(hits, c)
			}
		}
		if uniqueContent(hits) {
			return hits[0], true
		}
	}
	return nil, false
}

// uniqueContent: exactly one file, or several entries for the same file
// (the same name and hashes, e.g. listed by two recovery sets).
func uniqueContent(fs []*File) bool {
	if len(fs) == 0 {
		return false
	}
	for _, f := range fs[1:] {
		if f.Name != fs[0].Name || f.MD5 != fs[0].MD5 || f.Length != fs[0].Length {
			return false
		}
	}
	return true
}

// BaseName is the file name without any directory part.
func (f *File) BaseName() string {
	return path.Base(strings.ReplaceAll(f.Name, `\`, "/"))
}

// maxSlicePad bounds the zero padding Verify hashes for a short last
// slice; real slice sizes are a few MiB.
const maxSlicePad = 256 << 20

var zeroPad [32 << 10]byte

// MismatchError says which PAR2 checksum the content failed.
type MismatchError struct {
	Name   string
	What   string // "length", "first 16 KiB MD5", "slice N MD5", "slice N CRC32", "file MD5"
	Detail string
}

func (e *MismatchError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("par2: %s: %s mismatch (%s)", e.Name, e.What, e.Detail)
	}
	return fmt.Sprintf("par2: %s: %s mismatch", e.Name, e.What)
}

// Verify checks a file's leading bytes (prefix) and, when known (> 0), its
// size against the recorded checksums: the length, the first-16k MD5, every
// slice prefix fully covers (CRC32 and MD5) and, when prefix is the whole
// file, its MD5. It returns how many checks ran (0 when prefix is too short
// to check anything) and a *MismatchError on the first failure.
func (f *File) Verify(prefix []byte, size int64) (checks int, err error) {
	if size > 0 {
		checks++
		if size != f.Length {
			return checks, &MismatchError{Name: f.Name, What: "length", Detail: fmt.Sprintf("%d bytes, par2 says %d", size, f.Length)}
		}
	}
	if int64(len(prefix)) > f.Length {
		prefix = prefix[:f.Length]
	}
	if sum, ok := Hash16k(prefix, f.Length); ok {
		checks++
		if sum != f.MD5_16k {
			return checks, &MismatchError{Name: f.Name, What: "first 16 KiB MD5"}
		}
	}
	if f.SliceSize > 0 && len(f.Slices) > 0 {
		for k, sl := range f.Slices {
			start := int64(k) * f.SliceSize
			end := min(start+f.SliceSize, f.Length)
			if end > int64(len(prefix)) {
				break
			}
			// The last slice is hashed zero-padded to SliceSize. The padding
			// is streamed rather than allocated: SliceSize comes from the
			// (untrusted) Main packet and may be up to 1 TiB. Padding beyond
			// maxSlicePad is not checked at all.
			short := f.SliceSize - (end - start)
			if short > maxSlicePad {
				break
			}
			crc, sum := crc32.NewIEEE(), md5.New()
			crc.Write(prefix[start:end])
			sum.Write(prefix[start:end])
			for short > 0 {
				z := zeroPad[:min(short, int64(len(zeroPad)))]
				crc.Write(z)
				sum.Write(z)
				short -= int64(len(z))
			}
			checks++
			if crc.Sum32() != sl.CRC32 {
				return checks, &MismatchError{Name: f.Name, What: fmt.Sprintf("slice %d CRC32", k)}
			}
			if [16]byte(sum.Sum(nil)) != sl.MD5 {
				return checks, &MismatchError{Name: f.Name, What: fmt.Sprintf("slice %d MD5", k)}
			}
		}
	}
	if int64(len(prefix)) == f.Length {
		checks++
		if md5.Sum(prefix) != f.MD5 {
			return checks, &MismatchError{Name: f.Name, What: "file MD5"}
		}
	}
	return checks, nil
}

// SortedNames lists the base names of the set's files, sorted (for logs).
func (s *Set) SortedNames() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		out = append(out, f.BaseName())
	}
	sort.Strings(out)
	return out
}
