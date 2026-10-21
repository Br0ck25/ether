package transcode

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func everyKeyframe(span, every time.Duration) []core.KeyframePoint {
	var kfs []core.KeyframePoint
	for t := time.Duration(0); t < span; t += every {
		kfs = append(kfs, core.KeyframePoint{Offset: int64(t / time.Millisecond), Time: t})
	}
	return kfs
}

func boundaries(tl *Timeline) []float64 {
	var out []float64
	for n := 0; n <= tl.Total(); n++ {
		out = append(out, tl.Start(n).Seconds())
	}
	return out
}

func TestTimelineSnapsToDistantKeyframes(t *testing.T) {
	for _, tc := range []struct {
		every time.Duration
		want  []float64
	}{
		// Every boundary is the first keyframe at or after last+4s, however
		// far: the old plan gave up past one segment of drift and listed
		// 4s boundaries a copy can never cut at.
		{10 * time.Second, []float64{0, 10, 20, 30, 40, 50, 60}},
		{2500 * time.Millisecond, []float64{0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60}},
	} {
		tl := buildTimeline(time.Minute, 4*time.Second, timelineOptions{
			steadyMode: ModeCopy, snap: true, keyframes: everyKeyframe(time.Minute, tc.every)})
		if got := boundaries(tl); fmt.Sprint(got) != fmt.Sprint(tc.want) || !tl.Keyframed() {
			t.Errorf("keyframes every %v: boundaries %v (keyframed %v), want %v", tc.every, got, tl.Keyframed(), tc.want)
		}
		if tl.TargetDuration() < tl.Dur(1) {
			t.Errorf("target duration %v below segment %v", tl.TargetDuration(), tl.Dur(1))
		}
	}
	// The index stops (truncated) or skips more than a few segments: past
	// that the boundaries are nominal, and regrouping cuts at the real
	// keyframes there.
	tl := buildTimeline(time.Minute, 4*time.Second, timelineOptions{
		steadyMode: ModeCopy, snap: true, keyframes: everyKeyframe(30*time.Second, 10*time.Second)})
	if got := boundaries(tl); fmt.Sprint(got) != "[0 10 20 24 28 32 36 40 44 48 52 56 60]" {
		t.Errorf("index ending at 30s: %v", got)
	}
	gap := []core.KeyframePoint{{Time: 0}, {Time: 5 * time.Second}, {Time: 45 * time.Second}, {Time: 50 * time.Second}}
	tl = buildTimeline(time.Minute, 4*time.Second, timelineOptions{steadyMode: ModeCopy, snap: true, keyframes: gap})
	if got := boundaries(tl); fmt.Sprint(got) != "[0 5 9 13 17 21 25 29 45 50 60]" {
		t.Errorf("index with a 40s gap: %v", got)
	}
	// A fast-start ramp keeps its short segments until copy can begin.
	tl = buildTimeline(time.Minute, 4*time.Second, timelineOptions{steadyMode: ModeCopy, snap: true,
		fastStart: true, rampSegs: 2, rampDur: 2 * time.Second, keyframes: everyKeyframe(time.Minute, 9*time.Second)})
	if got := boundaries(tl); fmt.Sprint(got) != "[0 2 4 6 8 9 18 27 36 45 54 60]" || tl.SwitchSeg != 5 ||
		tl.ModeAt(4) != ModeFastStart || tl.ModeAt(5) != ModeCopy {
		t.Errorf("ramp to a 9s keyframe: %v switch %d", got, tl.SwitchSeg)
	}
	if buildTimeline(time.Minute, 4*time.Second, timelineOptions{steadyMode: ModeCopy}).Keyframed() {
		t.Error("nominal plan reports keyframed")
	}
}

func TestTSChunkStart(t *testing.T) {
	dir := t.TempDir()
	pat := make([]byte, 188) // PAT: unit start, not a PES
	pat[0], pat[1], pat[3], pat[5] = 0x47, 0x40, 0x10, 0x00
	audio := testTSChunk(7*time.Second, "audio")
	audio[3+4] = 0xC0 // stream id: audio
	video := testTSChunk(9500*time.Millisecond, "video")
	// Give the video packet an adaptation field (PCR) before its PES.
	withAF := make([]byte, 188)
	copy(withAF, video[:4])
	withAF[3] = 0x30
	withAF[4], withAF[5] = 7, 0x10
	copy(withAF[12:], video[4:180])
	write := func(name string, parts ...[]byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Join(parts, nil), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got, err := tsChunkStart(write("v.ts", pat, audio, withAF)); err != nil || got != 9500*time.Millisecond {
		t.Errorf("video PTS: %v %v", got, err)
	}
	if got, err := tsChunkStart(write("a.ts", []byte{1, 2, 3}, pat, audio)); err != nil || got != 7*time.Second {
		t.Errorf("audio-only PTS after junk: %v %v", got, err)
	}
	if _, err := tsChunkStart(write("none.ts", pat)); err == nil {
		t.Error("no PES: no error")
	}
	if _, err := tsChunkStart(write("short.ts", video[:100])); err == nil {
		t.Error("truncated packet: no error")
	}
}

func TestMatroskaChunkStart(t *testing.T) {
	dir := t.TempDir()
	cluster := func(kids ...[]byte) []byte {
		body := bytes.Join(kids, nil)
		return append([]byte{0x1F, 0x43, 0xB6, 0x75, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, body...)
	}
	crc := []byte{0xBF, 0x84, 1, 2, 3, 4}
	ts := []byte{0xE7, 0x83, 0x00, 0x75, 0x4F} // 30031 ms
	block := []byte{0xA3, 0x81, 0x00}
	for _, tc := range []struct {
		b    []byte
		want time.Duration
		ok   bool
	}{
		{cluster(crc, ts, block), 30031*time.Millisecond - matroskaOrigin, true},
		{cluster(ts), 30031*time.Millisecond - matroskaOrigin, true},
		{cluster(block, ts), 0, false},
		{[]byte{0x1A, 0x45, 0xDF, 0xA3, 0x80}, 0, false},
		{cluster([]byte{0xE7, 0x89, 1, 2, 3, 4, 5, 6, 7, 8, 9}), 0, false},
		{[]byte{0x1F, 0x43, 0xB6, 0x75}, 0, false},
		{nil, 0, false},
	} {
		p := filepath.Join(dir, "c.mkv")
		if err := os.WriteFile(p, tc.b, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := mkvChunkStart(p)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("% x: %v %v, want %v ok=%v", tc.b, got, err, tc.want, tc.ok)
		}
	}
}

func TestFMP4ChunkStartPicksTrack(t *testing.T) {
	dir := t.TempDir()
	// Two track fragments: audio (track 2) earlier than video (track 1).
	traf := func(id uint32, tfdt uint64) []byte {
		return mkBox("traf", mkFullBox("tfhd", 0, 0x020000, u32(id)), mkFullBox("tfdt", 1, 0, u64(tfdt)))
	}
	moof := mkBox("moof", mkFullBox("mfhd", 0, 0, u32(1)), traf(2, 48000*19), traf(1, 90000*20))
	chunk := bytes.Join([][]byte{mkBox("styp", []byte("msdh")), moof, mkBox("mdat", []byte("x"))}, nil)
	p := filepath.Join(dir, "c.m4s")
	if err := os.WriteFile(p, chunk, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := fmp4ChunkStart(p, 1, 90000); err != nil || got != 20*time.Second {
		t.Errorf("video track: %v %v", got, err)
	}
	if _, err := fmp4ChunkStart(p, 3, 90000); err == nil {
		t.Error("absent track: no error")
	}
	if err := os.WriteFile(p, chunk[:len(chunk)-len(moof)/2], 0o644); err != nil { // inside the moof
		t.Fatal(err)
	}
	if _, err := fmp4ChunkStart(p, 1, 90000); err == nil {
		t.Error("truncated chunk: no error")
	}
	tracks, err := parseInitTracks(testVideoInit(1280, 720))
	if err != nil || len(tracks) != 1 || tracks[0].Handler != "vide" || tracks[0].Timescale != 90000 || tracks[0].TrackID != 1 {
		t.Errorf("init tracks %+v %v", tracks, err)
	}
}

func TestAssembleChunks(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) fmp4Chunk {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return fmp4Chunk{path: p}
	}
	var removed []string
	rm := func(p string) { removed = append(removed, filepath.Base(p)); _ = os.Remove(p) }
	// fMP4: one styp, no sidx (each described a single chunk).
	a := write("a.m4s", testChunk(1, 0, 3750, 24, []byte("AAAA")))
	b := write("b.m4s", testChunk(2, 90000, 3750, 24, []byte("BBBB")))
	dst := filepath.Join(dir, "seg.m4s")
	if err := assembleChunks(dst, ContainerFMP4, []fmp4Chunk{a, b}, rm); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	boxes, err := readBoxes(got, 0, len(got))
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, x := range boxes {
		types = append(types, x.typ)
	}
	if strings.Join(types, ",") != "styp,moof,mdat,moof,mdat" || fmt.Sprint(removed) != "[a.m4s b.m4s]" {
		t.Errorf("boxes %v removed %v", types, removed)
	}
	// MPEG-TS: plain concatenation; a single chunk is renamed.
	c, d := write("c.ts", []byte("CC")), write("d.ts", []byte("DD"))
	if err := assembleChunks(filepath.Join(dir, "seg.ts"), ContainerMPEGTS, []fmp4Chunk{c, d}, rm); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "seg.ts")); string(b) != "CCDD" {
		t.Errorf("ts segment %q", b)
	}
	e := write("e.ts", []byte("EE"))
	if err := assembleChunks(filepath.Join(dir, "one.ts"), ContainerMPEGTS, []fmp4Chunk{e}, rm); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.path); !os.IsNotExist(err) {
		t.Error("single chunk not moved")
	}
	if err := assembleChunks(filepath.Join(dir, "x.ts"), ContainerMPEGTS, []fmp4Chunk{{path: filepath.Join(dir, "gone")}, e}, rm); err == nil {
		t.Error("missing chunk: no error")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.ts.tmp")); !os.IsNotExist(err) {
		t.Error("failed assembly left its temp file")
	}
}

func TestParseFFmpegPlaylistEntries(t *testing.T) {
	p := parseFFmpegPlaylist([]byte("#EXTM3U\n#EXTINF:2.502500,\nc-1/00000.ts\n#EXTINF:bad,\n00001.ts\n#EXTINF:1e300,\n00002.ts\n#EXT-X-ENDLIST\n"))
	if len(p.entries) != 3 || p.entries[0].name != "00000.ts" || p.entries[0].dur != 2502500*time.Microsecond ||
		p.entries[1].dur != 0 || p.entries[2].dur != 0 || !p.ended {
		t.Errorf("%+v", p)
	}
}

// chunkSession starts a keyframe-planned TS copy session on the fake
// runner: 60s with keyframes every gop.
func chunkSession(t *testing.T, fr *fakeRunner, gop time.Duration, opts ...Option) *Session {
	t.Helper()
	fr.chunkGOP = gop
	m := newTestManager(t, fr, config.Transcode{}, opts...)
	info := mustParse(t, "mp4_h264_aac.json")
	info.Duration = time.Minute
	info.Keyframes = everyKeyframe(time.Minute, gop)
	d := core.Decision{Kind: core.DecisionRemux, Container: ContainerMPEGTS, VideoCopy: true, AudioCopy: true, AudioStream: 1}
	hs, err := m.StartHLS(t.Context(), core.HLSRequest{SessionID: "chunks", InputURL: "http://127.0.0.1:1/source", Info: info, Decision: d})
	if err != nil {
		t.Fatal(err)
	}
	s := hs.(*Session)
	if !s.plan.Keyframed() {
		t.Fatal("plan not keyframed")
	}
	return s
}

// chunkTimesOf is the media times of the fake chunks a segment holds.
func chunkTimesOf(t *testing.T, body []byte) []float64 {
	t.Helper()
	var out []float64
	for _, m := range tsChunkMarkers(body) {
		i := strings.LastIndex(m, " t=")
		if i < 0 {
			t.Fatalf("marker %q", m)
		}
		f, err := strconv.ParseFloat(m[i+3:], 64)
		if err != nil {
			t.Fatalf("marker %q", m)
		}
		out = append(out, f)
	}
	return out
}

func TestChunkedSessionRegroupsOntoPlan(t *testing.T) {
	fr := newFakeRunner()
	fr.chunkEnd = time.Minute
	s := chunkSession(t, fr, 2500*time.Millisecond, WithBuffering(8*time.Second, 0, 0))
	if s.total != 12 {
		t.Fatalf("total %d", s.total)
	}
	for n := 0; n < 12; n++ {
		r := get(t, s, n)
		if r.Code != 200 {
			t.Fatalf("segment %d: %d %s", n, r.Code, r.Body.String())
		}
		got := fmt.Sprint(chunkTimesOf(t, r.Body.Bytes()))
		if want := fmt.Sprint([]float64{float64(5 * n), float64(5*n) + 2.5}); got != want {
			t.Errorf("segment %d holds chunks %s, want %s", n, got, want)
		}
	}
	if r := get(t, s, 12); r.Code != 404 {
		t.Errorf("segment past the plan: %d", r.Code)
	}
	args := fr.lastStart()
	if argValue(args, "-hls_time") != "0.500" || argValue(args, "-start_number") != "0" || hasArg(args, "-force_key_frames") {
		t.Errorf("chunked copy args %v", args)
	}
}

func TestChunkedSessionRestartDropsPreroll(t *testing.T) {
	fr := newFakeRunner()
	fr.segDelay = 20 * time.Millisecond
	s := chunkSession(t, fr, 10*time.Second, WithBuffering(4*time.Second, 0, 0))
	if s.total != 6 {
		t.Fatalf("total %d", s.total)
	}
	// Far ahead of the encoder: restart at 40s; ffmpeg's seek lands on the
	// 30s keyframe, whose interval belongs to segment 3.
	r := get(t, s, 4)
	if r.Code != 200 || fmt.Sprint(chunkTimesOf(t, r.Body.Bytes())) != "[40]" {
		t.Fatalf("segment 4 after restart: %d %v", r.Code, tsChunkMarkers(r.Body.Bytes()))
	}
	if !strings.Contains(tsChunkMarkers(r.Body.Bytes())[0], "ss=40.000") {
		t.Errorf("segment 4 not from the restart: %v", tsChunkMarkers(r.Body.Bytes()))
	}
	s.mu.Lock()
	done3 := s.done[0][3]
	s.mu.Unlock()
	if done3 {
		t.Error("the restart's pre-roll was published as segment 3")
	}
	// Behind the run: restart again.
	if r := get(t, s, 1); r.Code != 200 || fmt.Sprint(chunkTimesOf(t, r.Body.Bytes())) != "[10]" {
		t.Fatalf("segment 1: %d %v", r.Code, tsChunkMarkers(r.Body.Bytes()))
	}
	// No chunk directory of a finished run survives it.
	s.mu.Lock()
	err := s.startRunLocked(5, false)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "finished runs' chunk directories removed", func() bool {
		dirs, _ := filepath.Glob(filepath.Join(s.dir, "c-*"))
		return len(dirs) == 1
	})
}

// A GOP longer than several plan segments (an index from another release
// after failover): the segments it spans are served its media rather than
// never produced.
func TestChunkedSessionAliasesSegmentsInsideAGOP(t *testing.T) {
	fr := newFakeRunner()
	fr.chunkEnd = time.Minute
	s := chunkSession(t, fr, 2500*time.Millisecond, WithBuffering(30*time.Second, 0, 0))
	fr.mu.Lock()
	fr.chunkGOP = 20 * time.Second // the new release's keyframes
	fr.mu.Unlock()
	if err := s.SwapInput(t.Context(), "http://127.0.0.1:1/other", s.Info(), 0); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 12; n++ {
		r := get(t, s, n)
		if r.Code != 200 {
			t.Fatalf("segment %d: %d", n, r.Code)
		}
		times := chunkTimesOf(t, r.Body.Bytes())
		want := math.Floor(float64(5*n)/20) * 20
		if len(times) != 1 || times[0] != want {
			t.Errorf("segment %d holds %v, want the GOP at %v", n, times, want)
		}
	}
}

// Matroska without a keyframe index, GOPs longer than a segment: in order,
// a segment inside a GOP is an empty Cluster (the joined stream must not
// repeat the GOP); asked for out of order (a seek), it is the GOP.
func TestChunkedMatroskaAliasServedForSeeks(t *testing.T) {
	fr := newFakeRunner()
	fr.chunkGOP = 10 * time.Second
	fr.chunkEnd = time.Minute
	m := newTestManager(t, fr, config.Transcode{}, WithBuffering(60*time.Second, 0, 0))
	info := mustParse(t, "mp4_h264_aac.json")
	info.Duration = time.Minute
	d := core.Decision{Kind: core.DecisionRemux, Container: ContainerMatroska, VideoCopy: true, AudioCopy: true, AudioStream: 1}
	hs, err := m.StartHLS(t.Context(), core.HLSRequest{SessionID: "mkv-alias", InputURL: "http://127.0.0.1:1/source", Info: info, Decision: d})
	if err != nil {
		t.Fatal(err)
	}
	s := hs.(*Session)
	if s.plan.Keyframed() || s.total != 15 {
		t.Fatalf("plan keyframed=%v total=%d", s.plan.Keyframed(), s.total)
	}
	body := func(n int) []byte {
		t.Helper()
		r := get(t, s, n)
		if r.Code != 200 {
			t.Fatalf("segment %d: %d", n, r.Code)
		}
		return r.Body.Bytes()
	}
	// A probe requests metadata only: the next GET still needs its GOP.
	head := httptest.NewRecorder()
	s.ServeSegment(head, httptest.NewRequest(http.MethodHead, "/0.mkv", nil), 0)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD segment 0: %d %q", head.Code, head.Body.Bytes())
	}
	if b := body(1); !bytes.Contains(b, []byte("t=0.000")) {
		t.Errorf("first GET after HEAD: %q", b)
	}
	for n := 0; n < 4; n++ {
		b := body(n)
		if empty := len(b) <= emptyClusterSize; empty != (n == 1 || n == 3) {
			t.Errorf("in order, segment %d is %d bytes", n, len(b))
		}
	}
	// Segment 1 [4s,8s) lies inside the GOP at 0s; segment 3 [12s,16s)
	// inside the one at 10s, which starts segment 2.
	b := body(1)
	if !bytes.Contains(b, []byte("t=0.000")) {
		t.Errorf("seek to segment 1: %q", b)
	}
	if retry := body(1); !bytes.Equal(retry, b) {
		t.Errorf("retry changed segment 1: %q -> %q", b, retry)
	}
	req := httptest.NewRequest(http.MethodGet, "/1.mkv", nil)
	req.Header.Set("Range", "bytes=4-")
	rec := httptest.NewRecorder()
	s.ServeSegment(rec, req, 1)
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), b[4:]) {
		t.Errorf("range retry changed segment 1: status=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	if b := body(3); !bytes.Contains(b, []byte("t=10.000")) {
		t.Errorf("seek to segment 3: %q", b)
	}
	if b := body(4); len(b) > emptyClusterSize {
		t.Errorf("segment 4 after 3 (in order) is %d bytes, want the empty Cluster", len(b))
	}
	if b := body(4); len(b) > emptyClusterSize {
		t.Errorf("retry of sequential segment 4 is %d bytes, want the empty Cluster", len(b))
	}
	_ = body(0)
	// Fetching the init starts a new decoder, even if its first segment
	// happens to follow the previous decoder's last request.
	if init := get(t, s, InitSegment); init.Code != http.StatusOK {
		t.Fatalf("init: %d", init.Code)
	}
	if b := body(1); !bytes.Contains(b, []byte("t=0.000")) {
		t.Errorf("first GET after init: %q", b)
	}
}
