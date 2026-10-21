package archive

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"math"
	"sort"
)

// Encryption describes the CBC-encrypted logical stream formed by a member's
// Extents. Size excludes block padding; Offset locates this member within that
// plaintext stream (a 7z folder can contain several members). Keys are ephemeral
// parser results and must not be persisted or included in diagnostic JSON.
// CBC provides confidentiality, not authentication: archive password checks and
// checksums must be validated separately by the format reader.
type Encryption struct {
	Key    []byte `json:"-"`
	IV     [aes.BlockSize]byte
	Offset int64
	Size   int64
}

// newEncryptedMemberReader maps an encrypted member onto its volumes, decrypting only the
// requested AES-CBC blocks when necessary. It reads no volume data during
// construction. Metadata is copied, and independent ReadAt calls may run
// concurrently if the underlying volumes support concurrent ReadAt calls.
func newEncryptedMemberReader(vols []Volume, m Member) (io.ReaderAt, error) {
	if err := m.Streamable(); err != nil {
		return nil, err
	}
	if m.Size < 0 {
		return nil, fmt.Errorf("archive: negative member size: %w", ErrCorrupt)
	}
	r := &extentReader{volumes: append([]Volume(nil), vols...), extents: append([]Extent(nil), m.Extents...)}
	for _, e := range r.extents {
		if e.Volume < 0 || e.Volume >= len(r.volumes) {
			return nil, fmt.Errorf("archive: invalid extent volume: %w", ErrMissingVolume)
		}
		v := r.volumes[e.Volume]
		if v.R == nil || v.Size < 0 || e.Offset < 0 || e.Size < 0 || e.Offset > v.Size || e.Size > v.Size-e.Offset || e.Size > math.MaxInt64-r.size {
			return nil, fmt.Errorf("archive: invalid member extent: %w", ErrCorrupt)
		}
		r.size += e.Size
		r.ends = append(r.ends, r.size)
	}
	if m.Encryption == nil {
		if r.size != m.Size {
			return nil, fmt.Errorf("archive: member extent size mismatch: %w", ErrCorrupt)
		}
		return r, nil
	}
	e := *m.Encryption
	if e.Size < 0 || e.Offset < 0 || e.Offset > e.Size || m.Size > e.Size-e.Offset {
		return nil, fmt.Errorf("archive: invalid encrypted member bounds: %w", ErrCorrupt)
	}
	// Round up without overflowing even for malformed, near-MaxInt64 sizes.
	padded := e.Size
	if rem := padded % aes.BlockSize; rem != 0 {
		padding := int64(aes.BlockSize) - rem
		if padded > math.MaxInt64-padding {
			return nil, fmt.Errorf("archive: encrypted size overflow: %w", ErrCorrupt)
		}
		padded += padding
	}
	if r.size != padded || r.size%aes.BlockSize != 0 {
		return nil, fmt.Errorf("archive: ciphertext size mismatch: %w", ErrCorrupt)
	}
	block, err := aes.NewCipher(e.Key)
	if err != nil {
		return nil, fmt.Errorf("archive: invalid AES key: %w", ErrCorrupt)
	}
	return &memberCBCReader{source: r, block: block, iv: e.IV, offset: e.Offset, size: m.Size}, nil
}

type extentReader struct {
	volumes []Volume
	extents []Extent
	ends    []int64
	size    int64
}

func (r *extentReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("archive: negative read offset: %w", ErrCorrupt)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > r.size-off {
		want = int(r.size - off)
	}
	n := 0
	for n < want {
		i := sort.Search(len(r.ends), func(i int) bool { return r.ends[i] > off })
		e := r.extents[i]
		within := off - (r.ends[i] - e.Size)
		count := want - n
		if int64(count) > e.Size-within {
			count = int(e.Size - within)
		}
		got, err := r.volumes[e.Volume].R.ReadAt(p[n:n+count], e.Offset+within)
		if got < 0 || got > count {
			return n, fmt.Errorf("archive: invalid volume read count: %w", ErrCorrupt)
		}
		n += got
		off += int64(got)
		if err != nil && err != io.EOF {
			return n, fmt.Errorf("archive: volume read: %w", err)
		}
		if got != count {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return n, fmt.Errorf("archive: truncated member extent: %w: %w", ErrCorrupt, err)
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type memberCBCReader struct {
	source       *extentReader
	block        cipher.Block
	iv           [aes.BlockSize]byte
	offset, size int64
}

const cbcReadChunk = 64 * 1024

func (r *memberCBCReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("archive: negative read offset: %w", ErrCorrupt)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > r.size-off {
		want = int(r.size - off)
	}
	start := r.offset + off
	end := start + int64(want)
	alignedStart := start - start%aes.BlockSize
	alignedEnd := end
	if rem := end % aes.BlockSize; rem != 0 {
		alignedEnd += aes.BlockSize - rem
	}
	iv := r.iv
	if alignedStart != 0 {
		if _, err := r.source.ReadAt(iv[:], alignedStart-aes.BlockSize); err != nil {
			return 0, err
		}
	}
	decrypter := cipher.NewCBCDecrypter(r.block, iv[:])
	chunkSize := alignedEnd - alignedStart
	if chunkSize > cbcReadChunk {
		chunkSize = cbcReadChunk
	}
	buf := make([]byte, int(chunkSize))
	n := 0
	for pos := alignedStart; pos < alignedEnd; {
		count := int64(len(buf))
		if count > alignedEnd-pos {
			count = alignedEnd - pos
		}
		chunk := buf[:int(count)]
		if _, err := r.source.ReadAt(chunk, pos); err != nil {
			return n, err
		}
		decrypter.CryptBlocks(chunk, chunk)
		from, to := int64(0), count
		if pos < start {
			from = start - pos
		}
		if pos+to > end {
			to = end - pos
		}
		n += copy(p[n:want], chunk[int(from):int(to)])
		pos += count
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
