package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	at "github.com/braedonsaunders/ether/internal/archive/archivetest"
)

func buildRAR(t testing.TB, opts at.RAROptions, files []at.RARFile) [][]byte {
	t.Helper()
	vols, err := at.BuildRAR(files, opts)
	if err != nil {
		t.Fatal(err)
	}
	return vols
}

func versions() []int { return []int{4, 5} }

func TestDetect(t *testing.T) {
	cases := []struct {
		in   []byte
		want Format
	}{
		{[]byte("Rar!\x1a\x07\x00\xcf\x90"), FormatRAR4},
		{[]byte("Rar!\x1a\x07\x01\x00\x33"), FormatRAR5},
		{[]byte("7z\xbc\xaf\x27\x1c\x00\x04"), Format7z},
		{[]byte("Rar!\x1a\x07"), FormatUnknown},
		{[]byte("Rar!\x1a\x07\x02\x00"), FormatUnknown},
		{[]byte("PK\x03\x04"), FormatUnknown},
		{nil, FormatUnknown},
	}
	for _, c := range cases {
		if got := Detect(c.in); got != c.want {
			t.Errorf("Detect(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for f, s := range map[Format]string{FormatUnknown: "unknown", FormatRAR4: "rar4", FormatRAR5: "rar5", Format7z: "7z", Format(99): "unknown"} {
		if f.String() != s {
			t.Errorf("Format(%d).String() = %q, want %q", f, f.String(), s)
		}
	}
}

func sampleFiles() []at.RARFile {
	return []at.RARFile{
		{Name: "Movie.2024.1080p/Movie.2024.1080p.mkv", Data: randBytes(1, 9000)},
		{Name: "Movie.2024.1080p/Sample", Dir: true},
		{Name: "Movie.2024.1080p/movie.nfo", Data: randBytes(2, 333), Large: true},
		{Name: "empty.txt"},
		{Name: "Фильм ñandú 映画.mkv", Data: randBytes(3, 4097)},
	}
}

func assertMembers(t *testing.T, members []Member, files []at.RARFile, vols []Volume) {
	t.Helper()
	if len(members) != len(files) {
		t.Fatalf("got %d members, want %d: %+v", len(members), len(files), members)
	}
	for i, f := range files {
		m := members[i]
		if m.Name != f.Name || m.Size != int64(len(f.Data)) || m.Dir != f.Dir || !m.Stored || m.Method != "store" {
			t.Fatalf("member %d = %+v, want name %q size %d dir %v stored", i, m, f.Name, len(f.Data), f.Dir)
		}
		if f.Dir {
			if err := m.Streamable(); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("dir Streamable = %v, want ErrUnsupported", err)
			}
			continue
		}
		if err := m.Streamable(); err != nil {
			t.Fatalf("%q Streamable: %v", m.Name, err)
		}
		mr, err := NewMemberReader(m, vols)
		if err != nil {
			t.Fatalf("NewMemberReader(%q): %v", m.Name, err)
		}
		if mr.Size() != int64(len(f.Data)) {
			t.Fatalf("reader size %d, want %d", mr.Size(), len(f.Data))
		}
		if got := readAll(t, mr, mr.Size()); !bytes.Equal(got, f.Data) {
			t.Fatalf("%q: payload mismatch", m.Name)
		}
		checkRandomReads(t, mr, f.Data, extentCuts(m), uint64(i+1))
	}
}

func TestRARRoundTrip(t *testing.T) {
	for _, v := range versions() {
		for _, tc := range []struct {
			name string
			o    at.RAROptions
		}{
			{"single", at.RAROptions{}},
			{"single-service-blocks", at.RAROptions{ServiceBlocks: true}},
			{"vol-300", at.RAROptions{VolumeSize: 300}},
			{"vol-777-service", at.RAROptions{VolumeSize: 777, ServiceBlocks: true, NewNumbering: true}},
			{"vol-2500", at.RAROptions{VolumeSize: 2500, OmitEndVolumeNumber: true}},
			{"vol-9100", at.RAROptions{VolumeSize: 9100}},
			{"vol-no-end", at.RAROptions{VolumeSize: 3000, OmitEndBlock: true}},
		} {
			t.Run(fmt.Sprintf("rar%d/%s", v, tc.name), func(t *testing.T) {
				tc.o.Version = v
				files := sampleFiles()
				bufs := buildRAR(t, tc.o, files)
				if tc.o.VolumeSize > 0 {
					if len(bufs) < 2 {
						t.Fatalf("expected several volumes, got %d", len(bufs))
					}
					for i, b := range bufs[:len(bufs)-1] {
						if int64(len(b)) > tc.o.VolumeSize {
							t.Fatalf("volume %d is %d bytes > %d", i, len(b), tc.o.VolumeSize)
						}
					}
				}
				vols := memVols(bufs...)
				members, err := ListRAR(vols)
				if err != nil {
					t.Fatal(err)
				}
				assertMembers(t, members, files, vols)
				if tc.o.VolumeSize == 300 && len(members[0].Extents) < 30 {
					t.Fatalf("first member has %d extents, want >= 30", len(members[0].Extents))
				}
			})
		}
	}
}

// TestRAR5ManyVolumes crosses volume index 128, where the RAR5 volume
// number vint grows to two bytes.
func TestRAR5ManyVolumes(t *testing.T) {
	data := randBytes(9, 150*200)
	files := []at.RARFile{{Name: "long.mkv", Data: data}}
	bufs := buildRAR(t, at.RAROptions{Version: 5, VolumeSize: 300}, files)
	if len(bufs) < 130 {
		t.Fatalf("%d volumes", len(bufs))
	}
	for i, b := range bufs[:len(bufs)-1] {
		if len(b) != 300 {
			t.Fatalf("volume %d: %d bytes, want 300", i, len(b))
		}
	}
	for _, i := range []int{0, 1, 127, 128, 129, len(bufs) - 1} {
		v, err := ReadRARVolume(bytes.NewReader(bufs[i]), int64(len(bufs[i])), nil)
		if err != nil {
			t.Fatal(err)
		}
		if v.Number != i || v.First != (i == 0) {
			t.Fatalf("volume %d: Number %d First %v", i, v.Number, v.First)
		}
	}
	vols := memVols(bufs...)
	members, err := ListRAR(vols)
	if err != nil {
		t.Fatal(err)
	}
	assertMembers(t, members, files, vols)
}

func TestRARVolumeFields(t *testing.T) {
	for _, ver := range versions() {
		format := map[int]Format{4: FormatRAR4, 5: FormatRAR5}[ver]
		t.Run(format.String(), func(t *testing.T) {
			bufs := buildRAR(t, at.RAROptions{Version: ver, VolumeSize: 4000, NewNumbering: true, SolidArchive: true}, sampleFiles())
			for i, b := range bufs {
				v, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil)
				if err != nil {
					t.Fatalf("volume %d: %v", i, err)
				}
				if v.Format != format || !v.MultiVolume || v.First != (i == 0) || !v.Solid {
					t.Fatalf("volume %d: %+v", i, v)
				}
				if format == FormatRAR4 && !v.NewNumbering {
					t.Fatalf("volume %d: NewNumbering not set", i)
				}
				last := v.Files[len(v.Files)-1]
				wantNum := i
				if last.SplitAfter {
					if v.EndFound {
						t.Fatalf("volume %d: end block read after a SplitAfter file", i)
					}
					if format == FormatRAR4 && i > 0 {
						wantNum = -1 // only the end block carries it
					}
				} else if !v.EndFound || v.MoreVolumes != (i < len(bufs)-1) {
					t.Fatalf("volume %d: EndFound %v MoreVolumes %v", i, v.EndFound, v.MoreVolumes)
				}
				if v.Number != wantNum {
					t.Fatalf("volume %d: Number = %d, want %d", i, v.Number, wantNum)
				}
				for _, f := range v.Files {
					if !f.HasCRC && !f.Dir || f.HeaderOffset < v.DataStart || f.DataOffset <= f.HeaderOffset {
						t.Fatalf("volume %d: file %+v", i, f)
					}
				}
			}
			single := buildRAR(t, at.RAROptions{Version: ver}, sampleFiles())[0]
			v, err := ReadRARVolume(bytes.NewReader(single), int64(len(single)), nil)
			if err != nil {
				t.Fatal(err)
			}
			if v.MultiVolume || !v.First || v.Number != 0 || !v.EndFound || v.MoreVolumes || v.Solid {
				t.Fatalf("single volume: %+v", v)
			}
			if format == FormatRAR4 && v.DataStart != 7+13 {
				t.Fatalf("RAR4 DataStart = %d, want 20", v.DataStart)
			}
			if format == FormatRAR5 && v.DataStart != v.Files[0].HeaderOffset {
				t.Fatalf("RAR5 DataStart = %d, first header at %d", v.DataStart, v.Files[0].HeaderOffset)
			}
		})
	}
}

func TestRARFlagVariants(t *testing.T) {
	for _, ver := range versions() {
		t.Run(fmt.Sprintf("rar%d", ver), func(t *testing.T) {
			files := []at.RARFile{
				{Name: "a.mkv", Data: randBytes(10, 3000), Method: 3},
				{Name: "b.mkv", Data: randBytes(11, 3000), Solid: true, Method: 5},
				{Name: "c.mkv", Data: randBytes(12, 3008), Encrypted: true},
				{Name: "d.mkv", Data: randBytes(13, 500)},
				{Name: "e.mkv", Data: randBytes(14, 500), Solid: true},
				{Name: "f.bin", Data: randBytes(15, 64), UnknownSize: true},
			}
			// Multi-volume, so a compressed member spans volumes too.
			vols := memVols(buildRAR(t, at.RAROptions{Version: ver, VolumeSize: 2000}, files)...)
			members, err := ListRAR(vols)
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != len(files) {
				t.Fatalf("got %d members", len(members))
			}
			wantMethod := fmt.Sprintf("rar%d:m3", ver)
			if m := members[0]; m.Stored || m.Method != wantMethod || !errors.Is(m.Streamable(), ErrCompressed) || len(m.Extents) != 2 {
				t.Fatalf("compressed member: %+v (%v)", m, m.Streamable())
			}
			if m := members[1]; !m.Solid || !errors.Is(m.Streamable(), ErrSolid) {
				t.Fatalf("solid member: %+v", m)
			}
			if m := members[2]; !m.Encrypted || !m.Stored || !errors.Is(m.Streamable(), ErrEncrypted) {
				t.Fatalf("encrypted member: %+v", m)
			}
			if _, err := NewMemberReader(members[2], vols); !errors.Is(err, ErrEncrypted) {
				t.Fatalf("NewMemberReader(encrypted) = %v", err)
			}
			if m := members[3]; m.Streamable() != nil {
				t.Fatalf("plain member not streamable: %v", m.Streamable())
			}
			if m := members[4]; !errors.Is(m.Streamable(), ErrSolid) {
				t.Fatalf("stored+solid member: %v", m.Streamable())
			}
			if m := members[5]; m.Size != 64 || m.Streamable() != nil {
				t.Fatalf("unknown-size member: %+v", m)
			}
			v, err := ReadRARVolume(vols[0].R, vols[0].Size, nil)
			if err != nil {
				t.Fatal(err)
			}
			if f := v.Files[0]; f.Stored || f.Encrypted || f.Solid || !f.SplitAfter {
				t.Fatalf("raw file: %+v", f)
			}
			if wantRaw := map[int]int{4: 0x33, 5: 3}[ver]; v.Files[0].Method != wantRaw {
				t.Fatalf("raw method %#x", v.Files[0].Method)
			}
			last := vols[len(vols)-1]
			lv, err := ReadRARVolume(last.R, last.Size, nil)
			if err != nil {
				t.Fatal(err)
			}
			if f := lv.Files[len(lv.Files)-1]; f.UnpackedSize != -1 {
				t.Fatalf("unknown size reported as %d", f.UnpackedSize)
			}
		})
	}
}

func TestRARHeaderEncrypted(t *testing.T) {
	for _, ver := range versions() {
		b := buildRAR(t, at.RAROptions{Version: ver, HeaderEncrypted: true}, sampleFiles())[0]
		if _, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil); !errors.Is(err, ErrEncrypted) {
			t.Errorf("rar%d: ReadRARVolume = %v, want ErrEncrypted", ver, err)
		}
		if _, err := ListRAR(memVols(b)); !errors.Is(err, ErrEncrypted) {
			t.Errorf("rar%d: ListRAR = %v, want ErrEncrypted", ver, err)
		}
	}
}

func TestRAR4UnicodeNames(t *testing.T) {
	for _, name := range []string{
		`dir\sub\file.mkv`,
		"Ünïcödé €uro.mkv",
		"日本語のファイル名.mkv",
		"mixed ascii run then Ж and ジ and 𝄞 clef.bin",
		"ab" + string(rune(0x100)) + "cdefghijklmnopqrstuvwxyz0123456789" + string(rune(0x4e00)),
	} {
		b := buildRAR(t, at.RAROptions{Version: 4}, []at.RARFile{{Name: name, Data: []byte("x")}})[0]
		v, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := name
		if name == `dir\sub\file.mkv` {
			want = "dir/sub/file.mkv"
		}
		if got := v.Files[0].Name; got != want {
			t.Errorf("name = %q, want %q", got, want)
		}
	}
	// UTF-8 form: LHD_UNICODE without a NUL separator.
	h := at.RAR4FileHeader(at.Header{Name: "plain.txt", UTF8Name: true, PackSize: 1, UnpackSize: 1})
	f, _, err := parseRAR4File(h, rar4LongBlock|rar4FileUnicode)
	if err != nil || f.Name != "plain.txt" {
		t.Fatalf("unicode name: %q, %v", f.Name, err)
	}
	if got := decodeRAR4Name([]byte("plain€.txt"), true); got != "plain€.txt" {
		t.Fatalf("utf-8 unicode name = %q", got)
	}
	// Run copy with a correction byte (opcode 3, high bit set).
	if got := decodeRAR4Unicode([]byte("ab\x00\x04\xc0\x80\x10"), []byte{0x04, 0xc0, 0x80, 0x10}); got != "ѱѲ" {
		t.Fatalf("correction decode = %q", got)
	}
	// Non-UTF-8 OEM names map bytes to runes instead of producing garbage.
	if got := decodeRAR4Name([]byte{'c', 0xe9, '.', 't'}, false); got != "cé.t" {
		t.Fatalf("latin-1 fallback = %q", got)
	}
}

func TestRARLargeSizesWithoutReadingData(t *testing.T) {
	const big = int64(5)<<30 + 12345 // > 4 GiB: needs 64-bit size fields
	for _, ver := range versions() {
		t.Run(fmt.Sprintf("rar%d", ver), func(t *testing.T) {
			s := newSparse(t)
			var end []byte
			if ver == 4 {
				s.add(at.RAR4Signature).add(at.RAR4MainHeader(0))
				s.add(at.RAR4FileHeader(at.Header{Name: "huge.mkv", PackSize: big, UnpackSize: big, CRC: 1}))
				s.hole(big)
				s.add(at.RAR4FileHeader(at.Header{Name: "tail.nfo", PackSize: 10, UnpackSize: 10, CRC: 2}))
				s.hole(10)
				end = at.RAR4EndHeader(false, -1)
			} else {
				s.add(at.RAR5Signature).add(at.RAR5MainHeader(false, 0, false))
				s.add(at.RAR5FileHeader(at.Header{Name: "huge.mkv", PackSize: big, UnpackSize: big, CRC: 1}))
				s.hole(big)
				s.add(at.RAR5FileHeader(at.Header{Name: "tail.nfo", PackSize: 10, UnpackSize: 10, CRC: 2}))
				s.hole(10)
				end = at.RAR5EndHeader(false)
			}
			s.add(end)
			members, err := ListRAR([]Volume{{R: s, Size: s.size}})
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != 2 || members[0].Size != big || members[0].Extents[0].Size != big || members[1].Size != 10 {
				t.Fatalf("members: %+v", members)
			}
			if got, want := members[1].Extents[0].Offset, s.size-int64(len(end))-10; got != want {
				t.Fatalf("tail offset %d, want %d", got, want)
			}
		})
	}
}

func TestRARNeverReadsFileData(t *testing.T) {
	for _, ver := range versions() {
		t.Run(fmt.Sprintf("rar%d", ver), func(t *testing.T) {
			bufs := buildRAR(t, at.RAROptions{Version: ver, VolumeSize: 6000, ServiceBlocks: true}, sampleFiles())
			for i, b := range bufs {
				rr := &recReader{r: bytes.NewReader(b)}
				v, err := ReadRARVolume(rr, int64(len(b)), nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range v.Files {
					rr.assertNoDataReads(t, f.DataOffset, f.PackedSize, f.Name)
				}
				if i == 0 {
					// The service block's data is skipped too.
					idx := int64(bytes.Index(b, []byte("archive comment")))
					rr.assertNoDataReads(t, idx, 15, "comment")
				}
				last := v.Files[len(v.Files)-1]
				if last.SplitAfter && rr.maxEnd() > last.DataOffset {
					// Nothing past the split file's header may be read: not its
					// data and not the end block.
					t.Fatalf("volume %d: read up to %d past split file data start %d", i, rr.maxEnd(), last.DataOffset)
				}
				// Each header is read once, in at most two exact chunks.
				if len(rr.reads) > 1+2*(len(v.Files)+5) {
					t.Fatalf("volume %d: %d reads for %d files", i, len(rr.reads), len(v.Files))
				}
			}
		})
	}
}

func TestRARMaxFiles(t *testing.T) {
	for _, ver := range versions() {
		b := buildRAR(t, at.RAROptions{Version: ver}, sampleFiles())[0]
		rr := &recReader{r: bytes.NewReader(b)}
		v, err := ReadRARVolume(rr, int64(len(b)), &RAROptions{MaxFiles: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Files) != 1 || v.EndFound {
			t.Fatalf("rar%d: MaxFiles=1 gave %d files, EndFound %v", ver, len(v.Files), v.EndFound)
		}
		if rr.maxEnd() > v.Files[0].DataOffset {
			t.Fatalf("rar%d: read up to %d, first data at %d", ver, rr.maxEnd(), v.Files[0].DataOffset)
		}
		v, err = ReadRARVolume(bytes.NewReader(b), int64(len(b)), &RAROptions{MaxFiles: 3})
		if err != nil || len(v.Files) != 3 {
			t.Fatalf("rar%d: MaxFiles=3: %v files, %v", ver, len(v.Files), err)
		}
		v, err = ReadRARVolume(bytes.NewReader(b), int64(len(b)), &RAROptions{})
		if err != nil || len(v.Files) != len(sampleFiles()) || !v.EndFound {
			t.Fatalf("rar%d: no limit: %v files, %v", ver, len(v.Files), err)
		}
	}
}

func TestRARComposeErrors(t *testing.T) {
	for _, ver := range versions() {
		t.Run(fmt.Sprintf("rar%d", ver), func(t *testing.T) {
			files := []at.RARFile{{Name: "big.mkv", Data: randBytes(20, 5000)}, {Name: "next.mkv", Data: randBytes(21, 100)}}
			bufs := buildRAR(t, at.RAROptions{Version: ver, VolumeSize: 1500}, files)
			if len(bufs) < 4 {
				t.Fatalf("want >= 4 volumes, got %d", len(bufs))
			}
			parsed := make([]*RARVolume, len(bufs))
			for i, b := range bufs {
				v, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil)
				if err != nil {
					t.Fatal(err)
				}
				parsed[i] = v
			}
			if _, err := ComposeRAR(parsed); err != nil {
				t.Fatal(err)
			}
			if _, err := ComposeRAR(parsed[:2]); !errors.Is(err, ErrMissingVolume) {
				t.Errorf("set ending early: %v", err)
			}
			if _, err := ComposeRAR(parsed[1:]); !errors.Is(err, ErrMissingVolume) {
				t.Errorf("first volume missing: %v", err)
			}
			gap := append([]*RARVolume{parsed[0]}, parsed[2:]...)
			if ver == 5 {
				// RAR5 volume numbers reveal the gap.
				if _, err := ComposeRAR(gap); !errors.Is(err, ErrMissingVolume) {
					t.Errorf("middle volume missing: %v", err)
				}
			} else if _, err := ComposeRAR(gap); err == nil {
				t.Errorf("middle volume missing: no error")
			}
			swapped := append([]*RARVolume{parsed[0], parsed[2], parsed[1]}, parsed[3:]...)
			// RAR4 headers carry no volume number before the end block (which
			// is not read after a split file), so only RAR5 can tell.
			if _, err := ComposeRAR(swapped); ver == 5 && err == nil {
				t.Errorf("swapped volumes: %v", err)
			}
			renamed := *parsed[1]
			renamed.Files = append([]RARFile(nil), parsed[1].Files...)
			renamed.Files[0].Name = "other.mkv"
			if _, err := ComposeRAR(append([]*RARVolume{parsed[0], &renamed}, parsed[2:]...)); !errors.Is(err, ErrCorrupt) {
				t.Errorf("continuation with a different name: %v", err)
			}
			short := *parsed[1]
			short.Files = append([]RARFile(nil), parsed[1].Files...)
			short.Files[0].PackedSize--
			if _, err := ComposeRAR(append([]*RARVolume{parsed[0], &short}, parsed[2:]...)); !errors.Is(err, ErrCorrupt) {
				t.Errorf("stored size mismatch: %v", err)
			}
			if _, err := ComposeRAR([]*RARVolume{parsed[0], nil}); !errors.Is(err, ErrMissingVolume) {
				t.Errorf("nil volume: %v", err)
			}
			if _, err := ListRAR([]Volume{{R: bytes.NewReader(bufs[0]), Size: int64(len(bufs[0]))}, {}}); !errors.Is(err, ErrMissingVolume) {
				t.Errorf("ListRAR nil reader: %v", err)
			}
		})
	}
}

func TestRARComposeIgnoresUnrelatedCompressed(t *testing.T) {
	vols := []*RARVolume{{
		Format: FormatRAR5, Number: 0, First: true,
		Files: []RARFile{
			{Name: "info.nfo", Method: 3, PackedSize: 10, UnpackedSize: 999, DataOffset: 100},
			{Name: "movie.mkv", Stored: true, PackedSize: 50, UnpackedSize: 50, DataOffset: 200},
		},
	}}
	members, err := ComposeRAR(vols)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].Size != 999 || members[1].Streamable() != nil {
		t.Fatalf("members: %+v", members)
	}
}

func TestRARCorruptAndTruncated(t *testing.T) {
	for _, ver := range versions() {
		t.Run(fmt.Sprintf("rar%d", ver), func(t *testing.T) {
			b := buildRAR(t, at.RAROptions{Version: ver, ServiceBlocks: true}, sampleFiles())[0]
			full, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil)
			if err != nil {
				t.Fatal(err)
			}
			// Every truncation either stops cleanly at a block boundary or
			// reports ErrCorrupt; it never panics.
			for cut := 0; cut < len(b); cut++ {
				v, err := ReadRARVolume(bytes.NewReader(b[:cut]), int64(cut), nil)
				switch {
				case cut < len(at.RAR4Signature):
					if !errors.Is(err, ErrNotArchive) {
						t.Fatalf("cut %d: %v", cut, err)
					}
				case err == nil:
					if v.EndFound {
						t.Fatalf("cut %d: end found in truncated volume", cut)
					}
				case !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrNotArchive):
					t.Fatalf("cut %d: %v", cut, err)
				}
			}
			// A declared size larger than the data makes reads come up short.
			if _, err := ReadRARVolume(bytes.NewReader(b[:len(b)-3]), int64(len(b)), nil); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("short reader: %v", err)
			}
			// Flipping any header byte is caught or still parses; never panics.
			for i := len(at.RAR4Signature); i < int(full.Files[0].DataOffset); i++ {
				c := bytes.Clone(b)
				c[i] ^= 0x41
				_, _ = ReadRARVolume(bytes.NewReader(c), int64(len(c)), nil)
			}
			// A flipped byte in a file name must fail the header CRC.
			c := bytes.Clone(b)
			c[bytes.Index(c, []byte("movie.nfo"))] ^= 1
			if _, err := ReadRARVolume(bytes.NewReader(c), int64(len(c)), nil); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("crc mismatch: %v", err)
			}
		})
	}
	if _, err := ReadRARVolume(bytes.NewReader([]byte("not a rar file at all")), 21, nil); !errors.Is(err, ErrNotArchive) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestRARReaderErrorsPropagate(t *testing.T) {
	for _, ver := range versions() {
		b := buildRAR(t, at.RAROptions{Version: ver, ServiceBlocks: true}, sampleFiles())[0]
		full, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil)
		if err != nil {
			t.Fatal(err)
		}
		points := []int64{0, 3, 9, 20, full.Files[0].HeaderOffset + 3, full.Files[1].HeaderOffset, int64(len(b)) - 2}
		for _, p := range points {
			_, err := ReadRARVolume(failReader{bytes.NewReader(b), p}, int64(len(b)), nil)
			if !errors.Is(err, errSentinel) {
				t.Fatalf("rar%d: fail at %d: %v, want sentinel", ver, p, err)
			}
			if errors.Is(err, ErrCorrupt) {
				t.Fatalf("rar%d: fail at %d: network error reported as corrupt: %v", ver, p, err)
			}
		}
		// io.EOF from the reader before a header is complete => ErrCorrupt.
		if _, err := ReadRARVolume(eofReader{bytes.NewReader(b), 30}, int64(len(b)), nil); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("rar%d: eof: %v", ver, err)
		}
		if _, err := ListRAR([]Volume{{R: failReader{bytes.NewReader(b), 0}, Size: int64(len(b))}}); !errors.Is(err, errSentinel) {
			t.Fatalf("rar%d: ListRAR: %v", ver, err)
		}
	}
}

// eofReader behaves as if the data ended at n.
type eofReader struct {
	r io.ReaderAt
	n int64
}

func (e eofReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= e.n {
		return 0, io.EOF
	}
	if off+int64(len(p)) > e.n {
		k, _ := e.r.ReadAt(p[:e.n-off], off)
		return k, io.ErrUnexpectedEOF
	}
	return e.r.ReadAt(p, off)
}

func TestMemberReaderErrors(t *testing.T) {
	data := randBytes(30, 100)
	vols := memVols(data)
	m := Member{Name: "x", Size: 100, Stored: true, Extents: []Extent{{0, 0, 60}, {0, 60, 40}}}
	r, err := NewMemberReader(m, vols)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadAt(make([]byte, 1), -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if n, err := r.ReadAt(nil, 100); n != 0 || err != nil {
		t.Fatalf("empty read at end: %d %v", n, err)
	}
	if n, err := r.ReadAt(make([]byte, 1), 100); n != 0 || err != io.EOF {
		t.Fatalf("read at end: %d %v", n, err)
	}
	bad := m
	bad.Extents = []Extent{{1, 0, 100}}
	if _, err := NewMemberReader(bad, vols); !errors.Is(err, ErrMissingVolume) {
		t.Fatalf("missing volume: %v", err)
	}
	bad.Extents = []Extent{{0, 50, 100}}
	if _, err := NewMemberReader(bad, vols); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("extent past volume: %v", err)
	}
	bad.Extents = []Extent{{0, 0, 50}}
	if _, err := NewMemberReader(bad, vols); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short extents: %v", err)
	}
	comp := m
	comp.Stored = false
	if _, err := NewMemberReader(comp, vols); !errors.Is(err, ErrCompressed) {
		t.Fatalf("compressed: %v", err)
	}
	// Errors from the underlying volume propagate.
	fr, err := NewMemberReader(m, []Volume{{R: failReader{bytes.NewReader(data), 70}, Size: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fr.ReadAt(make([]byte, 20), 65); !errors.Is(err, errSentinel) {
		t.Fatalf("member read error: %v", err)
	}
	// A volume shorter than declared yields io.ErrUnexpectedEOF, not io.EOF.
	sr, err := NewMemberReader(m, []Volume{{R: bytes.NewReader(data[:80]), Size: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.ReadAt(make([]byte, 30), 60); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short volume: %v", err)
	}
}

func TestStreamableOrder(t *testing.T) {
	m := Member{Name: "x", Encrypted: true, Solid: true, Dir: true}
	if !errors.Is(m.Streamable(), ErrEncrypted) {
		t.Fatal("encrypted first")
	}
	m.Encrypted = false
	if !errors.Is(m.Streamable(), ErrSolid) {
		t.Fatal("solid second")
	}
	m.Solid = false
	if !errors.Is(m.Streamable(), ErrCompressed) {
		t.Fatal("compressed third")
	}
	m.Stored = true
	if !errors.Is(m.Streamable(), ErrUnsupported) {
		t.Fatal("dir last")
	}
}
