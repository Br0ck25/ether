package transcode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Chunked HLS runs.
//
// A session whose plan was built from the source's keyframe index
// (Timeline.Keyframed) cannot let ffmpeg's segmenters choose the cuts:
// they cut at the first keyframe at or after start+k*segment_length, with
// k counting the cuts made, and a restart (seek, failover, rung switch)
// resets k. With 2.5s keyframes and 4s segments that makes 5,5,2.5,...
// segments where the plan lists 5,5,5,...; with 10s keyframes it makes
// fewer segments than the playlist advertises and the tail 404s. A copy
// restart also begins at the keyframe before its seek point.
//
// Such runs instead cut at every keyframe into a chunk directory, and the
// session assigns each chunk to the plan segment its first timestamp falls
// in (the slotter shared with DASH), concatenates the chunks of a segment
// and drops the keyframe interval a remux restart reads before its seek
// point. Encoded runs force their keyframes at the plan's boundaries.
//
// Copies without a keyframe index are chunked the same way onto their
// nominal plan: every segment starts on a keyframe, so it decodes on its
// own after a seek; a segment inside a GOP longer than a segment repeats
// that GOP (an empty Cluster for Matroska).

// chunkCutFor is the chunked muxer segment length for a session segment
// length: a fraction of a segment, at most half a second.
func chunkCutFor(segDur time.Duration) time.Duration {
	c := segDur / 8
	if c > 500*time.Millisecond {
		c = 500 * time.Millisecond
	}
	if c < 40*time.Millisecond {
		c = 40 * time.Millisecond
	}
	return c
}

// maxForcedKeySegments bounds the boundaries one chunked encode forces
// keyframes at (one command line argument); the run chains a new one
// there.
const maxForcedKeySegments = 400

// tsMuxDelay is what ffmpeg's MPEG-TS muxer adds to every timestamp
// (2 x the default 0.7s -muxdelay) unless mpegts_copyts is set.
const tsMuxDelay = 1400 * time.Millisecond

// chunkRun is one chunked run's bookkeeping, owned by its watch goroutine.
type chunkRun struct {
	dir       string // ffmpeg writes chunks here
	container string
	initPath  string        // fMP4: the run's init segment
	startAt   time.Duration // media start of the run's first segment
	next      int           // playlist entries consumed
	sl        *slotter
	vTrack    uint32 // fMP4 video track and timescale, once read
	vScale    uint32
}

// chunkStart is the media time a chunk starts at (its first video
// timestamp), in the run's -ss clock.
func (cr *chunkRun) chunkStart(path string) (time.Duration, error) {
	switch cr.container {
	case ContainerFMP4:
		if cr.vScale == 0 {
			b, err := os.ReadFile(cr.initPath)
			if err != nil {
				return 0, err
			}
			tracks, err := parseInitTracks(b)
			if err != nil {
				return 0, err
			}
			cr.vTrack, cr.vScale = tracks[0].TrackID, tracks[0].Timescale
			for _, t := range tracks {
				if t.Handler == "vide" {
					cr.vTrack, cr.vScale = t.TrackID, t.Timescale
					break
				}
			}
		}
		return fmp4ChunkStart(path, cr.vTrack, cr.vScale)
	case ContainerMatroska:
		return mkvChunkStart(path)
	default:
		return tsChunkStart(path)
	}
}

// tsChunkStart reads the first video PES timestamp of an MPEG-TS chunk
// (the first PES of any stream when there is no video PES).
func tsChunkStart(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	const pkt = 188
	buf := make([]byte, 512*pkt)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	buf = buf[:n]
	first := int64(-1)
	for off := 0; off+pkt <= len(buf); {
		p := buf[off : off+pkt]
		if p[0] != 0x47 {
			off++ // resync
			continue
		}
		off += pkt
		if p[1]&0x40 == 0 { // not a payload unit start
			continue
		}
		afc := (p[3] >> 4) & 3
		o := 4
		if afc&2 != 0 {
			o += 1 + int(p[4])
		}
		if afc&1 == 0 || o+14 > pkt {
			continue
		}
		pes := p[o:]
		if pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[7]&0x80 == 0 {
			continue
		}
		t := pes[9:14]
		pts := int64(t[0]>>1&7)<<30 | int64(t[1])<<22 | int64(t[2]>>1)<<15 | int64(t[3])<<7 | int64(t[4]>>1)
		if id := pes[3]; id >= 0xE0 && id <= 0xEF {
			return ptsToMedia(pts), nil
		}
		if first < 0 {
			first = pts
		}
	}
	if first >= 0 {
		return ptsToMedia(first), nil
	}
	return 0, fmt.Errorf("mpegts: no timestamp in %s", filepath.Base(path))
}

func ptsToMedia(pts int64) time.Duration {
	return time.Duration(pts)*time.Second/90000 - tsMuxDelay
}

// fmp4ChunkStart reads the earliest decode time of track (any track when
// 0) in a fragmented MP4 chunk.
func fmp4ChunkStart(path string, track, timescale uint32) (time.Duration, error) {
	if timescale == 0 {
		return 0, errors.New("fmp4: no timescale")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()
	var hdr [16]byte
	for off := int64(0); off+8 <= size; {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return 0, err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:]))
		typ := string(hdr[4:8])
		switch boxSize {
		case 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return 0, err
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:]))
		case 0:
			boxSize = size - off
		}
		if boxSize < 8 || boxSize > size-off {
			return 0, fmt.Errorf("fmp4: bad %q box in %s", typ, filepath.Base(path))
		}
		if typ != "moof" {
			off += boxSize
			continue
		}
		if boxSize > maxMoofSize {
			return 0, fmt.Errorf("fmp4: moof of %d bytes", boxSize)
		}
		b := make([]byte, boxSize)
		if _, err := f.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		best := int64(-1)
		top, _ := readBoxes(b, 0, len(b))
		if len(top) == 0 {
			return 0, errShortBox
		}
		trafs, _ := readBoxes(b, top[0].start+top[0].hdr, top[0].end)
		for _, traf := range trafs {
			if traf.typ != "traf" {
				continue
			}
			var id uint32
			base := int64(-1)
			kids, _ := readBoxes(b, traf.start+traf.hdr, traf.end)
			for _, k := range kids {
				p := k.payload(b)
				switch {
				case k.typ == "tfhd" && len(p) >= 8:
					id = binary.BigEndian.Uint32(p[4:])
				case k.typ == "tfdt" && len(p) >= 8:
					if p[0] == 1 && len(p) >= 12 {
						base = int64(binary.BigEndian.Uint64(p[4:]))
					} else {
						base = int64(binary.BigEndian.Uint32(p[4:]))
					}
				}
			}
			if base >= 0 && (track == 0 || id == track) && (best < 0 || base < best) {
				best = base
			}
		}
		if best < 0 {
			return 0, fmt.Errorf("fmp4: no decode time for track %d in %s", track, filepath.Base(path))
		}
		return ticksToDuration(best, timescale), nil
	}
	return 0, fmt.Errorf("fmp4: no fragment in %s", filepath.Base(path))
}

// mkvChunkStart reads the Timestamp of the first Cluster of a segmented
// Matroska chunk (millisecond TimestampScale, as ffmpeg writes), less the
// output origin matroskaHLSMuxArgs adds.
func mkvChunkStart(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	buf = buf[:n]
	id, l := chunkVint(buf, true)
	if l == 0 || id != 0x1F43B675 {
		return 0, fmt.Errorf("matroska: %s does not start with a Cluster", filepath.Base(path))
	}
	off := l
	_, sl := chunkVint(buf[off:], false)
	if sl == 0 {
		return 0, errors.New("matroska: bad Cluster size")
	}
	off += sl
	for off < len(buf) {
		cid, il := chunkVint(buf[off:], true)
		if il == 0 {
			break
		}
		size, szl := chunkVint(buf[off+il:], false)
		if szl == 0 {
			break
		}
		body := off + il + szl
		if cid == 0xE7 {
			if size == 0 || size > 8 || body+int(size) > len(buf) {
				break
			}
			var ts uint64
			for _, c := range buf[body : body+int(size)] {
				ts = ts<<8 | uint64(c)
			}
			if ts > uint64(1<<53) {
				break
			}
			return time.Duration(ts)*time.Millisecond - matroskaOrigin, nil
		}
		if cid == 0xA3 || cid == 0xA0 || size > uint64(len(buf)) {
			break // blocks before the timestamp: not ffmpeg's layout
		}
		off = body + int(size)
	}
	return 0, fmt.Errorf("matroska: no Cluster Timestamp in %s", filepath.Base(path))
}

// emptyClusterSize is the size of emptyCluster's output.
const emptyClusterSize = 15

// emptyCluster is a Matroska Cluster holding only its Timestamp (ms).
func emptyCluster(at time.Duration) []byte {
	ms := uint64(max(at, 0) / time.Millisecond)
	b := []byte{0x1F, 0x43, 0xB6, 0x75, 0x8A, 0xE7, 0x88}
	for i := 7; i >= 0; i-- {
		b = append(b, byte(ms>>(8*i)))
	}
	return b
}

// chunkVint reads an EBML variable-length integer: an element ID (marker
// kept) or a size (marker stripped). It returns its length, 0 if invalid.
func chunkVint(b []byte, keepMarker bool) (uint64, int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	l := 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		l++
	}
	if l > 8 || len(b) < l {
		return 0, 0
	}
	v := uint64(b[0])
	if !keepMarker {
		v &= uint64(0xFF >> l)
	}
	for _, c := range b[1:l] {
		v = v<<8 | uint64(c)
	}
	return v, l
}

// assembleChunks makes dst one segment from consecutive chunk files: the
// first chunk renamed, the others appended (fMP4: without their own styp
// and sidx boxes, which describe a single chunk). The chunk files are
// consumed.
func assembleChunks(dst, container string, chunks []fmp4Chunk, remove func(string)) error {
	if len(chunks) == 0 {
		return errors.New("transcode: segment without chunks")
	}
	if len(chunks) == 1 {
		return os.Rename(chunks[0].path, dst)
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	for i, c := range chunks {
		if container == ContainerFMP4 {
			err = copyMP4Boxes(out, c.path, i == 0)
		} else {
			err = appendFile(out, c.path)
		}
		if err != nil {
			return fail(err)
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for _, c := range chunks {
		remove(c.path)
	}
	return nil
}

func appendFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// copyMP4Boxes copies a media chunk's top-level boxes, skipping sidx and
// (unless keepStyp) styp.
func copyMP4Boxes(w io.Writer, path string, keepStyp bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	var hdr [16]byte
	for off := int64(0); off < size; {
		if size-off < 8 {
			return errShortBox
		}
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:]))
		typ := string(hdr[4:8])
		switch boxSize {
		case 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return err
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:]))
		case 0:
			boxSize = size - off
		}
		if boxSize < 8 || boxSize > size-off {
			return fmt.Errorf("fmp4: bad %q box in %s", typ, filepath.Base(path))
		}
		if typ != "sidx" && (typ != "styp" || keepStyp) {
			if _, err := io.Copy(w, io.NewSectionReader(f, off, boxSize)); err != nil {
				return err
			}
		}
		off += boxSize
	}
	return nil
}

// isPreroll reports whether a run's first chunk c is media an earlier
// segment holds: it ends by the run's first segment, or its GOP starts in
// an earlier segment that is already published (a restart continuing
// playback, where repeating the GOP would run the stream backwards).
// Otherwise it is kept, so the run's first segment starts on its keyframe.
// It takes s.mu.
func (s *Session) isPreroll(r *run, cr *chunkRun, c fmp4Chunk) bool {
	if c.end <= cr.startAt+cr.sl.tol {
		return true
	}
	if c.start >= cr.startAt-cr.sl.tol {
		return false
	}
	slot := cr.sl.slotOf(c.start)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := r.track
	if t < 0 || t >= len(s.done) {
		t = 0
	}
	return slot < r.startSeg && s.done[t][slot]
}

// chunkSeg is one segment a chunked run finished: assembled into file, or
// (alias >= 0) a copy of an earlier segment whose GOP spans this one too.
type chunkSeg struct {
	n     int
	file  string
	alias int
}

// collectChunks takes the chunks a chunked run listed since the last poll,
// regroups them into the plan's segments and publishes the finished ones.
func (s *Session) collectChunks(r *run, exited bool) {
	s.mu.Lock()
	skip := s.closed || (r.stopped && s.cur != r)
	stopped := r.stopped
	s.mu.Unlock()
	if skip {
		return
	}
	cr := r.chunks
	var outs []slotOut
	p, err := readFFmpegPlaylist(filepath.Join(r.spec.Dir, r.spec.Playlist))
	if err == nil {
		for ; cr.next < len(p.entries); cr.next++ {
			e := p.entries[cr.next]
			path := filepath.Join(cr.dir, e.name)
			start, err := cr.chunkStart(path)
			if err != nil {
				// Chunks are renamed into place once complete, so this is
				// a muxer change or a full disk; skip rather than stall.
				s.log.Warn("unreadable HLS chunk", "run", r.seq, "chunk", e.name, "err", err)
				s.removals.remove(path)
				continue
			}
			c := fmp4Chunk{path: path, start: start, end: start + e.dur}
			if !cr.sl.started && s.isPreroll(r, cr, c) {
				// The keyframe interval before the seek point a remux
				// restart reads first: an earlier segment's media.
				s.removals.remove(path)
				continue
			}
			outs = append(outs, cr.sl.add(c)...)
		}
	}
	if exited {
		outs = append(outs, cr.sl.flush(!stopped && r.proc.Err() == nil)...)
	}
	if len(outs) == 0 {
		return
	}
	ext := segmentExt(s.container)
	segs := make([]chunkSeg, 0, len(outs))
	for _, o := range outs {
		if o.alias >= 0 {
			segs = append(segs, chunkSeg{n: o.n, alias: o.alias})
			continue
		}
		file := filepath.Join(cr.dir, fmt.Sprintf("seg-%05d.%s", o.n, ext))
		if err := assembleChunks(file, s.container, o.chunks, s.removals.remove); err != nil {
			s.log.Warn("assemble HLS segment", "run", r.seq, "segment", o.n, "err", err)
			continue
		}
		segs = append(segs, chunkSeg{n: o.n, file: file, alias: -1})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (r.stopped && s.cur != r) {
		return // the chunk directory goes with the run
	}
	t := r.track
	if t < 0 || t >= len(s.done) {
		t = 0
	}
	s.noteInitLocked(r, t)
	changed := false
	done := s.done[t]
	for _, sg := range segs {
		n := sg.n
		if n < r.startSeg || (r.planEnd > 0 && n >= r.planEnd) {
			// Past this run's mode boundary: the next run redoes it in its
			// mode.
			if sg.file != "" {
				s.removals.remove(sg.file)
			}
			continue
		}
		dst := s.trackSegPath(t, n)
		switch {
		case done[n]:
			// An earlier run's copy is as good; the run still got here.
			if sg.file != "" {
				s.removals.remove(sg.file)
			}
		case sg.alias >= 0 && s.container == ContainerMatroska:
			// Plex's Matroska player joins fragments into one stream: a
			// repeated GOP would run its clock backwards. An empty Cluster
			// keeps the segment and the stream valid; a request that does
			// not continue the stream (a seek) is served the GOP instead
			// (Session.decodableMKVLocked).
			if os.WriteFile(dst, emptyCluster(s.segStart(n)+matroskaOrigin), 0o644) != nil {
				continue
			}
		case sg.alias >= 0:
			// Players that place media by timestamp get the GOP covering
			// the segment again (as DASH segments do).
			if !done[sg.alias] || linkOrCopy(s.trackSegPath(t, sg.alias), dst) != nil {
				continue
			}
		default:
			if os.Rename(sg.file, dst) != nil {
				s.removals.remove(sg.file)
				continue
			}
		}
		done[n] = true
		r.produced++
		if n > r.maxDone {
			r.maxDone = n
		}
		if t == 0 && sg.alias < 0 {
			// Not an alias: its GOP's size over one segment would look
			// like a bitrate spike to the governor.
			s.noteSegSizeLocked(n, dst)
		}
		changed = true
	}
	if changed {
		s.progressedLocked(r)
	}
}
