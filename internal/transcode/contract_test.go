package transcode

import (
	"errors"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// yellowstone is the opening release of a real Apple TV session: 10-bit
// SDR HEVC at 2.00:1 without colour tags, played as a segmented-Matroska
// copy. Its failover replacement was 8-bit 16:9 H.264.
func yellowstone() (orig, repl core.MediaInfo) {
	orig = core.MediaInfo{Container: "mkv", BitrateKbps: 6000, Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "hevc", Profile: "Main 10", Width: 1920, Height: 960, BitDepth: 10},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 6, Language: "eng"},
	}}
	repl = core.MediaInfo{Container: "mkv", BitrateKbps: 5000, Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "h264", Profile: "High", Width: 1920, Height: 1080, BitDepth: 8, ColorTransfer: "bt709"},
		{Index: 1, Type: "audio", Codec: "eac3", Channels: 2, Language: "eng"},
	}}
	return orig, repl
}

func copyMKV() core.Decision {
	return core.Decision{Kind: core.DecisionRemux, Container: ContainerMatroska, VideoCopy: true, AudioCopy: true, AudioStream: 1, BitrateKbps: 6000}
}

func contractSpec(t *testing.T, info core.MediaInfo, d core.Decision, c *videoContract) runSpec {
	t.Helper()
	s := baseSpec(info, d)
	s.Target = c
	return s
}

func TestContractOfCopyKeepsUntaggedTenBitSDR(t *testing.T) {
	orig, _ := yellowstone()
	c, ok := contractOf(orig, copyMKV())
	if !ok {
		t.Fatal("no contract")
	}
	// Untagged HD: decoders assume BT.709, limited range, square pixels.
	want := videoContract{copied: true, codec: "hevc", depth: 10, rng: rangeSDR, width: 1920, height: 960,
		pic: picture{sarNum: 1, sarDen: 1, prim: "bt709", matrix: "bt709"}}
	if *c != want {
		t.Fatalf("contract %+v, want %+v", *c, want)
	}
}

func TestContractOfTranscodeMatchesEncodedOutput(t *testing.T) {
	_, repl := yellowstone()
	d := core.Decision{Kind: core.DecisionTranscode, Container: ContainerMatroska, VideoCodec: "hevc", MaxHeight: 960, AudioStream: 1}
	c, _ := contractOf(repl, d)
	// 1920x1080 scaled to 960 lines: 1706 wide, as the live header was.
	if c.width != 1706 || c.height != 960 || c.depth != 8 || c.rng != rangeSDR || c.pic.sarNum != 2560 || c.pic.sarDen != 2559 {
		t.Fatalf("contract %+v", *c)
	}
	// The opening run itself is unchanged by the target.
	plain := strings.Join(buildArgs(baseSpec(repl, d)), " ")
	target := strings.Join(buildArgs(contractSpec(t, repl, d, c)), " ")
	if plain != target {
		t.Fatalf("contract changed the opening encode:\n%s\n%s", plain, target)
	}
}

// The live Yellowstone failover delivered HEVC Main 8-bit 1706x960 under
// a Main 10 1920x960 header. Keep the header's depth, size and square
// pixels: pillarbox the 16:9 picture, never crop or stretch it.
func TestYellowstoneFailoverKeepsHeaderGeometryAndDepth(t *testing.T) {
	orig, repl := yellowstone()
	c, _ := contractOf(orig, copyMKV())
	d := adaptToContract(copyMKV(), orig, repl, ContainerMatroska, c)
	if d.VideoCopy || d.VideoCodec != "hevc" || d.ToneMap || d.BitrateKbps != 6000 {
		t.Fatalf("decision %+v", d)
	}
	if err := c.check(repl, testFeatures()); err != nil {
		t.Fatal(err)
	}
	sw := strings.Join(buildArgs(contractSpec(t, repl, d, c)), " ")
	mustContain(t, []string{sw}, "scale=1706:960,pad=1920:960:(ow-iw)/2:(oh-ih)/2,setsar=1/1,format=yuv420p10le[vout]",
		"-c:v libx265", "-color_primaries bt709 -color_trc bt709 -colorspace bt709", "-bsf:v dump_extra=freq=keyframe")
	mustNotContain(t, []string{sw}, "zscale", "hdr10-opt", "crop")

	vt := contractSpec(t, repl, d, c)
	vt.Accel = core.HWAccel{Name: AccelVideoToolbox, Encoders: []string{"h264_videotoolbox", "hevc_videotoolbox"}}
	args := strings.Join(buildArgs(vt), " ")
	mustContain(t, []string{args}, "-hwaccel videotoolbox -", "pad=1920:960:(ow-iw)/2:(oh-ih)/2,setsar=1/1,format=p010le[vout]",
		"-c:v hevc_videotoolbox", "-profile:v main10", "-color_primaries bt709")
	mustNotContain(t, []string{args}, "hwaccel_output_format", "scale_vt")
}

// A same-aspect replacement keeps the GPU pipeline, at 10 bits.
func TestContractSameAspectStaysOnGPU(t *testing.T) {
	orig, repl := yellowstone()
	orig.Video().Height, orig.Video().Width = 1080, 1920
	c, _ := contractOf(orig, copyMKV())
	d := adaptToContract(copyMKV(), orig, repl, ContainerMatroska, c)
	vt := contractSpec(t, repl, d, c)
	vt.Accel = core.HWAccel{Name: AccelVideoToolbox, Encoders: []string{"h264_videotoolbox", "hevc_videotoolbox"}}
	args := strings.Join(buildArgs(vt), " ")
	mustContain(t, []string{args}, "-hwaccel_output_format videotoolbox_vld", "scale_vt=w=1920:h=1080:format=p010le[vout]", "-profile:v main10")
	mustNotContain(t, []string{args}, "pad=", "setsar")
}

// HDR10 opening copy, then SDR, then HDR10 again, then a scope crop: each
// swap is judged against the delivered stream, not the previous source.
func TestRepeatedSwapsKeepHDR10Contract(t *testing.T) {
	hdr := mustParse(t, "mkv_hevc_hdr10.json")
	sdr := mustParse(t, "mp4_h264_aac.json")
	scope := mustParse(t, "mkv_pgs.json") // 1920x800 SDR
	d := copyMKV()
	c, _ := contractOf(hdr, d)
	if c.rng != rangeHDR10 || c.depth != 10 || c.width != 3840 || c.height != 2160 {
		t.Fatalf("contract %+v", *c)
	}
	cur := hdr
	steps := []struct {
		name     string
		info     core.MediaInfo
		copy     bool
		contains []string
	}{
		{"sdr", sdr, false, []string{
			"zscale=tin=bt709:pin=bt709:min=bt709:rin=limited:t=smpte2084:p=bt2020:m=bt2020nc:r=limited:npl=203,scale=3840:2160,format=yuv420p10le",
			"hdr10-opt=1", "transfer=smpte2084", "-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc"}},
		{"hdr10-again", hdr, true, nil},
		{"scope", scope, false, []string{
			"zscale=tin=bt709", "scale=3840:1600,pad=3840:2160:(ow-iw)/2:(oh-ih)/2,setsar=1/1,format=yuv420p10le"}},
	}
	for _, st := range steps {
		d = adaptToContract(d, cur, st.info, ContainerMatroska, c)
		cur = st.info
		if d.VideoCopy != st.copy || d.ToneMap {
			t.Fatalf("%s: decision %+v", st.name, d)
		}
		if err := c.check(st.info, testFeatures()); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if st.copy {
			continue
		}
		args := strings.Join(buildArgs(contractSpec(t, cur, d, c)), " ")
		mustContain(t, []string{args}, st.contains...)
		mustNotContain(t, []string{args}, "tonemap", "-color_trc bt709")
	}
}

// An SDR opening transcode stays SDR when HDR replacements follow: the
// second swap must not compare with the first replacement.
func TestRepeatedSwapsKeepSDRContract(t *testing.T) {
	hdr := mustParse(t, "mkv_hevc_hdr10.json")
	sdr := mustParse(t, "mp4_h264_aac.json")
	d := core.Decision{Kind: core.DecisionTranscode, Container: ContainerMatroska, VideoCodec: "h264", MaxHeight: 1080, AudioStream: 1, BitrateKbps: 8000}
	c, _ := contractOf(hdr, d)
	if c.rng != rangeSDR || c.depth != 8 || c.width != 1920 || c.height != 1080 {
		t.Fatalf("contract %+v", *c)
	}
	cur := hdr
	for i, next := range []core.MediaInfo{sdr, hdr, sdr} {
		d = adaptToContract(d, cur, next, ContainerMatroska, c)
		cur = next
		if d.VideoCopy || d.VideoCodec != "h264" || d.BitrateKbps != 8000 || d.ToneMap != (next.Video().ColorTransfer == "smpte2084") {
			t.Fatalf("swap %d: %+v", i, d)
		}
		args := strings.Join(buildArgs(contractSpec(t, cur, d, c)), " ")
		mustContain(t, []string{args}, "-c:v libx264", "format=yuv420p", "-color_trc bt709")
		mustNotContain(t, []string{args}, "10le", "smpte2084 -colorspace", "pad=")
	}
}

func TestContractCopyNeedsExactStream(t *testing.T) {
	orig, _ := yellowstone()
	c, _ := contractOf(orig, copyMKV())
	same := orig
	same.Streams = append([]core.StreamInfo(nil), orig.Streams...)
	if d := adaptToContract(copyMKV(), orig, same, ContainerMatroska, c); !d.VideoCopy {
		t.Fatalf("identical stream not copied: %+v", d)
	}
	for name, mut := range map[string]func(v *core.StreamInfo){
		"8-bit":   func(v *core.StreamInfo) { v.BitDepth = 8 },
		"width":   func(v *core.StreamInfo) { v.Width = 1916 },
		"height":  func(v *core.StreamInfo) { v.Height = 1080 },
		"hdr":     func(v *core.StreamInfo) { v.ColorTransfer = "smpte2084" },
		"smaller": func(v *core.StreamInfo) { v.Width, v.Height = 1280, 640 },
	} {
		next := orig
		next.Streams = append([]core.StreamInfo(nil), orig.Streams...)
		mut(&next.Streams[0])
		if d := adaptToContract(copyMKV(), orig, next, ContainerMatroska, c); d.VideoCopy {
			t.Errorf("%s: copied a stream that differs from the header", name)
		}
	}
	// An encoded opening is never replaced by a copy, even of a match.
	td := core.Decision{Kind: core.DecisionTranscode, Container: ContainerMatroska, VideoCodec: "hevc", MaxHeight: 960, AudioStream: 1}
	tc, _ := contractOf(orig, td)
	if d := adaptToContract(td, orig, same, ContainerMatroska, tc); d.VideoCopy {
		t.Fatal("copied under an encoded contract")
	}
}

func TestContractCheckRejectsUndeliverable(t *testing.T) {
	hdr := mustParse(t, "mkv_hevc_hdr10.json")
	sdr := mustParse(t, "mp4_h264_aac.json")
	dv := mustParse(t, "mkv_dv_p8.json")
	p5 := mustParse(t, "mkv_dv_p8.json")
	p5.Video().DolbyVisionProfile = 5
	p5.Video().ColorTransfer = ""
	noZ := testFeatures()
	noZ.filters = parseFilters([]byte(strings.ReplaceAll(sampleFilters, " zscale ", " zzzzzz ")))
	hdrC, _ := contractOf(hdr, copyMKV())
	p5C, _ := contractOf(p5, copyMKV())
	av1 := sdr
	av1.Streams = append([]core.StreamInfo(nil), sdr.Streams...)
	av1.Streams[0].Codec = "av1"
	av1C, _ := contractOf(av1, copyMKV())
	estimated := sdr
	estimated.Estimated = true
	hi10 := sdr
	hi10.Streams = append([]core.StreamInfo(nil), sdr.Streams...)
	hi10.Streams[0].BitDepth = 10
	hi10C, _ := contractOf(hi10, copyMKV())
	audioOnly := core.MediaInfo{Streams: []core.StreamInfo{{Index: 0, Type: "audio", Codec: "aac"}}}
	dvC, _ := contractOf(dv, copyMKV())
	withDV := func(mut func(v *core.StreamInfo)) core.MediaInfo {
		out := dv
		out.Streams = append([]core.StreamInfo(nil), dv.Streams...)
		mut(&out.Streams[0])
		return out
	}
	dv7 := withDV(func(v *core.StreamInfo) { v.DolbyVisionProfile, v.DolbyVisionCompatibilityID = 7, 6 })
	dv84 := withDV(func(v *core.StreamInfo) { v.DolbyVisionCompatibilityID = 4 })
	noRPU := withDV(func(v *core.StreamInfo) { v.DolbyVisionRPUPresent = nil })
	hdr10Sized := withDV(func(v *core.StreamInfo) {
		v.DolbyVision, v.DolbyVisionProfile, v.DolbyVisionCompatibilityID = false, 0, 0
	})

	for name, tc := range map[string]struct {
		c    *videoContract
		info core.MediaInfo
		f    features
		ok   bool
	}{
		"sdr into hdr10":            {hdrC, sdr, testFeatures(), true},
		"dv8 into hdr10":            {hdrC, dv, testFeatures(), true},
		"sdr into hdr10 no zscale":  {hdrC, sdr, noZ, false},
		"hdr10 copy without zscale": {hdrC, hdr, noZ, true},
		"dv5 source into hdr10":     {hdrC, p5, testFeatures(), false},
		"hdr10 into dv5":            {p5C, hdr, testFeatures(), false},
		"dv5 into dv5":              {p5C, p5, testFeatures(), true},
		"sdr into av1":              {av1C, sdr, testFeatures(), false},
		"8-bit into 10-bit h264":    {hi10C, sdr, testFeatures(), false},
		"unprobed":                  {hdrC, estimated, testFeatures(), false},
		"no video":                  {hdrC, audioOnly, testFeatures(), false},
		// A Dolby Vision header continues only with the same signalling.
		"dv8 into dv8":         {dvC, dv, testFeatures(), true},
		"hdr10 into dv8":       {dvC, hdr10Sized, testFeatures(), false},
		"sdr into dv8":         {dvC, sdr, testFeatures(), false},
		"dv7 into dv8":         {dvC, dv7, testFeatures(), false},
		"dv8.4 into dv8.1":     {dvC, dv84, testFeatures(), false},
		"dv8 without rpu flag": {dvC, noRPU, testFeatures(), false},
	} {
		err := tc.c.check(tc.info, tc.f)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrIncompatibleSwap)) {
			t.Errorf("%s: check = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

// SwapInput validates before it stops the run or deletes segments; a
// session without a contract accepts anything (fMP4/TS keep their path).
func TestSessionCheckSwap(t *testing.T) {
	hdr := mustParse(t, "mkv_hevc_hdr10.json")
	estimated := mustParse(t, "mp4_h264_aac.json")
	estimated.Estimated = true
	c, _ := contractOf(hdr, copyMKV())
	s := &Session{
		m: &Manager{feat: testFeatures()},
		sessionInput: sessionInput{
			contract: c,
		},
	}
	if err := s.CheckSwap(estimated); !errors.Is(err, ErrIncompatibleSwap) {
		t.Fatalf("CheckSwap = %v", err)
	}
	s.closed = true // SwapInput would otherwise report the closed session
	if err := s.SwapInput(t.Context(), "http://x/y", estimated, 0); !errors.Is(err, ErrIncompatibleSwap) {
		t.Fatalf("SwapInput = %v", err)
	}
	if err := (&Session{
		m: s.m,
	}).CheckSwap(estimated); err != nil {
		t.Fatalf("no-contract session: %v", err)
	}
}

func TestContractFitPadsWithoutCrop(t *testing.T) {
	c := &videoContract{width: 1920, height: 1080, pic: picture{sarNum: 1, sarDen: 1}}
	for _, tc := range []struct {
		w, h   int
		sar    string
		fw, fh int
	}{
		{1920, 800, "", 1920, 800},   // scope: letterbox
		{1440, 1080, "", 1440, 1080}, // 4:3: pillarbox
		{3840, 2160, "1:1", 1920, 1080},
		{1920, 1080, "", 1920, 1080},
		{1918, 1080, "", 1920, 1080},    // within two pixels: no bar
		{1440, 1080, "4:3", 1920, 1080}, // anamorphic HDV is 16:9
		{720, 480, "32:27", 1920, 1080}, // anamorphic NTSC DVD (16:9)
		{720, 576, "16:15", 1440, 1080}, // PAL DVD, 4:3
		{720, 480, "8:9", 1440, 1080},   // NTSC 4:3 with 8:9 pixels
	} {
		fw, fh := c.fit(&core.StreamInfo{Width: tc.w, Height: tc.h, SampleAspectRatio: tc.sar})
		if fw != tc.fw || fh != tc.fh {
			t.Errorf("%dx%d sar %s fits %dx%d, want %dx%d", tc.w, tc.h, tc.sar, fw, fh, tc.fw, tc.fh)
		}
	}
	// An anamorphic contract keeps its SAR; a square 16:9 source fills it.
	dvd := &videoContract{width: 720, height: 480, pic: picture{sarNum: 32, sarDen: 27}}
	if fw, fh := dvd.fit(&core.StreamInfo{Width: 1920, Height: 1080}); fw != 720 || fh != 480 {
		t.Errorf("16:9 into anamorphic DVD fits %dx%d", fw, fh)
	}
}

// An anamorphic opening copy keeps its sample aspect ratio, and its colour
// tags decide what a replacement is converted to.
func TestContractPreservesAnamorphicSARAndColour(t *testing.T) {
	dvd := core.MediaInfo{Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "h264", Width: 720, Height: 480, BitDepth: 8, SampleAspectRatio: "32:27"},
		{Index: 1, Type: "audio", Codec: "ac3", Channels: 2},
	}}
	c, _ := contractOf(dvd, copyMKV())
	if c.pic != (picture{sarNum: 32, sarDen: 27, prim: "smpte170m", matrix: "smpte170m"}) {
		t.Fatalf("contract picture %+v", c.pic)
	}
	// A square-pixel HD replacement is scaled into 720x480, which leaves
	// scale's 32:27 SAR, and converted from BT.709 to the DVD's BT.601.
	hd := mustParse(t, "mp4_h264_aac.json")
	d := adaptToContract(copyMKV(), dvd, hd, ContainerMatroska, c)
	args := strings.Join(buildArgs(contractSpec(t, hd, d, c)), " ")
	mustContain(t, []string{args},
		"zscale=tin=bt709:pin=bt709:min=bt709:rin=limited:t=bt709:p=smpte170m:m=smpte170m:r=limited:npl=203,scale=720:480,format=yuv420p[vout]",
		"-color_primaries smpte170m -color_trc bt709 -colorspace smpte170m")
	mustNotContain(t, []string{args}, "pad=")
	// A replacement whose SAR differs is never copied under the header.
	square := dvd
	square.Streams = append([]core.StreamInfo(nil), dvd.Streams...)
	square.Streams[0].SampleAspectRatio = "1:1"
	if d := adaptToContract(copyMKV(), dvd, square, ContainerMatroska, c); d.VideoCopy {
		t.Fatal("copied a different sample aspect ratio")
	}
	// An explicit BT.709 SD stream is not treated as BT.601.
	tagged := square
	tagged.Streams[0].SampleAspectRatio = "32:27"
	tagged.Streams[0].ColorPrimaries, tagged.Streams[0].ColorSpace = "bt709", "bt709"
	if p := pictureOf(tagged.Video()); p.prim != "bt709" || p.matrix != "bt709" {
		t.Fatalf("explicit tags ignored: %+v", p)
	}
	if d := adaptToContract(copyMKV(), dvd, tagged, ContainerMatroska, c); d.VideoCopy {
		t.Fatal("copied BT.709 pixels under a BT.601 header")
	}
	// Full-range sources are read as full range.
	full := hd
	full.Streams = append([]core.StreamInfo(nil), hd.Streams...)
	full.Streams[0].ColorRange = "pc"
	yc, _ := contractOf(hd, copyMKV())
	fd := adaptToContract(copyMKV(), hd, full, ContainerMatroska, yc)
	mustContain(t, []string{strings.Join(buildArgs(contractSpec(t, full, fd, yc)), " ")}, "rin=full:t=bt709:p=bt709:m=bt709:r=limited")
}
