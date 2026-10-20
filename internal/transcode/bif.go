package transcode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// BIF (Base Index Frame) is Roku's trickplay index format, which Plex
// serves as "media index files" at /library/parts/{id}/indexes/sd.
//
// Layout (all integers little-endian):
//
//	offset  size  field
//	0       8     magic: 89 'B' 'I' 'F' 0d 0a 1a 0a
//	8       4     version (0)
//	12      4     image count
//	16      4     timestamp multiplier (ms per timestamp unit; 1000 = seconds)
//	20      44    reserved, zero
//	64      ...   index: count+1 entries of (timestamp u32, offset u32);
//	              the last entry is the terminator (0xffffffff, total size)
//	...     ...   concatenated JPEG images at the indexed offsets
//
// An index may be sparse: entries carry explicit timestamps, so frames
// only need to exist for the intervals generated so far.

var bifMagic = []byte{0x89, 'B', 'I', 'F', 0x0d, 0x0a, 0x1a, 0x0a}

const (
	bifHeaderSize    = 64
	bifVersion       = 0
	bifMultiplier    = 1000 // timestamps are whole seconds
	bifTerminator    = 0xffffffff
	bifEntrySize     = 8
	errBIFFrameEmpty = "transcode: bif frame has no image data"
)

// BIFFrame is one thumbnail: the media time it shows and its JPEG bytes.
type BIFFrame struct {
	Time time.Duration
	JPEG []byte
}

// IsJPEG reports whether b starts with the JPEG SOI marker.
func IsJPEG(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff
}

// MarshalBIF encodes frames as a BIF file. Frames are sorted by time
// (stable, on a copy) and duplicate timestamps keep the first frame, so
// the output is deterministic for a given input order. At least one
// frame with image data is required.
func MarshalBIF(frames []BIFFrame) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteBIF(&buf, frames); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteBIF is MarshalBIF into w.
func WriteBIF(w io.Writer, frames []BIFFrame) error {
	frames = normalizeBIFFrames(frames)
	if len(frames) == 0 {
		return errors.New("transcode: bif needs at least one frame")
	}
	for _, f := range frames {
		if len(f.JPEG) == 0 {
			return errors.New(errBIFFrameEmpty)
		}
		if f.Time < 0 {
			return fmt.Errorf("transcode: bif frame at negative time %s", f.Time)
		}
	}
	imageStart := bifHeaderSize + (len(frames)+1)*bifEntrySize
	total := imageStart
	for _, f := range frames {
		total += len(f.JPEG)
	}

	hdr := make([]byte, bifHeaderSize)
	copy(hdr, bifMagic)
	binary.LittleEndian.PutUint32(hdr[8:], bifVersion)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(frames)))
	binary.LittleEndian.PutUint32(hdr[16:], bifMultiplier)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	entry := make([]byte, bifEntrySize)
	off := imageStart
	for _, f := range frames {
		binary.LittleEndian.PutUint32(entry[0:], uint32(f.Time.Milliseconds()/bifMultiplier))
		binary.LittleEndian.PutUint32(entry[4:], uint32(off))
		if _, err := w.Write(entry); err != nil {
			return err
		}
		off += len(f.JPEG)
	}
	binary.LittleEndian.PutUint32(entry[0:], bifTerminator)
	binary.LittleEndian.PutUint32(entry[4:], uint32(total))
	if _, err := w.Write(entry); err != nil {
		return err
	}
	for _, f := range frames {
		if _, err := w.Write(f.JPEG); err != nil {
			return err
		}
	}
	return nil
}

// normalizeBIFFrames returns a sorted copy with duplicate timestamps
// removed (first occurrence wins) and timestamps truncated to the BIF
// multiplier resolution, so index timestamps are strictly increasing.
func normalizeBIFFrames(in []BIFFrame) []BIFFrame {
	if len(in) == 0 {
		return nil
	}
	out := make([]BIFFrame, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	res := out[:0]
	var lastUnit int64 = -1
	for _, f := range out {
		unit := f.Time.Milliseconds() / bifMultiplier
		if unit == lastUnit {
			continue
		}
		lastUnit = unit
		res = append(res, f)
	}
	return res
}

// BIF is a parsed BIF file.
type BIF struct {
	Version    uint32
	Multiplier uint32 // ms per timestamp unit
	times      []time.Duration
	data       []byte
	offsets    []int // start of each image; last element is total size
}

// Len is the number of frames.
func (b *BIF) Len() int { return len(b.times) }

// Time returns the media time of frame i.
func (b *BIF) Time(i int) time.Duration { return b.times[i] }

// Frame returns the JPEG bytes of frame i (a sub-slice of the input).
func (b *BIF) Frame(i int) []byte { return b.data[b.offsets[i]:b.offsets[i+1]] }

// FrameAt returns the latest frame at or before at, if any.
func (b *BIF) FrameAt(at time.Duration) ([]byte, bool) {
	i := sort.Search(len(b.times), func(i int) bool { return b.times[i] > at }) - 1
	if i < 0 {
		return nil, false
	}
	return b.Frame(i), true
}

// ParseBIF parses a whole BIF file held in memory.
func ParseBIF(data []byte) (*BIF, error) {
	if len(data) < bifHeaderSize {
		return nil, fmt.Errorf("transcode: bif too short (%d bytes)", len(data))
	}
	if !bytes.Equal(data[:8], bifMagic) {
		return nil, errors.New("transcode: bif bad magic")
	}
	b := &BIF{data: data}
	b.Version = binary.LittleEndian.Uint32(data[8:])
	count := int(binary.LittleEndian.Uint32(data[12:]))
	b.Multiplier = binary.LittleEndian.Uint32(data[16:])
	if b.Multiplier == 0 {
		b.Multiplier = bifMultiplier // per spec, 0 means 1000
	}
	indexEnd := bifHeaderSize + (count+1)*bifEntrySize
	if count < 0 || indexEnd > len(data) {
		return nil, fmt.Errorf("transcode: bif index of %d frames exceeds %d bytes", count, len(data))
	}
	b.times = make([]time.Duration, count)
	b.offsets = make([]int, count+1)
	for i := 0; i <= count; i++ {
		e := data[bifHeaderSize+i*bifEntrySize:]
		ts := binary.LittleEndian.Uint32(e)
		off := int(binary.LittleEndian.Uint32(e[4:]))
		if off < indexEnd || off > len(data) {
			return nil, fmt.Errorf("transcode: bif frame %d offset %d out of bounds", i, off)
		}
		if i > 0 && off < b.offsets[i-1] {
			return nil, fmt.Errorf("transcode: bif frame %d offset goes backwards", i)
		}
		b.offsets[i] = off
		if i < count {
			if ts == bifTerminator {
				return nil, fmt.Errorf("transcode: bif terminator at frame %d of %d", i, count)
			}
			b.times[i] = time.Duration(int64(ts)*int64(b.Multiplier)) * time.Millisecond
		} else if ts != bifTerminator {
			return nil, errors.New("transcode: bif index has no terminator")
		}
	}
	return b, nil
}
