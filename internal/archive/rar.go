package archive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
)

// RARFile is one file header found in one RAR volume.
type RARFile struct {
	Name         string      // path inside the archive, '/'-separated
	HeaderOffset int64       // offset of the block header in this volume
	DataOffset   int64       // offset of this file's data in this volume
	PackedSize   int64       // bytes of this file's data stored in this volume
	UnpackedSize int64       // total unpacked size of the whole file (-1 if unknown)
	Method       int         // raw method: RAR4 0x30..0x35, RAR5 0..5
	Stored       bool        // RAR4 method 0x30 / RAR5 method 0
	Encryption   *Encryption `json:"-"`
	Encrypted    bool        // RAR4 LHD_PASSWORD, RAR5 file encryption extra record
	Solid        bool        // RAR4 LHD_SOLID / RAR5 compression-info solid bit
	Dir          bool
	SplitBefore  bool // continued from previous volume
	SplitAfter   bool // continues in next volume
	CRC32        uint32
	HasCRC       bool
}

// RARVolume is the parsed header listing of one volume.
type RARVolume struct {
	Format       Format // FormatRAR4 or FormatRAR5
	MultiVolume  bool   // archive is part of a volume set
	First        bool   // first volume (also true for a single-volume archive)
	Number       int    // 0-based volume number when known, else -1
	Solid        bool   // archive-level solid flag
	NewNumbering bool   // RAR4 MHD_NEWNUMBERING (name.partNN.rar)
	Files        []RARFile
	DataStart    int64 // offset of the first byte after the main archive header
	EndFound     bool  // end-of-archive block was read
	MoreVolumes  bool  // end block says another volume follows (only meaningful if EndFound)
}

// RAROptions tunes ReadRARVolume.
type RAROptions struct {
	// MaxFiles stops parsing after this many file headers (0 = no limit).
	MaxFiles      int
	Password      string `json:"-"`
	Context       context.Context
	PasswordCache *RARPasswordCache
}

// ReadRARVolume parses the block headers of one RAR4 or RAR5 volume without
// reading file data. It stops at the end-of-archive block, at size, after a
// file header with SplitAfter set (nothing streamable follows it in this
// volume, so the end block is not read), or after opts.MaxFiles file headers.
// opts may be nil.
//
// Encrypted headers require opts.Password; derived keys remain ephemeral. Unknown
// block types are skipped. Header CRCs are verified (ErrCorrupt on mismatch).
func ReadRARVolume(r io.ReaderAt, size int64, opts *RAROptions) (*RARVolume, error) {
	var o RAROptions
	if opts != nil {
		o = *opts
	}
	if o.Context != nil {
		if err := o.Context.Err(); err != nil {
			return nil, err
		}
	}
	n := min(int64(SignatureLen), max(size, 0))
	head := make([]byte, n)
	if err := readAt(r, head, 0, size); err != nil {
		return nil, err
	}
	switch Detect(head) {
	case FormatRAR4:
		return readRAR4(r, size, o)
	case FormatRAR5:
		return readRAR5(r, size, o)
	default:
		return nil, fmt.Errorf("archive: no RAR signature: %w", ErrNotArchive)
	}
}

// rarMethodName returns the human-readable method of a RAR file header.
func rarMethodName(f Format, file *RARFile) string {
	if file.Stored {
		return "store"
	}
	if f == FormatRAR4 {
		return fmt.Sprintf("rar4:m%d", file.Method-0x30)
	}
	return fmt.Sprintf("rar5:m%d", file.Method)
}

// ComposeRAR joins per-volume listings (vols in volume order) into members.
// A file that starts in volume i and continues (SplitAfter) is followed into
// volume i+1, whose first file header must be the continuation (SplitBefore,
// same name); otherwise the result wraps ErrMissingVolume (the set ends
// early or a volume number is skipped) or ErrCorrupt.
//
// Extents of stored members cover exactly the member bytes; extents of other
// members cover their packed (compressed and/or encrypted) data.
func ComposeRAR(vols []*RARVolume) ([]Member, error) {
	for i, v := range vols {
		if v == nil {
			return nil, fmt.Errorf("archive: volume %d not parsed: %w", i, ErrMissingVolume)
		}
	}
	var members []Member
	vi, fi := 0, 0
	for vi < len(vols) {
		files := vols[vi].Files
		if fi >= len(files) {
			vi, fi = vi+1, 0
			continue
		}
		f := &files[fi]
		if f.SplitBefore {
			if vi == 0 {
				return nil, fmt.Errorf("archive: %q continues from a volume before the first one given: %w", f.Name, ErrMissingVolume)
			}
			return nil, fmt.Errorf("archive: volume %d: %q continues a file the previous volume did not split: %w", vi, f.Name, ErrCorrupt)
		}
		m := Member{
			Name:       f.Name,
			Size:       f.UnpackedSize,
			Stored:     f.Stored,
			Encrypted:  f.Encrypted,
			Encryption: f.Encryption,
			Solid:      f.Solid,
			Dir:        f.Dir,
			Method:     rarMethodName(vols[vi].Format, f),
		}
		addExtent := func(v int, rf *RARFile) {
			if rf.PackedSize > 0 {
				m.Extents = append(m.Extents, Extent{Volume: v, Offset: rf.DataOffset, Size: rf.PackedSize})
			}
		}
		addExtent(vi, f)
		cur, last := vi, f
		for last.SplitAfter {
			if cur == vi && fi != len(files)-1 || cur != vi && len(vols[cur].Files) != 1 {
				return nil, fmt.Errorf("archive: volume %d: split file %q is not the last file header: %w", cur, f.Name, ErrCorrupt)
			}
			next := cur + 1
			if next >= len(vols) {
				return nil, fmt.Errorf("archive: %q continues after volume %d: %w", f.Name, cur, ErrMissingVolume)
			}
			nv := vols[next]
			if pn, nn := vols[cur].Number, nv.Number; pn >= 0 && nn >= 0 && nn != pn+1 {
				if nn > pn+1 {
					return nil, fmt.Errorf("archive: volume number %d follows %d: %w", nn, pn, ErrMissingVolume)
				}
				return nil, fmt.Errorf("archive: volume number %d follows %d: %w", nn, pn, ErrCorrupt)
			}
			if len(nv.Files) == 0 || !nv.Files[0].SplitBefore || nv.Files[0].Name != f.Name {
				return nil, fmt.Errorf("archive: volume %d does not continue %q: %w", next, f.Name, ErrCorrupt)
			}
			c := &nv.Files[0]
			if f.Encrypted != c.Encrypted || (f.Encryption == nil) != (c.Encryption == nil) || f.Encryption != nil && (!bytes.Equal(f.Encryption.Key, c.Encryption.Key) || f.Encryption.IV != c.Encryption.IV) {
				return nil, fmt.Errorf("archive: split file encryption changes: %w", ErrCorrupt)
			}
			if m.Encryption != nil && m.Size >= 0 && c.UnpackedSize >= 0 && m.Size != c.UnpackedSize {
				return nil, fmt.Errorf("archive: encrypted split file size changes: %w", ErrCorrupt)
			}
			m.Stored = m.Stored && c.Stored
			m.Encrypted = m.Encrypted || c.Encrypted
			m.Solid = m.Solid || c.Solid
			if m.Size < 0 {
				m.Size = c.UnpackedSize
			}
			addExtent(next, c)
			cur, last = next, c
		}
		if m.Dir {
			m.Size = max(m.Size, 0)
		} else if m.Stored && (!m.Encrypted || m.Encryption != nil) {
			var sum int64
			for _, e := range m.Extents {
				if e.Size > math.MaxInt64-sum {
					return nil, fmt.Errorf("archive: stored file %q size overflows: %w", m.Name, ErrCorrupt)
				}
				sum += e.Size
			}
			if m.Size < 0 {
				if m.Encryption != nil {
					return nil, fmt.Errorf("archive: encrypted stored file has unknown plaintext size: %w", ErrUnsupported)
				}
				m.Size = sum
			} else if m.Encryption != nil {
				if m.Size > math.MaxInt64-15 || sum != (m.Size+15)&^15 {
					return nil, fmt.Errorf("archive: encrypted stored size mismatch: %w", ErrCorrupt)
				}
				e := *m.Encryption
				e.Size = m.Size
				m.Encryption = &e
			} else if sum != m.Size {
				return nil, fmt.Errorf("archive: stored file %q has %d data bytes, header says %d: %w", m.Name, sum, m.Size, ErrCorrupt)
			}
		}
		members = append(members, m)
		if cur == vi {
			fi++
		} else {
			vi, fi = cur, 1
		}
	}
	return members, nil
}

// ListRAR reads every volume (in order) with ReadRARVolume and composes the
// members. Extent volume indexes refer to vols.
func ListRAR(vols []Volume) ([]Member, error) {
	parsed := make([]*RARVolume, len(vols))
	for i, v := range vols {
		if v.R == nil {
			return nil, fmt.Errorf("archive: volume %d: %w", i, ErrMissingVolume)
		}
		rv, err := ReadRARVolume(v.R, v.Size, nil)
		if err != nil {
			return nil, fmt.Errorf("archive: volume %d: %w", i, err)
		}
		parsed[i] = rv
	}
	return ComposeRAR(parsed)
}
