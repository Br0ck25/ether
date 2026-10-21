package transcode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// HLS fMP4 runs on the clock of the init segment players keep.
//
// frag_discont starts a run's decode times at its first packet's
// presentation time, and its init's edit list removes that packet's
// composition offset. A run starting on an open-GOP keyframe (whose offset
// exceeds the reorder delay) or on another release with a different
// reorder delay writes decode times and an edit both shifted by the
// difference. Its own init places it right, but players keep the first
// init they fetched (one EXT-X-MAP), where its video plays late while the
// audio stays in place.
//
// So the session keeps the edits of the first init it published in each
// init scope (its retained clock): a rendition from a playlist
// discontinuity, where a fresh EXT-X-MAP lets the player take a new init
// (a governor clamp, a ramp encode switching to copy, an RTX fallback). A later run whose edits differ gets its init
// edit rewritten to the retained one and every fragment's decode time
// moved by the difference, which keeps each sample's presentation time.
// Output that cannot be corrected exactly fails the run; it is never
// published.

// trackEdit is one track's ID, media timescale and edit shift
// (trackInit.EditShift, in that timescale). A player holding an init
// reads fragments by its track IDs and counts every fragment time (tfdt,
// durations, composition offsets) in its timescales, so within one scope
// only the edit may differ, and is corrected.
type trackEdit struct {
	id    uint32
	scale uint32
	shift int64
}

// continues reports why a run's track t cannot be played under the
// retained track ret (nil when it can, up to its edit).
func (ret trackEdit) continues(t trackEdit) error {
	if t.id != ret.id {
		return fmt.Errorf("fmp4: track %d where the retained init has track %d", t.id, ret.id)
	}
	if t.scale != ret.scale {
		return fmt.Errorf("fmp4: track %d counts in %d where the retained init counts in %d", t.id, t.scale, ret.scale)
	}
	return nil
}

// initEdits reads an init segment's edits by track handler.
func initEdits(b []byte) (map[string]trackEdit, []trackInit, error) {
	tracks, err := parseInitTracks(b)
	if err != nil {
		return nil, nil, err
	}
	edits := make(map[string]trackEdit, len(tracks))
	for _, t := range tracks {
		if t.EditBad {
			return nil, nil, fmt.Errorf("fmp4: unreadable edit list on track %d", t.TrackID)
		}
		if _, dup := edits[t.Handler]; dup {
			// Which retained track it continues would be a guess.
			return nil, nil, fmt.Errorf("fmp4: more than one %q track", t.Handler)
		}
		edits[t.Handler] = trackEdit{id: t.TrackID, scale: t.Timescale, shift: t.EditShift}
	}
	return edits, tracks, nil
}

// runEdits is a run's correction onto the retained clock, owned by the
// goroutine collecting its output.
type runEdits struct {
	ready bool
	// own are the run's edits as ffmpeg wrote them, before any rewrite.
	own map[string]trackEdit
	// delta is what each track's decode times move back by (track ID).
	delta map[uint32]int64
	// shifted lists the files already moved, so a repeated poll never
	// moves one twice.
	shifted map[string]bool
	// published: the run's init passed checkPublishedEdits.
	published bool
	// failed: the run's output could not be held to the clock.
	failed bool
}

// prepareRunEdits reads run r's init once and, when retained is set and
// differs, rewrites the init's edits onto it and records the decode time
// corrections. With no retained clock yet the run is left as written: its
// init, once published, becomes the retained clock.
func prepareRunEdits(r *run, retained map[string]trackEdit) error {
	if r.edits != nil && r.edits.ready {
		return nil
	}
	path := filepath.Join(r.spec.Dir, r.spec.InitName)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	own, tracks, err := initEdits(b)
	if err != nil {
		return err
	}
	re := &runEdits{own: own, shifted: map[string]bool{}}
	if retained != nil {
		re.delta = map[uint32]int64{}
		var patches []filePatch
		for _, t := range tracks {
			ret, ok := retained[t.Handler]
			if !ok {
				continue // a track the retained init lacks: no clock to keep
			}
			if err := ret.continues(own[t.Handler]); err != nil {
				return err
			}
			d := t.EditShift - ret.shift
			if d == 0 {
				continue
			}
			p, err := elstPatch(b, t.TrackID, d)
			if err != nil {
				return err
			}
			patches = append(patches, p)
			re.delta[t.TrackID] = d
		}
		if len(patches) > 0 {
			if err := rewriteFile(path, patches); err != nil {
				return err
			}
		}
	}
	re.ready = true
	r.edits = re
	return nil
}

// filePatch replaces len(b) bytes at off.
type filePatch struct {
	off int64
	b   []byte
}

// elstPatch moves the media edit of track id's edit list back by d: its
// shift becomes EditShift-d.
func elstPatch(b []byte, id uint32, d int64) (filePatch, error) {
	top, _ := readBoxes(b, 0, len(b))
	for _, moov := range top {
		if moov.typ != "moov" {
			continue
		}
		kids, _ := readBoxes(b, moov.start+moov.hdr, moov.end)
		for _, trak := range kids {
			if trak.typ != "trak" {
				continue
			}
			tkhd, ok := child(b, trak, "tkhd")
			if !ok || trackID(tkhd.payload(b)) != id {
				continue
			}
			elst, ok := path(b, trak, "edts", "elst")
			if !ok {
				return filePatch{}, fmt.Errorf("fmp4: track %d has no edit list to move", id)
			}
			p := elst.payload(b)
			at, version, _, err := mediaEdit(p)
			if err != nil {
				return filePatch{}, err
			}
			off := int64(elst.start + elst.hdr + at)
			if version == 1 {
				v := int64(binary.BigEndian.Uint64(p[at:])) - d
				if v < 0 {
					return filePatch{}, fmt.Errorf("fmp4: track %d edit would start at %d", id, v)
				}
				return filePatch{off: off, b: binary.BigEndian.AppendUint64(nil, uint64(v))}, nil
			}
			v := int64(int32(binary.BigEndian.Uint32(p[at:]))) - d
			if v < 0 || v > math.MaxInt32 {
				return filePatch{}, fmt.Errorf("fmp4: track %d edit %d does not fit its box", id, v)
			}
			return filePatch{off: off, b: binary.BigEndian.AppendUint32(nil, uint32(v))}, nil
		}
	}
	return filePatch{}, fmt.Errorf("fmp4: no track %d in init", id)
}

func trackID(tkhd []byte) uint32 {
	switch {
	case len(tkhd) >= 24 && tkhd[0] == 1:
		return binary.BigEndian.Uint32(tkhd[20:])
	case len(tkhd) >= 16:
		return binary.BigEndian.Uint32(tkhd[12:])
	}
	return 0
}

// shiftDecodeTimes moves every fragment's decode time (tfdt) of the tracks
// in delta back by their delta, validating the whole file before replacing
// it. A corrected time must be non-negative and fit its box.
func shiftDecodeTimes(path string, delta map[uint32]int64) error {
	if len(delta) == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	var patches []filePatch
	size := st.Size()
	var hdr [16]byte
	for off := int64(0); off < size; {
		if size-off < 8 {
			f.Close()
			return errShortBox
		}
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			f.Close()
			return err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:]))
		typ := string(hdr[4:8])
		switch boxSize {
		case 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				f.Close()
				return err
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:]))
		case 0:
			boxSize = size - off
		}
		if boxSize < 8 || boxSize > size-off {
			f.Close()
			return fmt.Errorf("fmp4: bad %q box in %s", typ, filepath.Base(path))
		}
		if typ == "moof" {
			if boxSize > maxMoofSize {
				f.Close()
				return fmt.Errorf("fmp4: moof of %d bytes", boxSize)
			}
			b := make([]byte, boxSize)
			if _, err := f.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
				f.Close()
				return err
			}
			ps, err := tfdtPatches(b, delta)
			if err != nil {
				f.Close()
				return fmt.Errorf("%w in %s", err, filepath.Base(path))
			}
			for _, p := range ps {
				p.off += off
				patches = append(patches, p)
			}
		}
		off += boxSize
	}
	f.Close()
	return rewriteFile(path, patches)
}

// tfdtPatches are the corrected tfdt values of one moof (offsets in b).
func tfdtPatches(b []byte, delta map[uint32]int64) ([]filePatch, error) {
	top, err := readBoxes(b, 0, len(b))
	if err != nil || len(top) != 1 {
		return nil, errShortBox
	}
	trafs, err := readBoxes(b, top[0].start+top[0].hdr, top[0].end)
	if err != nil {
		return nil, err
	}
	var out []filePatch
	for _, traf := range trafs {
		if traf.typ != "traf" {
			continue
		}
		tfhd, ok := child(b, traf, "tfhd")
		if !ok || len(tfhd.payload(b)) < 8 {
			return nil, errors.New("fmp4: traf without tfhd")
		}
		id := binary.BigEndian.Uint32(tfhd.payload(b)[4:])
		d, ok := delta[id]
		if !ok {
			continue
		}
		tfdt, ok := child(b, traf, "tfdt")
		if !ok {
			return nil, fmt.Errorf("fmp4: track %d fragment without tfdt", id)
		}
		p := tfdt.payload(b)
		off := int64(tfdt.start + tfdt.hdr + 4)
		switch {
		case len(p) >= 12 && p[0] == 1:
			v := int64(binary.BigEndian.Uint64(p[4:])) - d
			if v < 0 {
				return nil, fmt.Errorf("fmp4: track %d decode time would be %d", id, v)
			}
			out = append(out, filePatch{off: off, b: binary.BigEndian.AppendUint64(nil, uint64(v))})
		case len(p) >= 8 && p[0] == 0:
			v := int64(binary.BigEndian.Uint32(p[4:])) - d
			if v < 0 || v > math.MaxUint32 {
				return nil, fmt.Errorf("fmp4: track %d decode time %d does not fit its box", id, v)
			}
			out = append(out, filePatch{off: off, b: binary.BigEndian.AppendUint32(nil, uint32(v))})
		default:
			return nil, fmt.Errorf("fmp4: bad tfdt on track %d", id)
		}
	}
	return out, nil
}

// rewriteFile replaces path with a copy carrying patches, renamed into
// place, so readers see the old or the new file whole.
func rewriteFile(path string, patches []filePatch) error {
	if len(patches) == 0 {
		return nil
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	tmp := path + ".edit"
	out, err := os.Create(tmp)
	if err != nil {
		in.Close()
		return err
	}
	fail := func(err error) error {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	_, err = io.Copy(out, in)
	// Closed before the rename: Windows cannot replace a file this
	// process still has open (Go opens without FILE_SHARE_DELETE).
	if cerr := in.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	for _, p := range patches {
		if _, err := out.WriteAt(p.b, p.off); err != nil {
			return fail(err)
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// checkPublishedEdits holds a run's init, as it is about to be published
// for a track, to the track's retained clock: every track the retained
// init has must present on it exactly. With none yet, the init's edits
// become the retained clock.
func checkPublishedEdits(retained map[string]trackEdit, initPath string) (map[string]trackEdit, error) {
	b, err := os.ReadFile(initPath)
	if err != nil {
		return nil, err
	}
	edits, _, err := initEdits(b)
	if err != nil {
		return nil, err
	}
	if retained == nil {
		return edits, nil
	}
	for h, e := range edits {
		ret, ok := retained[h]
		if !ok {
			continue
		}
		if err := ret.continues(e); err != nil {
			return nil, err
		}
		if e.shift != ret.shift {
			return nil, fmt.Errorf("fmp4: %s edit %d against the retained %d", h, e.shift, ret.shift)
		}
	}
	return retained, nil
}

// clockScope is the init scope a run's segments are played under: its
// rendition, from the last playlist discontinuity at or before the run's
// first segment (runs end at mode changes, so they stay inside one).
type clockScope struct{ track, from int }

// clockLocked is run r's init scope on track t and its retained clock
// (nil none yet).
func (s *Session) clockLocked(r *run, t int) (clockScope, map[string]trackEdit) {
	from := max(r.startSeg, 0)
	for from > 0 && !s.discontBeforeLocked(t, from) {
		from--
	}
	c := clockScope{track: t, from: from}
	return c, s.fmp4Edits[c]
}

// heldToClock reports whether run r's output is held to a retained clock:
// ffmpeg's HLS fMP4 runs. Native RTX runs keep their own clock.
func (s *Session) heldToClock(r *run) bool {
	return s.container == ContainerFMP4 && !s.isDASH() && !r.rtx
}

func runTrack(s *Session, r *run) int {
	if r.track < 0 || r.track >= len(s.done) {
		return 0
	}
	return r.track
}

// moveOntoRetained holds chunk path of run r to its retained clock, once.
// It is called by the run's collector without s.mu.
func (s *Session) moveOntoRetained(r *run, path string) error {
	if !s.heldToClock(r) {
		return nil
	}
	if r.edits == nil || !r.edits.ready {
		s.mu.Lock()
		_, retained := s.clockLocked(r, runTrack(s, r))
		s.mu.Unlock()
		if err := prepareRunEdits(r, retained); err != nil {
			return err
		}
	}
	return r.edits.shift(path)
}

// moveOntoRetainedLocked is moveOntoRetained for collectors holding s.mu.
func (s *Session) moveOntoRetainedLocked(r *run, t int, path string) error {
	if !s.heldToClock(r) {
		return nil
	}
	if r.edits == nil || !r.edits.ready {
		_, retained := s.clockLocked(r, t)
		if err := prepareRunEdits(r, retained); err != nil {
			return err
		}
	}
	return r.edits.shift(path)
}

// shift moves path's decode times by the run's correction unless it has
// already.
func (re *runEdits) shift(path string) error {
	if len(re.delta) == 0 || re.shifted[path] {
		return nil
	}
	if err := shiftDecodeTimes(path, re.delta); err != nil {
		return err
	}
	re.shifted[path] = true
	return nil
}

// holdInitEditsLocked checks run r's init against its retained clock on
// track t before it is published, capturing the clock from the first
// init published in the scope.
func (s *Session) holdInitEditsLocked(r *run, t int) error {
	if !s.heldToClock(r) || (r.edits != nil && r.edits.published) {
		return nil
	}
	c, retained := s.clockLocked(r, t)
	edits, err := checkPublishedEdits(retained, filepath.Join(r.spec.Dir, r.spec.InitName))
	if err != nil {
		return err
	}
	if s.fmp4Edits == nil {
		s.fmp4Edits = map[clockScope]map[string]trackEdit{}
	}
	s.fmp4Edits[c] = edits
	if r.edits == nil {
		r.edits = &runEdits{shifted: map[string]bool{}}
	}
	r.edits.published = true
	return nil
}

// failRunOutputLocked ends run r, whose output cannot be published, as a
// failed run (retried or failed over like any other).
func (s *Session) failRunOutputLocked(r *run, err error) {
	s.log.Warn("HLS run output unusable", "run", r.seq, "err", err)
	if r.edits == nil {
		r.edits = &runEdits{shifted: map[string]bool{}}
	}
	r.edits.failed = true
	r.stderr.add(err.Error())
	r.proc = failedProc{Process: r.proc, err: err}
	r.proc.Stop()
}
