package transcode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"strings"
	"time"
)

// Minimal ISO-BMFF (fragmented MP4) reading for DASH sessions: the track
// and codec of an init segment, and the timing of the media fragments in a
// chunk ffmpeg's dash muxer wrote, so chunks can be regrouped into
// fixed-duration segments.

var errShortBox = errors.New("fmp4: truncated box")

// mp4Box is one box inside a buffer: b[start:end] is the whole box and
// b[start+hdr:end] its payload.
type mp4Box struct {
	typ        string
	start, end int
	hdr        int
}

func (x mp4Box) payload(b []byte) []byte { return b[x.start+x.hdr : x.end] }

// readBoxes lists the boxes in b[off:end].
func readBoxes(b []byte, off, end int) ([]mp4Box, error) {
	var out []mp4Box
	for off < end {
		if end-off < 8 {
			return out, errShortBox
		}
		size := int64(binary.BigEndian.Uint32(b[off:]))
		typ := string(b[off+4 : off+8])
		hdr := 8
		switch size {
		case 1:
			if end-off < 16 {
				return out, errShortBox
			}
			size = int64(binary.BigEndian.Uint64(b[off+8:]))
			hdr = 16
		case 0:
			size = int64(end - off)
		}
		if size < int64(hdr) || size > int64(end-off) {
			return out, fmt.Errorf("fmp4: bad %q box size %d", typ, size)
		}
		out = append(out, mp4Box{typ: typ, start: off, end: off + int(size), hdr: hdr})
		off += int(size)
	}
	return out, nil
}

// child finds the first box of a type among parent's children.
func child(b []byte, parent mp4Box, typ string) (mp4Box, bool) {
	kids, _ := readBoxes(b, parent.start+parent.hdr, parent.end)
	for _, k := range kids {
		if k.typ == typ {
			return k, true
		}
	}
	return mp4Box{}, false
}

// path walks nested boxes: path(b, moov, "trak", "mdia", "mdhd").
func path(b []byte, from mp4Box, types ...string) (mp4Box, bool) {
	cur := from
	for _, t := range types {
		next, ok := child(b, cur, t)
		if !ok {
			return mp4Box{}, false
		}
		cur = next
	}
	return cur, true
}

// trackInit describes the single track of an fMP4 initialization segment.
type trackInit struct {
	TrackID         uint32
	Timescale       uint32
	DefaultDuration uint32 // trex default_sample_duration
	Handler         string // "vide", "soun"
	SampleEntry     string // "avc1", "hvc1", "mp4a", "Opus"...
	Codecs          string // RFC 6381 codecs parameter
	Width, Height   int
	SampleRate      int
	Channels        int
	// EditShift maps composition times to presentation times (both in
	// Timescale): a sample presents at its composition time minus this.
	// It is the edit list's media_time less any leading empty edit.
	EditShift int64
	// EditBad is set when the track has an edit list this reader cannot
	// map (truncated, or no edit that maps media).
	EditBad bool
}

// parseInit reads the first track of an init segment (ftyp + moov).
func parseInit(b []byte) (trackInit, error) {
	var ti trackInit
	top, err := readBoxes(b, 0, len(b))
	if err != nil && len(top) == 0 {
		return ti, err
	}
	var moov mp4Box
	found := false
	for _, x := range top {
		if x.typ == "moov" {
			moov, found = x, true
			break
		}
	}
	if !found {
		return ti, errors.New("fmp4: init segment has no moov")
	}
	trak, ok := child(b, moov, "trak")
	if !ok {
		return ti, errors.New("fmp4: init segment has no track")
	}
	return parseTrak(b, moov, trak)
}

// parseInitTracks reads every track of an init segment (an HLS fMP4 init
// carries video and audio in one moov).
func parseInitTracks(b []byte) ([]trackInit, error) {
	top, err := readBoxes(b, 0, len(b))
	if err != nil && len(top) == 0 {
		return nil, err
	}
	var out []trackInit
	for _, x := range top {
		if x.typ != "moov" {
			continue
		}
		kids, _ := readBoxes(b, x.start+x.hdr, x.end)
		for _, k := range kids {
			if k.typ != "trak" {
				continue
			}
			if ti, err := parseTrak(b, x, k); err == nil {
				out = append(out, ti)
			}
		}
		break
	}
	if len(out) == 0 {
		return nil, errors.New("fmp4: init segment has no readable track")
	}
	return out, nil
}

// parseTrak describes one trak box of a moov.
func parseTrak(b []byte, moov, trak mp4Box) (trackInit, error) {
	var ti trackInit
	if tkhd, ok := child(b, trak, "tkhd"); ok {
		p := tkhd.payload(b)
		if len(p) >= 24 {
			if p[0] == 1 {
				ti.TrackID = binary.BigEndian.Uint32(p[20:])
			} else {
				ti.TrackID = binary.BigEndian.Uint32(p[12:])
			}
		}
	}
	if mdhd, ok := path(b, trak, "mdia", "mdhd"); ok {
		p := mdhd.payload(b)
		switch {
		case len(p) >= 24 && p[0] == 1:
			ti.Timescale = binary.BigEndian.Uint32(p[20:])
		case len(p) >= 16:
			ti.Timescale = binary.BigEndian.Uint32(p[12:])
		}
	}
	if ti.Timescale == 0 {
		return ti, errors.New("fmp4: no media timescale")
	}
	if hdlr, ok := path(b, trak, "mdia", "hdlr"); ok {
		if p := hdlr.payload(b); len(p) >= 12 {
			ti.Handler = string(p[8:12])
		}
	}
	if elst, ok := path(b, trak, "edts", "elst"); ok {
		var movieScale uint32
		if mvhd, ok := child(b, moov, "mvhd"); ok {
			movieScale, _ = fullBoxTime(mvhd.payload(b))
		}
		shift, err := editShift(elst.payload(b), movieScale, ti.Timescale)
		ti.EditShift, ti.EditBad = shift, err != nil
	}
	if mvex, ok := child(b, moov, "mvex"); ok {
		kids, _ := readBoxes(b, mvex.start+mvex.hdr, mvex.end)
		for _, k := range kids {
			if k.typ != "trex" {
				continue
			}
			p := k.payload(b)
			if len(p) >= 24 && (ti.TrackID == 0 || binary.BigEndian.Uint32(p[4:]) == ti.TrackID) {
				ti.DefaultDuration = binary.BigEndian.Uint32(p[12:])
				break
			}
		}
	}
	stsd, ok := path(b, trak, "mdia", "minf", "stbl", "stsd")
	if !ok {
		return ti, errors.New("fmp4: no sample description")
	}
	// stsd: full box header (4) + entry count (4), then sample entries.
	if stsd.end-(stsd.start+stsd.hdr) < 16 {
		return ti, errors.New("fmp4: empty sample description")
	}
	entries, _ := readBoxes(b, stsd.start+stsd.hdr+8, stsd.end)
	if len(entries) == 0 {
		return ti, errors.New("fmp4: no sample entry")
	}
	describeSampleEntry(b, entries[0], &ti)
	return ti, nil
}

// editShift reads an elst payload: the media_time of the first edit that
// maps media, less the empty edits (durations in the movie timescale)
// that delay it, in the media timescale. ffmpeg writes one edit whose
// media_time is the first sample's composition offset.
func editShift(p []byte, movieScale, mediaScale uint32) (int64, error) {
	at, version, empty, err := mediaEdit(p)
	if err != nil {
		return 0, err
	}
	media := int64(int32(binary.BigEndian.Uint32(p[at:])))
	if version == 1 {
		media = int64(binary.BigEndian.Uint64(p[at:]))
	}
	if empty > 0 {
		if movieScale == 0 {
			return 0, errors.New("fmp4: empty edit without a movie timescale")
		}
		media -= int64(empty * uint64(mediaScale) / uint64(movieScale))
	}
	return media, nil
}

// mediaEdit finds the media_time field of an elst payload's first edit
// that maps media: its offset in p, the box version (field width 4 or 8)
// and the summed durations of the empty edits before it.
func mediaEdit(p []byte) (at int, version byte, empty uint64, err error) {
	if len(p) < 8 || p[0] > 1 {
		return 0, 0, 0, errors.New("fmp4: bad elst")
	}
	version, at = p[0], 8
	n, size := uint64(binary.BigEndian.Uint32(p[4:])), uint64(12)
	if version == 1 {
		size = 20
	}
	if uint64(len(p)) < 8+n*size {
		return 0, 0, 0, errors.New("fmp4: truncated elst")
	}
	for i := uint64(0); i < n; i++ {
		var seg uint64
		var media int64
		if version == 1 {
			if len(p) < at+20 {
				return 0, 0, 0, errors.New("fmp4: truncated elst")
			}
			seg, media = binary.BigEndian.Uint64(p[at:]), int64(binary.BigEndian.Uint64(p[at+8:]))
			at += 20
		} else {
			if len(p) < at+12 {
				return 0, 0, 0, errors.New("fmp4: truncated elst")
			}
			seg, media = uint64(binary.BigEndian.Uint32(p[at:])), int64(int32(binary.BigEndian.Uint32(p[at+4:])))
			at += 12
		}
		if media >= 0 {
			if version == 1 {
				return at - 12, version, empty, nil // media_time follows the 8-byte duration
			}
			return at - 8, version, empty, nil
		}
		empty += seg
	}
	return 0, 0, 0, errors.New("fmp4: elst maps no media")
}

// describeSampleEntry fills the codec, size and audio fields from a sample
// entry box.
func describeSampleEntry(b []byte, e mp4Box, ti *trackInit) {
	ti.SampleEntry = e.typ
	p := e.payload(b)
	childrenAt := -1
	switch ti.Handler {
	case "vide":
		// 6 reserved + 2 data_reference_index + 16 pre_defined/reserved,
		// then width and height, then 50 more bytes.
		if len(p) >= 78 {
			ti.Width = int(binary.BigEndian.Uint16(p[24:]))
			ti.Height = int(binary.BigEndian.Uint16(p[26:]))
			childrenAt = 78
		}
	case "soun":
		if len(p) >= 28 {
			version := binary.BigEndian.Uint16(p[8:])
			ti.Channels = int(binary.BigEndian.Uint16(p[16:]))
			ti.SampleRate = int(binary.BigEndian.Uint32(p[24:]) >> 16)
			childrenAt = 28
			switch version {
			case 1:
				childrenAt += 16
			case 2:
				childrenAt += 36
			}
		}
	}
	var kids []mp4Box
	if childrenAt >= 0 && e.start+e.hdr+childrenAt <= e.end {
		kids, _ = readBoxes(b, e.start+e.hdr+childrenAt, e.end)
	}
	find := func(typ string) []byte {
		for _, k := range kids {
			if k.typ == typ {
				return k.payload(b)
			}
		}
		return nil
	}
	ti.Codecs = codecsString(e.typ, find)
	if ti.Handler == "soun" {
		if d := find("dOps"); len(d) >= 2 && d[1] > 0 {
			ti.Channels = int(d[1]) // Opus: the real output channel count
		}
	}
}

// codecsString builds the RFC 6381 codecs parameter for a sample entry.
func codecsString(entry string, find func(string) []byte) string {
	switch entry {
	case "avc1", "avc3":
		if c := find("avcC"); len(c) >= 4 {
			return fmt.Sprintf("%s.%02x%02x%02x", entry, c[1], c[2], c[3])
		}
	case "hvc1", "hev1", "dvh1", "dvhe":
		if c := find("hvcC"); len(c) >= 13 {
			return entry + "." + hevcCodecs(c)
		}
	case "av01":
		if c := find("av1C"); len(c) >= 3 {
			profile := c[1] >> 5
			level := c[1] & 0x1f
			tier := "M"
			if c[2]&0x80 != 0 {
				tier = "H"
			}
			depth := 8
			if c[2]&0x40 != 0 {
				depth = 10
				if profile == 2 && c[2]&0x20 != 0 {
					depth = 12
				}
			}
			return fmt.Sprintf("av01.%d.%02d%s.%02d", profile, level, tier, depth)
		}
	case "vp09":
		if c := find("vpcC"); len(c) >= 7 {
			// Full box: version/flags, profile, level, bitDepth<<4|...
			return fmt.Sprintf("vp09.%02d.%02d.%02d", c[4], c[5], c[6]>>4)
		}
	case "mp4a":
		return mp4aCodecs(find("esds"))
	case "ac-3":
		return "ac-3"
	case "ec-3":
		return "ec-3"
	case "Opus":
		return "opus"
	case "fLaC":
		return "flac"
	case "alac":
		return "alac"
	}
	return strings.TrimSpace(entry)
}

// hevcCodecs is the "P.C.Tl.B" part of an HEVC codecs string from hvcC.
func hevcCodecs(c []byte) string {
	space := []string{"", "A", "B", "C"}[c[1]>>6]
	tier := "L"
	if c[1]&0x20 != 0 {
		tier = "H"
	}
	profile := c[1] & 0x1f
	compat := bits.Reverse32(binary.BigEndian.Uint32(c[2:]))
	level := c[12]
	constraints := c[6:12]
	last := len(constraints)
	for last > 0 && constraints[last-1] == 0 {
		last--
	}
	s := fmt.Sprintf("%s%d.%X.%s%d", space, profile, compat, tier, level)
	for _, x := range constraints[:last] {
		s += fmt.Sprintf(".%X", x)
	}
	return s
}

// mp4aCodecs reads the object type from an esds box: "mp4a.40.2" for AAC
// LC, "mp4a.40.5" for HE-AAC, "mp4a.6b" for MP3.
func mp4aCodecs(esds []byte) string {
	if len(esds) < 4 {
		return "mp4a.40.2"
	}
	d := esds[4:] // skip version/flags
	// descriptor reads tag and length (with 0x80 continuation bytes).
	descriptor := func(d []byte) (tag byte, body []byte, rest []byte, ok bool) {
		if len(d) < 2 {
			return 0, nil, nil, false
		}
		tag = d[0]
		n, i := 0, 1
		for ; i < len(d) && i <= 4; i++ {
			n = n<<7 | int(d[i]&0x7f)
			if d[i]&0x80 == 0 {
				i++
				break
			}
		}
		if i+n > len(d) {
			return 0, nil, nil, false
		}
		return tag, d[i : i+n], d[i+n:], true
	}
	tag, es, _, ok := descriptor(d)
	if !ok || tag != 0x03 || len(es) < 3 {
		return "mp4a.40.2"
	}
	flags := es[2]
	es = es[3:]
	if flags&0x80 != 0 && len(es) >= 2 {
		es = es[2:]
	}
	if flags&0x40 != 0 && len(es) >= 1 {
		if 1+int(es[0]) > len(es) {
			return "mp4a.40.2"
		}
		es = es[1+int(es[0]):]
	}
	if flags&0x20 != 0 && len(es) >= 2 {
		es = es[2:]
	}
	tag, dc, _, ok := descriptor(es)
	if !ok || tag != 0x04 || len(dc) < 13 {
		return "mp4a.40.2"
	}
	oti := dc[0]
	if oti != 0x40 {
		return fmt.Sprintf("mp4a.%02x", oti)
	}
	tag, dsi, _, ok := descriptor(dc[13:])
	if !ok || tag != 0x05 || len(dsi) < 1 {
		return "mp4a.40.2"
	}
	aot := int(dsi[0] >> 3)
	if aot == 31 && len(dsi) >= 2 {
		aot = 32 + int((dsi[0]&0x07)<<3|dsi[1]>>5)
	}
	if aot == 0 {
		aot = 2
	}
	return fmt.Sprintf("mp4a.40.%d", aot)
}

// chunkPart is a byte range of a chunk file to copy into a segment, or
// replacement bytes for it.
type chunkPart struct {
	off, n int64
	data   []byte // when set, written instead of the file range
}

// fmp4Chunk is one media chunk written by ffmpeg's dash muxer: its decode
// time span and the boxes to copy into a segment (styp and sidx dropped,
// negative decode times clamped to zero).
type fmp4Chunk struct {
	path       string
	start, end time.Duration
	parts      []chunkPart
	size       int64
}

// maxMoofSize bounds the moof boxes read into memory.
const maxMoofSize = 16 << 20

// scanChunk reads a chunk's box layout and fragment timing without loading
// its media data.
func scanChunk(path string, ti trackInit) (fmp4Chunk, error) {
	c := fmp4Chunk{path: path}
	f, err := openMuxOutput(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return c, err
	}
	size := st.Size()
	var first, last int64 = -1, -1
	var hdr [16]byte
	for off := int64(0); off < size; {
		if size-off < 8 {
			return c, errShortBox
		}
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return c, err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:]))
		typ := string(hdr[4:8])
		switch boxSize {
		case 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return c, err
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:]))
		case 0:
			boxSize = size - off
		}
		if boxSize < 8 || off+boxSize > size {
			return c, fmt.Errorf("fmp4: bad %q box in %s", typ, path)
		}
		switch typ {
		case "styp", "sidx", "ftyp", "moov", "prft", "free", "skip":
			// Not copied: segments get their own init and styp.
		case "moof":
			if boxSize > maxMoofSize {
				return c, fmt.Errorf("fmp4: moof of %d bytes", boxSize)
			}
			b := make([]byte, boxSize)
			if _, err := f.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
				return c, err
			}
			s, e, err := moofTiming(b, ti.DefaultDuration)
			if err != nil {
				return c, err
			}
			if first < 0 || s < first {
				first = s
			}
			if e > last {
				last = e
			}
			c.parts = append(c.parts, chunkPart{off: off, n: boxSize, data: b})
		default: // mdat and anything else in a media segment
			c.parts = append(c.parts, chunkPart{off: off, n: boxSize})
		}
		off += boxSize
	}
	if first < 0 {
		return c, fmt.Errorf("fmp4: no fragment in %s", path)
	}
	c.start = ticksToDuration(first, ti.Timescale)
	c.end = ticksToDuration(last, ti.Timescale)
	for _, p := range c.parts {
		c.size += p.n
	}
	return c, nil
}

func ticksToDuration(t int64, timescale uint32) time.Duration {
	if timescale == 0 {
		return 0
	}
	return time.Duration(float64(t) / float64(timescale) * float64(time.Second))
}

// moofTiming returns the decode time span of a moof's track fragments. A
// negative baseMediaDecodeTime (encoder priming before zero) is rewritten
// to 0 in b, so players never see a wrapped 64-bit time.
func moofTiming(b []byte, trexDefault uint32) (start, end int64, err error) {
	top, err := readBoxes(b, 0, len(b))
	if err != nil || len(top) == 0 || top[0].typ != "moof" {
		return 0, 0, errors.New("fmp4: not a moof")
	}
	start, end = -1, -1
	trafs, _ := readBoxes(b, top[0].start+top[0].hdr, top[0].end)
	for _, traf := range trafs {
		if traf.typ != "traf" {
			continue
		}
		kids, _ := readBoxes(b, traf.start+traf.hdr, traf.end)
		def := trexDefault
		var base int64
		haveBase := false
		var dur int64
		for _, k := range kids {
			p := k.payload(b)
			switch k.typ {
			case "tfhd":
				if len(p) < 8 {
					continue
				}
				flags := uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
				o := 8
				if flags&0x01 != 0 {
					o += 8
				}
				if flags&0x02 != 0 {
					o += 4
				}
				if flags&0x08 != 0 && len(p) >= o+4 {
					def = binary.BigEndian.Uint32(p[o:])
				}
			case "tfdt":
				if len(p) < 8 {
					continue
				}
				if p[0] == 1 && len(p) >= 12 {
					base = int64(binary.BigEndian.Uint64(p[4:]))
					if base < 0 {
						base = 0
						for i := 4; i < 12; i++ {
							p[i] = 0
						}
					}
				} else {
					base = int64(binary.BigEndian.Uint32(p[4:]))
				}
				haveBase = true
			case "trun":
				dur += trunDuration(p, def)
			}
		}
		if !haveBase {
			continue
		}
		if start < 0 || base < start {
			start = base
		}
		if base+dur > end {
			end = base + dur
		}
	}
	if start < 0 {
		return 0, 0, errors.New("fmp4: moof without tfdt")
	}
	return start, end, nil
}

// trunDuration sums a track run's sample durations.
func trunDuration(p []byte, def uint32) int64 {
	if len(p) < 8 {
		return 0
	}
	flags := uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
	count := int(binary.BigEndian.Uint32(p[4:]))
	o := 8
	if flags&0x01 != 0 {
		o += 4
	}
	if flags&0x04 != 0 {
		o += 4
	}
	if flags&0x100 == 0 {
		return int64(count) * int64(def)
	}
	per := 4
	for _, f := range []uint32{0x200, 0x400, 0x800} {
		if flags&f != 0 {
			per += 4
		}
	}
	var total int64
	for i := 0; i < count && o+4 <= len(p); i++ {
		total += int64(binary.BigEndian.Uint32(p[o:]))
		o += per
	}
	return total
}

// trunFirstOffset reads a track run's sample count and its first sample's
// composition offset: unsigned in version 0, signed in version 1, 0 when
// the run carries none. ok is false when the payload is shorter than its
// samples.
func trunFirstOffset(p []byte) (cto int64, n uint32, ok bool) {
	if len(p) < 8 {
		return 0, 0, false
	}
	flags := uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
	n = binary.BigEndian.Uint32(p[4:])
	o, per := 8, 0
	for _, f := range []uint32{0x001, 0x004} { // data offset, first sample flags
		if flags&f != 0 {
			o += 4
		}
	}
	for _, f := range []uint32{0x100, 0x200, 0x400, 0x800} { // duration, size, flags, offset
		if flags&f != 0 {
			per += 4
		}
	}
	if uint64(len(p)) < uint64(o)+uint64(n)*uint64(per) {
		return 0, n, false
	}
	if n == 0 || flags&0x800 == 0 {
		return 0, n, true
	}
	v := binary.BigEndian.Uint32(p[o+per-4:])
	if p[0] == 0 {
		return int64(v), n, true
	}
	return int64(int32(v)), n, true
}

// stypBox is the segment type box at the start of every DASH segment.
var stypBox = []byte{
	0, 0, 0, 24, 's', 't', 'y', 'p',
	'm', 's', 'd', 'h', 0, 0, 0, 0,
	'm', 's', 'd', 'h', 'm', 's', 'i', 'x',
}

// writeSegment writes a self-initializing DASH media segment: the init
// segment, styp, then every chunk's fragments. Carrying the init in each
// segment (as PMS does) lets players follow codec changes between ffmpeg
// runs, e.g. after a failover to another release.
func writeSegment(w io.Writer, init []byte, chunks []fmp4Chunk) error {
	if _, err := w.Write(init); err != nil {
		return err
	}
	if _, err := w.Write(stypBox); err != nil {
		return err
	}
	for _, c := range chunks {
		if err := copyChunk(w, c); err != nil {
			return err
		}
	}
	return nil
}

func copyChunk(w io.Writer, c fmp4Chunk) error {
	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, p := range c.parts {
		if p.data != nil {
			if _, err := w.Write(p.data); err != nil {
				return err
			}
			continue
		}
		if _, err := io.Copy(w, io.NewSectionReader(f, p.off, p.n)); err != nil {
			return err
		}
	}
	return nil
}
