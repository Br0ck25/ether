package yenc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// DefaultMaxSize bounds the buffer a header can make the decoder allocate.
// Usenet parts are typically 384 KiB-1 MiB; 64 MiB leaves ample headroom
// while stopping a hostile header from requesting gigabytes.
const DefaultMaxSize = 64 << 20

// LineReader yields one line at a time without its line terminator (LF or
// CRLF). The returned slice is only valid until the next call. At the end of
// input it returns io.EOF. The NNTP client implements it directly over its
// connection buffer so article bodies are decoded without an extra copy.
type LineReader interface {
	ReadLine() ([]byte, error)
}

// DecodeOptions tunes decoding. The zero value is valid.
type DecodeOptions struct {
	// Buffer is reused for the decoded data when it has enough capacity.
	Buffer []byte
	// MaxSize caps the decoded part size; 0 means DefaultMaxSize.
	MaxSize int64
	// SkipCRC disables CRC32 verification (sizes are still checked).
	SkipCRC bool
}

// DecodeLines decodes one yEnc part from lr. Lines before =ybegin are
// skipped. Decoding stops after the =yend line; any remaining lines are left
// unread so the caller can drain its transport.
//
// When the data decodes but fails verification (ErrCRCMismatch or
// ErrSizeMismatch) the returned Part is non-nil and holds the data, so
// callers may choose to use it anyway.
func DecodeLines(lr LineReader, opt *DecodeOptions) (*Part, error) {
	var o DecodeOptions
	if opt != nil {
		o = *opt
	}
	if o.MaxSize <= 0 {
		o.MaxSize = DefaultMaxSize
	}

	p := &Part{}
	// 1. Find =ybegin.
	for {
		line, err := lr.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, ErrNoHeader
			}
			return nil, err
		}
		if bytes.HasPrefix(line, []byte(keyBegin)) {
			h, err := ParseBegin(line)
			if err != nil {
				return nil, err
			}
			p.Header = h
			break
		}
	}

	// 2. Optional =ypart, then data lines up to =yend.
	first := true
	escaped := false
	var dst, scratch []byte
	alloc := func(hint int64) error {
		if hint > o.MaxSize {
			return fmt.Errorf("%w: %d bytes", ErrTooLarge, hint)
		}
		if hint <= 0 {
			hint = 64 << 10
		}
		if int64(cap(o.Buffer)) >= hint {
			dst = o.Buffer[:0]
		} else {
			dst = make([]byte, 0, hint)
		}
		return nil
	}
	for {
		line, err := lr.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if dst != nil {
					p.Data = dst
				}
				return p, ErrTruncated
			}
			return nil, err
		}
		if first {
			first = false
			if bytes.HasPrefix(line, []byte(keyPart)) {
				if err := ParsePart(line, &p.Header); err != nil {
					return nil, err
				}
				if err := alloc(p.Header.PartSize()); err != nil {
					return nil, err
				}
				continue
			}
			if err := alloc(p.Header.PartSize()); err != nil {
				return nil, err
			}
		}
		if len(line) >= len(keyEnd) && line[0] == '=' && line[1] == 'y' && bytes.HasPrefix(line, []byte(keyEnd)) {
			t, err := ParseEnd(line)
			if err != nil {
				p.Data = dst
				return p, err
			}
			p.Trailer = t
			p.Data = dst
			return p, verify(p, o.SkipCRC)
		}
		if len(line) == 0 {
			continue
		}
		if cap(dst)-len(dst) >= len(line) {
			// Fast path: decode straight into the output (decoded length
			// never exceeds encoded length).
			n := len(dst)
			var w int
			w, escaped = DecodeLine(dst[n:n+len(line)], line, escaped)
			dst = dst[:n+w]
		} else {
			// Near the end of the announced size, or the size was wrong:
			// decode via scratch so an exact-size buffer is not doubled.
			if cap(scratch) < len(line) {
				scratch = make([]byte, len(line))
			}
			var w int
			w, escaped = DecodeLine(scratch[:len(line)], line, escaped)
			if int64(len(dst)+w) > o.MaxSize {
				return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, o.MaxSize)
			}
			dst = append(dst, scratch[:w]...)
		}
	}
}

func verify(p *Part, skipCRC bool) error {
	n := int64(len(p.Data))
	if p.Trailer.Size != n {
		return fmt.Errorf("%w: decoded %d bytes, =yend size=%d", ErrSizeMismatch, n, p.Trailer.Size)
	}
	if want := p.Header.PartSize(); want != n {
		return fmt.Errorf("%w: decoded %d bytes, header announces %d", ErrSizeMismatch, n, want)
	}
	if skipCRC {
		return nil
	}
	whole := !p.Header.Multipart || (p.Header.Begin == 1 && p.Header.End == p.Header.Size)
	switch {
	case p.Trailer.HasPartCRC:
		if got := crc32.ChecksumIEEE(p.Data); got != p.Trailer.PartCRC32 {
			return fmt.Errorf("%w: pcrc32 got %08x want %08x", ErrCRCMismatch, got, p.Trailer.PartCRC32)
		}
	case p.Trailer.HasCRC && whole:
		if got := crc32.ChecksumIEEE(p.Data); got != p.Trailer.CRC32 {
			return fmt.Errorf("%w: crc32 got %08x want %08x", ErrCRCMismatch, got, p.Trailer.CRC32)
		}
	}
	return nil
}

// DecodeLine decodes one encoded line (without line terminator) into dst and
// returns the number of bytes written. dst must have room for len(src)
// bytes. escaped carries a dangling '=' from the end of the previous line,
// and the returned flag reports a dangling '=' at the end of this one.
//
// This is the hot loop. Escapes are rare (about 1.6% of bytes), so the line
// is processed as runs between '=' characters: bytes.IndexByte finds the
// next escape with SIMD and the run is decoded eight bytes per step with
// SWAR arithmetic (subRun), which beats a 256-entry lookup table that costs
// a load per byte. Nothing is allocated.
func DecodeLine(dst, src []byte, escaped bool) (int, bool) {
	n := 0
	if escaped && len(src) > 0 {
		dst[0] = src[0] - 106
		n = 1
		src = src[1:]
		escaped = false
	}
	for len(src) > 0 {
		i := bytes.IndexByte(src, '=')
		if i < 0 {
			i = len(src)
		}
		subRun(dst[n:n+i], src[:i])
		n += i
		if i == len(src) {
			break
		}
		// '=' at index i escapes the next byte.
		if i+1 >= len(src) {
			return n, true
		}
		dst[n] = src[i+1] - 106 // -42 -64
		n++
		src = src[i+2:]
	}
	return n, escaped
}

// Decode decodes a whole encoded article body held in memory.
func Decode(encoded []byte) (*Part, error) {
	return DecodeLines(&bytesLines{b: encoded}, nil)
}

// DecodeReader decodes one part from r.
func DecodeReader(r io.Reader, opt *DecodeOptions) (*Part, error) {
	return DecodeLines(NewLineReader(r), opt)
}

// bytesLines is a LineReader over an in-memory buffer.
type bytesLines struct{ b []byte }

func (l *bytesLines) ReadLine() ([]byte, error) {
	if len(l.b) == 0 {
		return nil, io.EOF
	}
	i := bytes.IndexByte(l.b, '\n')
	var line []byte
	if i < 0 {
		line, l.b = l.b, nil
	} else {
		line, l.b = l.b[:i], l.b[i+1:]
	}
	return trimCR(line), nil
}

func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}

// NewLineReader adapts an io.Reader to a LineReader. Lines longer than the
// internal buffer are assembled in a side buffer.
func NewLineReader(r io.Reader) LineReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &readerLines{r: br}
	}
	return &readerLines{r: bufio.NewReaderSize(r, 32<<10)}
}

type readerLines struct {
	r    *bufio.Reader
	long []byte
}

func (l *readerLines) ReadLine() ([]byte, error) {
	line, err := l.r.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		l.long = append(l.long[:0], line...)
		for err == bufio.ErrBufferFull {
			line, err = l.r.ReadSlice('\n')
			l.long = append(l.long, line...)
		}
		line = l.long
	}
	if err != nil {
		if err == io.EOF && len(line) > 0 {
			return trimCR(line), nil
		}
		return nil, err
	}
	return trimCR(line[:len(line)-1]), nil
}

// subRun writes src[i]-42 to dst[i] for every byte. It works on eight bytes
// at a time with SWAR arithmetic: per-byte subtraction without borrows
// crossing byte lanes, ((a|H) - L) ^ ((a ^ ^L) & H).
func subRun(dst, src []byte) {
	const (
		h = 0x8080808080808080
		l = 0x2a2a2a2a2a2a2a2a
	)
	dst = dst[:len(src)]
	for len(src) >= 8 {
		a := binary.LittleEndian.Uint64(src)
		binary.LittleEndian.PutUint64(dst, ((a|h)-l)^((a^^uint64(l))&h))
		src, dst = src[8:], dst[8:]
	}
	for i, c := range src {
		dst[i] = c - 42
	}
}
