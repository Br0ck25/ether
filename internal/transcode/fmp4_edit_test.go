package transcode

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// retained1328 is the clock of a run that started on an IDR: 2 frames of
// reorder delay at 24fps, AAC without an edit.
func retained1328() map[string]trackEdit {
	return map[string]trackEdit{"vide": {id: 1, scale: 16000, shift: 1328}, "soun": {id: 2, scale: 48000}}
}

// videoTfdt reads the video (track 1) tfdt of a chunk's first moof.
func videoTfdt(t *testing.T, b []byte) (video, audio uint64) {
	t.Helper()
	top, _ := readBoxes(b, 0, len(b))
	for _, x := range top {
		if x.typ != "moof" {
			continue
		}
		trafs, _ := readBoxes(b, x.start+x.hdr, x.end)
		for _, traf := range trafs {
			if traf.typ != "traf" {
				continue
			}
			tfhd, _ := child(b, traf, "tfhd")
			tfdt, _ := child(b, traf, "tfdt")
			v := binary.BigEndian.Uint64(tfdt.payload(b)[4:])
			if binary.BigEndian.Uint32(tfhd.payload(b)[4:]) == 1 {
				video = v
			} else {
				audio = v
			}
		}
		return video, audio
	}
	t.Fatal("no moof")
	return 0, 0
}

func editRun(t *testing.T, init []byte) *run {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), init, 0o644); err != nil {
		t.Fatal(err)
	}
	return &run{spec: runSpec{Dir: dir, InitName: "init.mp4"}}
}

func writeFile(t *testing.T, path string, b []byte) string {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRestartRunMovesOntoRetainedClock(t *testing.T) {
	// A restart on a CRA: its init's edit is the CRA's offset (3328), its
	// decode times start at the CRA's presentation time (2.000s).
	r := editRun(t, openGOPInit(3328))
	initPath := filepath.Join(r.spec.Dir, "init.mp4")
	if err := prepareRunEdits(r, retained1328()); err != nil {
		t.Fatal(err)
	}
	if r.edits.own["vide"] != (trackEdit{id: 1, scale: 16000, shift: 3328}) || r.edits.delta[1] != 2000 || len(r.edits.delta) != 1 {
		t.Fatalf("edits %+v", r.edits)
	}
	tracks, err := parseInitTracks(readFile(t, initPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range tracks {
		if want := map[string]int64{"vide": 1328, "soun": 0}[tr.Handler]; tr.EditShift != want || tr.EditBad {
			t.Errorf("%s edit %d, want %d", tr.Handler, tr.EditShift, want)
		}
	}
	if _, err := os.Stat(initPath + ".edit"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
	chunk := writeFile(t, filepath.Join(r.spec.Dir, "c0.m4s"), openGOPChunk(32000, 0, 3328))
	_, audio0 := videoTfdt(t, readFile(t, chunk))
	for i := 0; i < 3; i++ { // repeated polls move it once
		if err := r.edits.shift(chunk); err != nil {
			t.Fatal(err)
		}
	}
	video, audio := videoTfdt(t, readFile(t, chunk))
	if video != 30000 || audio != audio0 {
		t.Errorf("tfdt video %d audio %d, want 30000 %d", video, audio, audio0)
	}
	// Presentation is unchanged: 30000 + 3328 - 1328 = 2.000s.
	cr := &chunkRun{container: ContainerFMP4, initPath: initPath}
	if got, err := cr.chunkStart(chunk); err != nil || got != 2*time.Second {
		t.Errorf("chunk start %v %v", got, err)
	}
	// Preparing again neither rereads nor rewrites the init.
	before := readFile(t, initPath)
	if err := prepareRunEdits(r, retained1328()); err != nil || !bytes.Equal(before, readFile(t, initPath)) {
		t.Errorf("second prepare: %v", err)
	}
	if got, err := checkPublishedEdits(retained1328(), initPath); err != nil || got["vide"] != (trackEdit{id: 1, scale: 16000, shift: 1328}) {
		t.Errorf("published check %v %v", got, err)
	}
}

func TestRetainedClockRefusesOtherTracks(t *testing.T) {
	// The fragments of a run whose video counts in another timescale, or
	// sits on another track ID, cannot be read under the retained init
	// whatever its edit says: refused before anything is rewritten.
	for _, tc := range []struct {
		name     string
		init     []byte
		retained map[string]trackEdit
	}{
		{"timescale 90000 for 16000", openGOPInitScale(18720, 90000), retained1328()},
		{"same timescale and edit, other track", openGOPInit(1328), map[string]trackEdit{"vide": {id: 3, scale: 16000, shift: 1328}}},
		{"audio at another rate", openGOPInit(1328), map[string]trackEdit{"vide": {id: 1, scale: 16000, shift: 1328}, "soun": {id: 2, scale: 44100}}},
	} {
		r := editRun(t, tc.init)
		initPath := filepath.Join(r.spec.Dir, "init.mp4")
		if err := prepareRunEdits(r, tc.retained); err == nil {
			t.Errorf("%s: prepared", tc.name)
		}
		if !bytes.Equal(tc.init, readFile(t, initPath)) || r.edits != nil {
			t.Errorf("%s: refused run changed the init or the run", tc.name)
		}
		if _, err := checkPublishedEdits(tc.retained, initPath); err == nil {
			t.Errorf("%s: published", tc.name)
		}
	}
}

func TestRetainedClockRefusals(t *testing.T) {
	// Decode time that would go negative: refused, chunk untouched.
	r := editRun(t, openGOPInit(3328))
	if err := prepareRunEdits(r, retained1328()); err != nil {
		t.Fatal(err)
	}
	early := openGOPChunk(1000, 0, 3328)
	chunk := writeFile(t, filepath.Join(r.spec.Dir, "early.m4s"), early)
	if err := r.edits.shift(chunk); err == nil {
		t.Error("negative decode time: no error")
	}
	if !bytes.Equal(early, readFile(t, chunk)) || r.edits.shifted[chunk] {
		t.Error("refused chunk was changed")
	}
	// All or nothing: a valid moof before an invalid one stays as it was.
	two := append(openGOPChunk(32000, 0, 3328), openGOPChunk(1000, 0, 3328)...)
	chunk = writeFile(t, filepath.Join(r.spec.Dir, "two.m4s"), two)
	if err := r.edits.shift(chunk); err == nil || !bytes.Equal(two, readFile(t, chunk)) {
		t.Errorf("partly correctable chunk: %v", err)
	}
	// A version 0 tfdt pushed past 32 bits.
	chunk = writeFile(t, filepath.Join(r.spec.Dir, "v0.m4s"), openGOPChunkTfdt(mkFullBox("tfdt", 0, 0, u32(0xFFFFF000)), 0, 3328))
	if err := shiftDecodeTimes(chunk, map[uint32]int64{1: -0x2000}); err == nil {
		t.Error("v0 overflow: no error")
	}
	if err := shiftDecodeTimes(chunk, map[uint32]int64{1: 0x1000}); err != nil {
		t.Errorf("v0 in range: %v", err)
	}
	// A track fragment the correction applies to without a tfdt.
	chunk = writeFile(t, filepath.Join(r.spec.Dir, "notfdt.m4s"), openGOPChunkTfdt(nil, 0, 3328))
	if err := shiftDecodeTimes(chunk, map[uint32]int64{1: 2000}); err == nil {
		t.Error("no tfdt: no error")
	}

	// An edit that would start before the media.
	r = editRun(t, openGOPInit(1328))
	init := readFile(t, filepath.Join(r.spec.Dir, "init.mp4"))
	if err := prepareRunEdits(r, map[string]trackEdit{"vide": {id: 1, scale: 16000, shift: -6672}}); err == nil {
		t.Error("negative edit: no error")
	}
	if !bytes.Equal(init, readFile(t, filepath.Join(r.spec.Dir, "init.mp4"))) {
		t.Error("refused init was changed")
	}
	// Malformed edit list, and two tracks of one kind: no guessing.
	bad := openGOPInitBoxes(16000, mkFullBox("elst", 0, 0, u32(2), u32(0), u32(3328), u32(0x10000)), 1)
	if _, _, err := initEdits(bad); err == nil {
		t.Error("truncated elst: no error")
	}
	if _, err := checkPublishedEdits(nil, writeFile(t, filepath.Join(t.TempDir(), "bad.mp4"), bad)); err == nil {
		t.Error("truncated elst published")
	}
	r = editRun(t, bad)
	if err := prepareRunEdits(r, retained1328()); err == nil {
		t.Error("truncated elst corrected")
	}
	cr := &chunkRun{container: ContainerFMP4, initPath: filepath.Join(r.spec.Dir, "init.mp4")}
	if _, err := cr.chunkStart(writeFile(t, filepath.Join(r.spec.Dir, "c.m4s"), openGOPChunk(32000, 0, 3328))); err == nil {
		t.Error("chunk start against a truncated video elst: no error")
	}
	if _, _, err := initEdits(openGOPInitBoxes(16000, mkFullBox("elst", 0, 0, u32(1), u32(0), u32(1328), u32(0x10000)), 2)); err == nil {
		t.Error("two video tracks: no error")
	}
	// Published init off the retained clock.
	if _, err := checkPublishedEdits(retained1328(), writeFile(t, filepath.Join(t.TempDir(), "i.mp4"), openGOPInit(3328))); err == nil {
		t.Error("init off the clock published")
	}
	// A track the retained init lacks is not held to it.
	if _, err := checkPublishedEdits(map[string]trackEdit{"soun": {id: 2, scale: 48000}}, writeFile(t, filepath.Join(t.TempDir(), "j.mp4"), openGOPInit(3328))); err != nil {
		t.Errorf("new track kind: %v", err)
	}
}

// stopProc is a run process that has exited, recording Stop.
type stopProc struct{ stopped int }

func (p *stopProc) Done() <-chan struct{} { c := make(chan struct{}); close(c); return c }
func (p *stopProc) Err() error            { return nil }
func (p *stopProc) Stop()                 { p.stopped++ }
func (p *stopProc) Pause() error          { return nil }
func (p *stopProc) Resume() error         { return nil }

// pollSession is a non-chunked HLS fMP4 session whose current run wrote
// init (as init.mp4) and lists the segments in segs (name -> bytes).
func pollSession(t *testing.T, init []byte) (*Session, *run, *stopProc, string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "init.mp4"), init)
	p := &stopProc{}
	r := &run{spec: runSpec{Dir: dir, InitName: "init.mp4", Playlist: "run.m3u8"}, proc: p, stderr: newTailBuffer(4096),
		planEnd: -1, started: time.Now()}
	s := &Session{m: &Manager{}, dir: dir, container: ContainerFMP4, done: []map[int]bool{{}}, log: testLogger(t),
		changed: make(chan struct{}), cur: r}
	return s, r, p, filepath.Join(dir, "run.m3u8")
}

func listSegments(t *testing.T, playlist string, n int) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n")
	for i := 0; i < n; i++ {
		b.WriteString("#EXTINF:4.0,\n")
		b.WriteString(segmentFile(i, "m4s") + "\n")
	}
	writeFile(t, playlist, b.Bytes())
}

func TestPollHoldsFMP4RunsToRetainedClock(t *testing.T) {
	t.Run("first published init is the clock", func(t *testing.T) {
		s, r, _, pl := pollSession(t, openGOPInit(1328))
		seg := openGOPChunk(30000, 0, 3328)
		writeFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")), seg)
		listSegments(t, pl, 1)
		s.poll(r, pl, false)
		if !s.done[0][0] || s.initFileLocked(0) != "init.mp4" || s.fmp4Edits[clockScope{}]["vide"] != (trackEdit{id: 1, scale: 16000, shift: 1328}) {
			t.Fatalf("done %v init %q clock %v", s.done[0], s.initFileLocked(0), s.fmp4Edits)
		}
		if !bytes.Equal(seg, readFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")))) {
			t.Error("the clock's own run was rewritten")
		}
	})
	t.Run("restart corrected once across polls", func(t *testing.T) {
		s, r, p, pl := pollSession(t, openGOPInit(3328))
		s.fmp4Edits = map[clockScope]map[string]trackEdit{{}: retained1328()}
		writeFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")), openGOPChunk(32000, 0, 3328))
		listSegments(t, pl, 1)
		for i := 0; i < 3; i++ {
			s.poll(r, pl, false)
		}
		writeFile(t, filepath.Join(s.dir, segmentFile(1, "m4s")), openGOPChunk(48000, 0, 3328))
		listSegments(t, pl, 2)
		for i := 0; i < 3; i++ {
			s.poll(r, pl, false)
		}
		for n, want := range []uint64{30000, 46000} {
			if v, _ := videoTfdt(t, readFile(t, filepath.Join(s.dir, segmentFile(n, "m4s")))); v != want || !s.done[0][n] {
				t.Errorf("segment %d tfdt %d done %v, want %d", n, v, s.done[0][n], want)
			}
		}
		if _, err := checkPublishedEdits(retained1328(), filepath.Join(s.dir, "init.mp4")); err != nil || p.stopped != 0 {
			t.Errorf("published init: %v, stops %d", err, p.stopped)
		}
	})
	t.Run("uncorrectable segment is never published", func(t *testing.T) {
		s, r, p, pl := pollSession(t, openGOPInit(3328))
		s.fmp4Edits = map[clockScope]map[string]trackEdit{{}: retained1328()}
		path := writeFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")), openGOPChunk(1000, 0, 3328))
		listSegments(t, pl, 1)
		s.poll(r, pl, false)
		s.poll(r, pl, false)
		if s.done[0][0] || s.initFileLocked(0) != "" || p.stopped != 1 || r.proc.Err() == nil {
			t.Errorf("done %v init %q stops %d err %v", s.done[0], s.initFileLocked(0), p.stopped, r.proc.Err())
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("refused segment left in place")
		}
	})
	t.Run("a discontinuity starts a new clock", func(t *testing.T) {
		// A fresh EXT-X-MAP at segment 2 (here an RTX fallback; also a
		// governor clamp or a ramp encode switching to copy): the run
		// there is not held to the clock before it.
		// Its video even counts in another timescale.
		s, r, p, pl := pollSession(t, openGOPInitScale(18720, 90000))
		s.fmp4Edits = map[clockScope]map[string]trackEdit{{}: retained1328()}
		s.rtxDiscontinuities = map[int]bool{2: true}
		r.startSeg = 4
		seg := openGOPChunk(450000, 0, 18720)
		writeFile(t, filepath.Join(s.dir, segmentFile(4, "m4s")), seg)
		listSegments(t, pl, 5)
		s.poll(r, pl, false)
		if !s.done[0][4] || p.stopped != 0 || s.fmp4Edits[clockScope{0, 2}]["vide"] != (trackEdit{id: 1, scale: 90000, shift: 18720}) {
			t.Errorf("done %v stops %d clocks %v", s.done[0], p.stopped, s.fmp4Edits)
		}
		if !bytes.Equal(seg, readFile(t, filepath.Join(s.dir, segmentFile(4, "m4s")))) {
			t.Error("segment after the discontinuity was moved")
		}
	})
	t.Run("native RTX runs keep their clock", func(t *testing.T) {
		s, r, p, pl := pollSession(t, openGOPInit(3328))
		r.rtx = true
		s.fmp4Edits = map[clockScope]map[string]trackEdit{{}: retained1328()}
		seg := openGOPChunk(1000, 0, 3328)
		writeFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")), seg)
		listSegments(t, pl, 1)
		s.poll(r, pl, false)
		if !s.done[0][0] || p.stopped != 0 || !bytes.Equal(seg, readFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")))) {
			t.Errorf("RTX run held: done %v stops %d", s.done[0], p.stopped)
		}
	})
	t.Run("clock captured after the run prepared", func(t *testing.T) {
		// The run found no clock, then another run's init was published.
		s, r, p, pl := pollSession(t, openGOPInit(3328))
		if err := prepareRunEdits(r, nil); err != nil {
			t.Fatal(err)
		}
		s.fmp4Edits = map[clockScope]map[string]trackEdit{{}: retained1328()}
		writeFile(t, filepath.Join(s.dir, segmentFile(0, "m4s")), openGOPChunk(32000, 0, 3328))
		listSegments(t, pl, 1)
		s.poll(r, pl, false)
		if s.done[0][0] || s.initFileLocked(0) != "" || p.stopped != 1 {
			t.Errorf("off-clock run published: done %v init %q stops %d", s.done[0], s.initFileLocked(0), p.stopped)
		}
	})
}
