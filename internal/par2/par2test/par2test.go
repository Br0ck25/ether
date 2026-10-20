// Package par2test builds PAR2 2.0 metadata packets and index files for
// tests. It is for tests only: production code must not import it.
//
// Packets carry correct packet MD5s. File IDs are derived the way PAR2
// clients do (MD5 of the 16k hash, the length and the name); recovery set
// IDs are the MD5 of the Main packet body. No recovery slices are written.
package par2test

import (
	"crypto/md5"
	"encoding/binary"
	"hash/crc32"
	"unicode/utf16"

	"github.com/braedonsaunders/ether/internal/par2"
)

// File is one file to describe.
type File struct {
	Name string
	Data []byte
	// UniName, if set, adds a Unicode filename packet.
	UniName string
	// NoIFSC omits the file's slice checksum packet.
	NoIFSC bool
}

// Packet returns one valid packet of type typ (a par2.Type* constant).
func Packet(setID par2.ID, typ string, body []byte) []byte {
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	n := par2.HeaderSize + len(body)
	p := make([]byte, n)
	copy(p, par2.Magic)
	binary.LittleEndian.PutUint64(p[8:], uint64(n))
	copy(p[32:], setID[:])
	copy(p[48:], typ)
	copy(p[64:], body)
	sum := md5.Sum(p[32:])
	copy(p[16:], sum[:])
	return p
}

// FileID derives a file's ID from its 16k hash, length and name.
func FileID(name string, data []byte) par2.ID {
	h16 := hash16k(data)
	b := append(h16[:], le64(uint64(len(data)))...)
	b = append(b, name...)
	return md5.Sum(b)
}

func hash16k(data []byte) [16]byte {
	return md5.Sum(data[:min(len(data), par2.Hash16kSize)])
}

func le64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

// MainBody returns a Main packet body.
func MainBody(sliceSize int64, ids []par2.ID) []byte {
	b := le64(uint64(sliceSize))
	n := make([]byte, 4)
	binary.LittleEndian.PutUint32(n, uint32(len(ids)))
	b = append(b, n...)
	for _, id := range ids {
		b = append(b, id[:]...)
	}
	return b
}

// FileDescBody returns a File Description packet body.
func FileDescBody(id par2.ID, name string, data []byte) []byte {
	whole := md5.Sum(data)
	h16 := hash16k(data)
	b := append([]byte{}, id[:]...)
	b = append(b, whole[:]...)
	b = append(b, h16[:]...)
	b = append(b, le64(uint64(len(data)))...)
	return append(b, name...)
}

// IFSCBody returns the slice checksum packet body of data.
func IFSCBody(id par2.ID, sliceSize int64, data []byte) []byte {
	b := append([]byte{}, id[:]...)
	for off := int64(0); off < int64(len(data)); off += sliceSize {
		chunk := make([]byte, sliceSize)
		copy(chunk, data[off:min(off+sliceSize, int64(len(data)))])
		sum := md5.Sum(chunk)
		b = append(b, sum[:]...)
		c := make([]byte, 4)
		binary.LittleEndian.PutUint32(c, crc32.ChecksumIEEE(chunk))
		b = append(b, c...)
	}
	return b
}

// UniNameBody returns a Unicode filename packet body.
func UniNameBody(id par2.ID, name string) []byte {
	b := append([]byte{}, id[:]...)
	for _, u := range utf16.Encode([]rune(name)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// Index returns a PAR2 index file describing files with the given slice
// size (a positive multiple of 4), and its recovery set ID.
func Index(sliceSize int64, files []File) ([]byte, par2.ID) {
	ids := make([]par2.ID, len(files))
	for i, f := range files {
		ids[i] = FileID(f.Name, f.Data)
	}
	main := MainBody(sliceSize, ids)
	setID := par2.ID(md5.Sum(main))
	var out []byte
	out = append(out, Packet(setID, par2.TypeMain, main)...)
	for i, f := range files {
		out = append(out, Packet(setID, par2.TypeFileDesc, FileDescBody(ids[i], f.Name, f.Data))...)
		if !f.NoIFSC {
			out = append(out, Packet(setID, par2.TypeIFSC, IFSCBody(ids[i], sliceSize, f.Data))...)
		}
		if f.UniName != "" {
			out = append(out, Packet(setID, par2.TypeUniName, UniNameBody(ids[i], f.UniName))...)
		}
	}
	out = append(out, Packet(setID, par2.TypeCreator, []byte("par2test"))...)
	return out, setID
}
