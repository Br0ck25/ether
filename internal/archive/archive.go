// Package archive reads the headers of RAR (4.x and 5.x) and 7z archives and
// maps each inner file onto byte ranges (extents) of the archive volumes, so
// that store-mode (uncompressed, unencrypted) members can be streamed straight
// out of the volumes without extracting anything.
//
// Every volume is accessed through an io.ReaderAt whose reads may be expensive
// (each one can trigger a network fetch), so the parsers read only header
// bytes, in small exact-size chunks, and never touch file data.
package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Sentinel errors. Errors returned by this package wrap one of these (via
// fmt.Errorf with %w) so callers can test them with errors.Is.
var (
	ErrNotArchive    = errors.New("archive: not a recognised archive")
	ErrCompressed    = errors.New("archive: compressed (not store mode)")
	ErrEncrypted     = errors.New("archive: encrypted")
	ErrSolid         = errors.New("archive: solid archive")
	ErrUnsupported   = errors.New("archive: unsupported archive feature")
	ErrCorrupt       = errors.New("archive: corrupt or truncated header")
	ErrMissingVolume = errors.New("archive: missing volume")
)

// Format identifies an archive container format.
type Format int

// Known formats.
const (
	FormatUnknown Format = iota
	FormatRAR4
	FormatRAR5
	Format7z
)

// String returns "unknown", "rar4", "rar5" or "7z".
func (f Format) String() string {
	switch f {
	case FormatRAR4:
		return "rar4"
	case FormatRAR5:
		return "rar5"
	case Format7z:
		return "7z"
	default:
		return "unknown"
	}
}

var (
	sigRAR4 = []byte("Rar!\x1a\x07\x00")
	sigRAR5 = []byte("Rar!\x1a\x07\x01\x00")
	sig7z   = []byte("7z\xbc\xaf\x27\x1c")
)

// SignatureLen is the number of leading bytes Detect needs to recognise every
// supported format.
const SignatureLen = 8

// Detect sniffs the signature at the start of head. It needs at most
// SignatureLen bytes.
func Detect(head []byte) Format {
	switch {
	case bytes.HasPrefix(head, sigRAR5):
		return FormatRAR5
	case bytes.HasPrefix(head, sigRAR4):
		return FormatRAR4
	case bytes.HasPrefix(head, sig7z):
		return Format7z
	default:
		return FormatUnknown
	}
}

// Volume is one archive volume.
type Volume struct {
	R    io.ReaderAt
	Size int64
}

// Member is one inner file mapped onto volume byte ranges.
type Member struct {
	Name      string
	Size      int64 // unpacked size
	Stored    bool
	Encrypted bool
	Solid     bool
	Dir       bool
	Method    string   // human-readable, e.g. "store", "rar5:m3", "lzma", "copy", "aes"
	Extents   []Extent // in order; sum of Extent.Size == Size for stored files
}

// Extent is a contiguous byte range inside one volume.
type Extent struct {
	Volume int   // index into the volumes slice passed in
	Offset int64 // byte offset inside that volume
	Size   int64
}

// Streamable returns nil if the member can be read directly from its extents,
// else an error wrapping ErrEncrypted, ErrSolid or ErrCompressed (checked in
// that order), or ErrUnsupported for a directory.
func (m *Member) Streamable() error {
	switch {
	case m.Encrypted:
		return fmt.Errorf("archive: member %q: %w", m.Name, ErrEncrypted)
	case m.Solid:
		return fmt.Errorf("archive: member %q: %w", m.Name, ErrSolid)
	case !m.Stored:
		return fmt.Errorf("archive: member %q (method %s): %w", m.Name, m.Method, ErrCompressed)
	case m.Dir:
		return fmt.Errorf("archive: member %q is a directory: %w", m.Name, ErrUnsupported)
	}
	return nil
}

// readAt fills buf from r at off. limit is the known size of r; reads that
// would cross it fail with ErrCorrupt without touching r. A short read
// (io.EOF, io.ErrUnexpectedEOF or a nil error with n < len(buf)) is reported
// as ErrCorrupt; any other error from r is wrapped with %w so that the
// caller's errors.Is still sees it.
func readAt(r io.ReaderAt, buf []byte, off, limit int64) error {
	if off < 0 || int64(len(buf)) > limit-off {
		return fmt.Errorf("archive: header at offset %d (+%d) runs past end of volume (%d bytes): %w",
			off, len(buf), limit, ErrCorrupt)
	}
	n, err := r.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || err == io.EOF || err == io.ErrUnexpectedEOF {
		return fmt.Errorf("archive: short read at offset %d (%d of %d bytes): %w", off, n, len(buf), ErrCorrupt)
	}
	return fmt.Errorf("archive: read at offset %d: %w", off, err)
}
