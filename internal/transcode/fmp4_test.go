package transcode

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fMP4 builders for tests.

func u16(v int) []byte    { return binary.BigEndian.AppendUint16(nil, uint16(v)) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func mkBox(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	return append(append(u32(uint32(8+len(body))), typ...), body...)
}

func mkFullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	vf := u32(uint32(version)<<24 | flags)
	return mkBox(typ, append([][]byte{vf}, payload...)...)
}

// testInit builds ftyp+moov for one track with the given sample entry.
func testInit(handler string, timescale, defaultDur uint32, entry []byte) []byte {
	ftyp := mkBox("ftyp", []byte("iso5"), u32(512), []byte("iso5iso6mp41"))
	mvhd := mkFullBox("mvhd", 0, 0, u32(0), u32(0), u32(1000), u32(0), make([]byte, 80))
	tkhd := mkFullBox("tkhd", 0, 3, u32(0), u32(0), u32(1), u32(0), u32(0), make([]byte, 60))
	mdhd := mkFullBox("mdhd", 0, 0, u32(0), u32(0), u32(timescale), u32(0), u16(0x55c4), u16(0))
	hdlr := mkFullBox("hdlr", 0, 0, u32(0), []byte(handler), make([]byte, 12), []byte("Handler\x00"))
	stbl := mkBox("stbl", mkFullBox("stsd", 0, 0, u32(1), entry))
	mdia := mkBox("mdia", mdhd, hdlr, mkBox("minf", stbl))
	trex := mkFullBox("trex", 0, 0, u32(1), u32(1), u32(defaultDur), u32(0), u32(0))
	moov := mkBox("moov", mvhd, mkBox("trak", tkhd, mdia), mkBox("mvex", trex))
	return append(ftyp, moov...)
}

func avc1Entry(w, h int, profile, compat, level byte) []byte {
	avcC := mkBox("avcC", []byte{1, profile, compat, level, 0xff, 0xe0, 0x00})
	return mkBox("avc1", make([]byte, 6), u16(1), make([]byte, 16), u16(w), u16(h), make([]byte, 50), avcC)
}

func audioEntry(typ string, ch, rate int, kids ...[]byte) []byte {
	return mkBox(typ, append([][]byte{make([]byte, 6), u16(1), make([]byte, 8), u16(ch), u16(16), u16(0), u16(0),
		u32(uint32(rate) << 16)}, kids...)...)
}

// esdsAAC is an esds box for AAC with the given audio object type.
func esdsAAC(aot byte) []byte {
	dsi := []byte{0x05, 2, aot<<3 | 0x01, 0x90}
	dcd := append([]byte{0x04, byte(13 + len(dsi)), 0x40, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, dsi...)
	sl := []byte{0x06, 1, 2}
	es := append(append([]byte{0x03, byte(3 + len(dcd) + len(sl)), 0, 1, 0}, dcd...), sl...)
	return mkFullBox("esds", 0, 0, es)
}

func testVideoInit(w, h int) []byte {
	return testInit("vide", 90000, 0, avc1Entry(w, h, 0x64, 0x00, 0x28))
}

func testAudioInit(ch, rate int) []byte {
	return testInit("soun", 48000, 0, audioEntry("mp4a", ch, rate, esdsAAC(2)))
}

// testChunk builds a dash-muxer style chunk: styp, sidx, one moof with
// samples of sampleDur each, and the payload as mdat.
func testChunk(seq uint32, tfdt int64, sampleDur uint32, samples int, payload []byte) []byte {
	if samples < 1 {
		samples = 1
	}
	styp := mkBox("styp", []byte("msdh"), u32(0), []byte("msdhmsix"))
	sidx := mkFullBox("sidx", 1, 0, u32(1), u32(90000), u64(uint64(tfdt)), u64(0), u16(0), u16(1),
		u32(0), u32(sampleDur*uint32(samples)), u32(0x90000000))
	build := func(dataOffset uint32) []byte {
		var run [][]byte
		run = append(run, u32(uint32(samples)), u32(dataOffset))
		size := len(payload) / samples
		for i := 0; i < samples; i++ {
			sz := size
			if i == samples-1 {
				sz = len(payload) - size*(samples-1)
			}
			run = append(run, u32(sampleDur), u32(uint32(sz)))
		}
		trun := mkFullBox("trun", 0, 0x000301, run...)
		tfhd := mkFullBox("tfhd", 0, 0x020000, u32(1))
		tfdtBox := mkFullBox("tfdt", 1, 0, u64(uint64(tfdt)))
		return mkBox("moof", mkFullBox("mfhd", 0, 0, u32(seq)), mkBox("traf", tfhd, tfdtBox, trun))
	}
	moof := build(0)
	moof = build(uint32(len(moof) + 8))
	return bytes.Join([][]byte{styp, sidx, moof, mkBox("mdat", payload)}, nil)
}

// ---------------------------------------------------------------------------

func TestParseInitPMSHeaders(t *testing.T) {
	// Init segments captured from a real PMS DASH session.
	for _, c := range []struct {
		file, handler, codecs string
		timescale             uint32
		w, h, ch, rate        int
	}{
		{"pms-header-video.mp4", "vide", "avc1.640028", 24000, 1920, 1080, 0, 0},
		{"pms-header-audio.mp4", "soun", "opus", 48000, 0, 0, 2, 48000},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "dash", c.file))
		if err != nil {
			t.Fatal(err)
		}
		ti, err := parseInit(b)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if ti.Handler != c.handler || ti.Codecs != c.codecs || ti.Timescale != c.timescale || ti.TrackID != 1 {
			t.Errorf("%s: %+v", c.file, ti)
		}
		if ti.Width != c.w || ti.Height != c.h || ti.Channels != c.ch || ti.SampleRate != c.rate {
			t.Errorf("%s: size/audio %+v", c.file, ti)
		}
	}
}

func TestCodecsStrings(t *testing.T) {
	hvcC := make([]byte, 23)
	hvcC[1] = 0x01 // space 0, tier main, profile 1
	hvcC[2] = 0x60 // compat flags 0x60000000 -> reversed 0x6
	hvcC[6] = 0x90 // constraint byte
	hvcC[12] = 120 // level 4.0
	av1C := []byte{0x81, 0x08, 0x40, 0}
	vpcC := []byte{1, 0, 0, 0, 0, 31, 0x80}
	for _, c := range []struct {
		entry string
		kids  map[string][]byte
		want  string
	}{
		{"avc1", map[string][]byte{"avcC": {1, 0x4d, 0x40, 0x1f}}, "avc1.4d401f"},
		{"hvc1", map[string][]byte{"hvcC": hvcC}, "hvc1.1.6.L120.90"},
		{"av01", map[string][]byte{"av1C": av1C}, "av01.0.08M.10"},
		{"vp09", map[string][]byte{"vpcC": vpcC}, "vp09.00.31.08"},
		{"ec-3", nil, "ec-3"},
		{"ac-3", nil, "ac-3"},
		{"fLaC", nil, "flac"},
		{"Opus", nil, "opus"},
	} {
		got := codecsString(c.entry, func(typ string) []byte { return c.kids[typ] })
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.entry, got, c.want)
		}
	}
	for aot, want := range map[byte]string{2: "mp4a.40.2", 5: "mp4a.40.5", 29: "mp4a.40.29"} {
		ti, err := parseInit(testInit("soun", 48000, 0, audioEntry("mp4a", 2, 48000, esdsAAC(aot))))
		if err != nil || ti.Codecs != want {
			t.Errorf("aot %d: %q %v", aot, ti.Codecs, err)
		}
	}
	if got := mp4aCodecs(nil); got != "mp4a.40.2" {
		t.Errorf("empty esds: %q", got)
	}
}

func TestParseInitErrors(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":     nil,
		"no moov":   mkBox("ftyp", []byte("iso5")),
		"truncated": testVideoInit(640, 360)[:40],
	} {
		if _, err := parseInit(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestScanChunkTimingAndCleanup(t *testing.T) {
	dir := t.TempDir()
	ti, err := parseInit(testAudioInit(2, 48000))
	if err != nil {
		t.Fatal(err)
	}
	// AAC priming makes ffmpeg's first audio fragment start before zero.
	neg := int64(-1024)
	path := filepath.Join(dir, "c1.m4s")
	if err := os.WriteFile(path, testChunk(1, neg, 960, 50, []byte("audio-payload")), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := scanChunk(path, ti)
	if err != nil {
		t.Fatal(err)
	}
	if c.start != 0 || c.end != time.Second {
		t.Errorf("span %v-%v, want 0-1s", c.start, c.end)
	}
	var out bytes.Buffer
	init := testAudioInit(2, 48000)
	if err := writeSegment(&out, init, []fmp4Chunk{c, c}); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if !bytes.HasPrefix(b, init) {
		t.Fatal("segment does not start with the init segment")
	}
	boxes, err := readBoxes(b, 0, len(b))
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, x := range boxes {
		types = append(types, x.typ)
	}
	if got := strings.Join(types, ","); got != "ftyp,moov,styp,moof,mdat,moof,mdat" {
		t.Errorf("segment boxes %s", got)
	}
	// The rewritten tfdt reads back as zero.
	moof := boxes[3]
	s, e, err := moofTiming(b[moof.start:moof.end], 0)
	if err != nil || s != 0 || e != 48000 {
		t.Errorf("rewritten moof timing %d-%d %v", s, e, err)
	}
	if !bytes.Contains(b, []byte("audio-payload")) {
		t.Error("payload missing")
	}

	// Default durations from tfhd/trex when trun has none.
	trunNoDur := mkFullBox("trun", 0, 0x000200, u32(3), u32(10), u32(10), u32(10))
	if d := trunDuration(trunNoDur[8:], 512); d != 1536 {
		t.Errorf("default durations: %d", d)
	}

	if err := os.WriteFile(path, []byte("garbage!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanChunk(path, ti); err == nil {
		t.Error("garbage chunk accepted")
	}
}
