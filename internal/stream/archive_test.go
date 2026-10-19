package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/braedonsaunders/ether/internal/archive/archivetest"
	"github.com/braedonsaunders/ether/internal/core"
)

// postVolumes posts archive volumes under names produced by name(i).
func postVolumes(t testing.TB, f *fakeFetcher, partSize int, vols [][]byte, name func(i int) string, extra ...postFile) []byte {
	t.Helper()
	var files []postFile
	for i, v := range vols {
		files = append(files, postFile{name: name(i), data: v})
	}
	return post(t, f, partSize, append(files, extra...)...)
}

func newStyle(base string) func(int) string {
	return func(i int) string { return fmt.Sprintf("%s.part%02d.rar", base, i+1) }
}

func oldStyle(base string) func(int) string {
	return func(i int) string {
		if i == 0 {
			return base + ".rar"
		}
		return fmt.Sprintf("%s.r%02d", base, i-1)
	}
}

func buildRAR(t testing.TB, files []archivetest.RARFile, opts archivetest.RAROptions) [][]byte {
	t.Helper()
	vols, err := archivetest.BuildRAR(files, opts)
	if err != nil {
		t.Fatal(err)
	}
	return vols
}

func TestRARStoreMultiVolume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		naming  func(string) func(int) string
	}{
		{"rar5-new", 5, newStyle},
		{"rar4-old", 4, oldStyle},
		{"rar4-new", 4, newStyle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFetcher()
			video := mkvBytes(21, 3_000_000)
			vols := buildRAR(t, []archivetest.RARFile{
				{Name: "Movie.2024.1080p/Movie.2024.1080p.mkv", Data: video},
			}, archivetest.RAROptions{Version: tc.version, VolumeSize: 400_000, NewNumbering: true})
			if len(vols) < 5 {
				t.Fatalf("only %d volumes", len(vols))
			}
			doc := postVolumes(t, f, 70_001, vols, tc.naming("Movie.2024.1080p"),
				postFile{name: "Movie.2024.1080p.nfo", data: []byte("nfo")},
				postFile{name: "Movie.2024.1080p.par2", data: randBytes(1, 10_000)})
			e := newTestEngine(t, f, nil, Options{})
			src := openSource(t, e, "rar", doc)
			if src.Name() != "Movie.2024.1080p.mkv" || src.Size() != int64(len(video)) {
				t.Fatalf("name %q size %d", src.Name(), src.Size())
			}
			// Open only touched the first article of volumes 1, 2 and last
			// (plus possibly the article holding the end of a header).
			if c := f.totalCalls(); c > 4 {
				t.Fatalf("Open fetched %d articles", c)
			}
			checkRandomReads(t, src, video, 99, 200)
			if got := readAll(t, src, 32<<10); !bytes.Equal(got, video) {
				t.Fatal("sequential mismatch")
			}
		})
	}
}

func TestRAR5VolumeNumberVintGrowth(t *testing.T) {
	// More than 128 volumes: from volume index 128 the RAR5 main header's
	// volume-number vint takes two bytes, shifting the data by one byte.
	f := newFakeFetcher()
	video := mkvBytes(22, 150*1500)
	vols := buildRAR(t, []archivetest.RARFile{{Name: "big.mkv", Data: video}}, archivetest.RAROptions{Version: 5, VolumeSize: 1600})
	if len(vols) <= 130 {
		t.Fatalf("only %d volumes", len(vols))
	}
	doc := postVolumes(t, f, 700, vols, newStyle("big"))
	e := newTestEngine(t, f, nil, Options{ReadAheadMin: 64 << 10})
	src := openSource(t, e, "vint", doc)
	if c := f.totalCalls(); c > 4 {
		t.Fatalf("Open fetched %d articles: uniform model was rejected", c)
	}
	if got := readAll(t, src, 4096); !bytes.Equal(got, video) {
		t.Fatal("mismatch")
	}
	checkRandomReads(t, src, video, 5, 100)
}

// buildIrregularRAR5 hand-builds a stored RAR5 set whose volume data sizes
// are given explicitly.
func buildIrregularRAR5(name string, data []byte, packs []int) [][]byte {
	var vols [][]byte
	off := 0
	for i, n := range packs {
		var b bytes.Buffer
		b.Write(archivetest.RAR5Signature)
		b.Write(archivetest.RAR5MainHeader(true, i, false))
		b.Write(archivetest.RAR5FileHeader(archivetest.Header{
			Name: name, PackSize: int64(n), UnpackSize: int64(len(data)),
			SplitBefore: i > 0, SplitAfter: i < len(packs)-1,
		}))
		b.Write(data[off : off+n])
		off += n
		b.Write(archivetest.RAR5EndHeader(i < len(packs)-1))
		vols = append(vols, b.Bytes())
	}
	if off != len(data) {
		panic("packs do not cover data")
	}
	return vols
}

func TestRARIrregularVolumesDetectedLazily(t *testing.T) {
	// Volumes 3 and 4 differ from the template but compensate, so the totals
	// add up and Open trusts the model; reading volume 3 must detect the
	// mismatch and rebuild from every volume before serving its bytes.
	f := newFakeFetcher()
	video := mkvBytes(23, 6*10_000+3_000)
	packs := []int{10_000, 10_000, 5_000, 15_000, 10_000, 10_000, 3_000}
	vols := buildIrregularRAR5("irr.mkv", video, packs)
	doc := postVolumes(t, f, 3_000, vols, newStyle("irr"))
	e := newTestEngine(t, f, nil, Options{ReadAheadParallel: 1, ReadAheadMin: 1})
	src := openSource(t, e, "irr", doc)
	if src.Size() != int64(len(video)) {
		t.Fatalf("size %d", src.Size())
	}
	// Read straight into volume 4's region (inner offset 27000 is in vol 4).
	buf := make([]byte, 4000)
	if n, err := src.ReadAt(buf, 27_000); err != nil || !bytes.Equal(buf[:n], video[27_000:31_000]) {
		t.Fatalf("read = %d %v", n, err)
	}
	checkRandomReads(t, src, video, 8, 200)
	if got := readAll(t, src, 1000); !bytes.Equal(got, video) {
		t.Fatal("mismatch")
	}
}

func TestRARTotalsMismatchFallsBackToExact(t *testing.T) {
	// Middle volumes smaller than the template: the sum check fails at Open,
	// which then reads every volume header.
	f := newFakeFetcher()
	video := mkvBytes(24, 10_000+10_000+4_000+4_000+2_000)
	vols := buildIrregularRAR5("sum.mkv", video, []int{10_000, 10_000, 4_000, 4_000, 2_000})
	doc := postVolumes(t, f, 3_000, vols, newStyle("sum"))
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "sum", doc)
	checkRandomReads(t, src, video, 9, 100)
}

func TestRARMultipleMembersFullScan(t *testing.T) {
	// Two episodes in one set: the larger starts mid-set.
	f := newFakeFetcher()
	ep1 := mkvBytes(25, 500_000)
	ep2 := mkvBytes(26, 900_000)
	vols := buildRAR(t, []archivetest.RARFile{
		{Name: "Show.S01E01.mkv", Data: ep1},
		{Name: "Show.S01E02.mkv", Data: ep2},
		{Name: "Show.nfo", Data: []byte("nfo")},
	}, archivetest.RAROptions{Version: 5, VolumeSize: 200_000})
	doc := postVolumes(t, f, 50_000, vols, newStyle("show"))
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "multi", doc)
	if src.Name() != "Show.S01E02.mkv" || src.Size() != int64(len(ep2)) {
		t.Fatalf("name %q size %d", src.Name(), src.Size())
	}
	checkRandomReads(t, src, ep2, 3, 100)
}

func TestRARSingleVolume(t *testing.T) {
	f := newFakeFetcher()
	video := mkvBytes(27, 250_000)
	vols := buildRAR(t, []archivetest.RARFile{
		{Name: "Movie.nfo", Data: []byte("hello")},
		{Name: "Movie.mkv", Data: video},
		{Name: "Sample/Movie.sample.mkv", Data: mkvBytes(1, 300_000)},
	}, archivetest.RAROptions{Version: 4})
	doc := postVolumes(t, f, 40_000, vols, func(int) string { return "Movie.rar" })
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "single", doc)
	if src.Name() != "Movie.mkv" {
		t.Fatalf("name %q", src.Name())
	}
	checkRandomReads(t, src, video, 4, 100)
}

func TestRARRejections(t *testing.T) {
	video := mkvBytes(28, 300_000)
	cases := []struct {
		name  string
		files []archivetest.RARFile
		opts  archivetest.RAROptions
		want  error
	}{
		{"compressed", []archivetest.RARFile{{Name: "m.mkv", Data: video, Method: 3}}, archivetest.RAROptions{Version: 5, VolumeSize: 100_000}, ErrCompressed},
		{"compressed-rar4", []archivetest.RARFile{{Name: "m.mkv", Data: video, Method: 5}}, archivetest.RAROptions{Version: 4}, ErrCompressed},
		{"file-encrypted", []archivetest.RARFile{{Name: "m.mkv", Data: video, Encrypted: true}}, archivetest.RAROptions{Version: 5, VolumeSize: 100_000}, ErrEncrypted},
		{"header-encrypted", []archivetest.RARFile{{Name: "m.mkv", Data: video}}, archivetest.RAROptions{Version: 5, HeaderEncrypted: true}, ErrEncrypted},
		{"header-encrypted-rar4", []archivetest.RARFile{{Name: "m.mkv", Data: video}}, archivetest.RAROptions{Version: 4, HeaderEncrypted: true}, ErrEncrypted},
		{"solid", []archivetest.RARFile{{Name: "a.nfo", Data: []byte("x")}, {Name: "m.mkv", Data: video, Solid: true}}, archivetest.RAROptions{Version: 5, SolidArchive: true}, ErrSolid},
		{"no-video", []archivetest.RARFile{{Name: "a.nfo", Data: []byte("x")}, {Name: "b.txt", Data: video}}, archivetest.RAROptions{Version: 5}, ErrNoPlayableFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFetcher()
			vols := buildRAR(t, tc.files, tc.opts)
			doc := postVolumes(t, f, 30_000, vols, newStyle("rej"))
			e := newTestEngine(t, f, nil, Options{})
			_, err := e.Open(context.Background(), "rej", doc)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open err = %v, want %v", err, tc.want)
			}
			if !IsRejection(err) {
				t.Fatalf("%v is not a rejection", err)
			}
			ok, err := e.Check(context.Background(), doc)
			if ok || !errors.Is(err, tc.want) {
				t.Fatalf("Check = %v %v", ok, err)
			}
		})
	}
}

func TestRARMissingArticleInVolume(t *testing.T) {
	f := newFakeFetcher()
	video := mkvBytes(29, 1_000_000)
	vols := buildRAR(t, []archivetest.RARFile{{Name: "m.mkv", Data: video}}, archivetest.RAROptions{Version: 5, VolumeSize: 300_000})
	doc := postVolumes(t, f, 50_000, vols, newStyle("miss"))
	f.setMissing(postID("miss.part02.rar", 3))
	e := newTestEngine(t, f, nil, Options{ReadAheadParallel: 1})
	src := openSource(t, e, "miss", doc)
	_, err := src.ReadAt(make([]byte, 1_000_000), 0)
	if !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("err = %v", err)
	}
	ok, err := e.Check(context.Background(), doc)
	_ = ok // the sample may or may not hit the dead article; Check must not panic
	_ = err
}

func cut(data []byte, size int) [][]byte {
	var out [][]byte
	for off := 0; off < len(data); off += size {
		out = append(out, data[off:min(off+size, len(data))])
	}
	return out
}

func Test7zCopyMultiVolume(t *testing.T) {
	f := newFakeFetcher()
	video := mkvBytes(30, 2_000_000)
	arc := archivetest.Build7zCopy([]archivetest.SevenZipFile{
		{Name: "Movie.nfo", Data: []byte("nfo")},
		{Name: "Movie.2024.mkv", Data: video},
	}, false)
	vols := cut(arc, 300_000)
	doc := postVolumes(t, f, 60_000, vols, func(i int) string { return fmt.Sprintf("Movie.2024.7z.%03d", i+1) })
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "7z", doc)
	if src.Name() != "Movie.2024.mkv" || src.Size() != int64(len(video)) {
		t.Fatalf("name %q size %d", src.Name(), src.Size())
	}
	if c := f.totalCalls(); c > 4 {
		t.Fatalf("Open fetched %d articles", c)
	}
	checkRandomReads(t, src, video, 31, 150)
	if got := readAll(t, src, 32<<10); !bytes.Equal(got, video) {
		t.Fatal("mismatch")
	}
}

func Test7zSolidCopyFolder(t *testing.T) {
	f := newFakeFetcher()
	video := mkvBytes(32, 700_000)
	arc := archivetest.Build7zCopy([]archivetest.SevenZipFile{
		{Name: "a.nfo", Data: []byte("nfo")},
		{Name: "dir", Dir: true},
		{Name: "dir/Movie.mkv", Data: video},
		{Name: "z.srt", Data: []byte("subs")},
	}, true)
	doc := postVolumes(t, f, 100_000, [][]byte{arc}, func(int) string { return "Movie.7z" })
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "7zs", doc)
	checkRandomReads(t, src, video, 1, 100)
}

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", rel))
	if err != nil {
		t.Skipf("fixture %s: %v", rel, err)
	}
	return b
}

// Real 7-Zip output (see internal/archive/testdata/gen7z.sh).
func Test7zFixtures(t *testing.T) {
	want := readFixture(t, "7z/rand_a.bin")
	f := newFakeFetcher()
	var vols [][]byte
	for i := 1; ; i++ {
		b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "7z", fmt.Sprintf("store_vol.7z.%03d", i)))
		if err != nil {
			break
		}
		vols = append(vols, b)
	}
	if len(vols) < 2 {
		t.Skip("multi-volume fixture missing")
	}
	doc := postVolumes(t, f, 7_000, vols, func(i int) string { return fmt.Sprintf("Fixture.7z.%03d", i+1) })
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "7zfix", doc)
	if src.Name() != "rand_a.bin" {
		t.Fatalf("name %q", src.Name())
	}
	if got := readAll(t, src, 5000); !bytes.Equal(got, want) {
		t.Fatal("mismatch")
	}

	for _, tc := range []struct {
		file string
		want error
	}{
		{"7z/compressed.7z", ErrCompressed},
		{"7z/enc_content.7z", ErrEncrypted},
		{"7z/enc_header.7z", ErrEncrypted},
	} {
		b := readFixture(t, tc.file)
		f := newFakeFetcher()
		doc := postVolumes(t, f, 5_000, [][]byte{b}, func(int) string { return "Fixture.7z" })
		e := newTestEngine(t, f, nil, Options{})
		if _, err := e.Open(context.Background(), tc.file, doc); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.file, err, tc.want)
		}
	}
}

// Real WinRAR RAR5 output, from AltMount's fixtures (MIT).
func TestRARFixtures(t *testing.T) {
	want := readFixture(t, "altmount/payload_a.bin")
	p1 := readFixture(t, "altmount/archive.part01.rar")
	p2 := readFixture(t, "altmount/archive.part02.rar")
	f := newFakeFetcher()
	doc := postVolumes(t, f, 11_000, [][]byte{p1, p2}, newStyle("archive"))
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "rarfix", doc)
	if src.Size() != int64(len(want)) {
		t.Fatalf("size %d, want %d", src.Size(), len(want))
	}
	if got := readAll(t, src, 4096); !bytes.Equal(got, want) {
		t.Fatal("mismatch")
	}
	for file, wantErr := range map[string]error{
		"altmount/compressed.rar": ErrCompressed,
		"altmount/encrypted.rar":  ErrEncrypted,
	} {
		b := readFixture(t, file)
		f := newFakeFetcher()
		doc := postVolumes(t, f, 5_000, [][]byte{b}, func(int) string { return "x.rar" })
		e := newTestEngine(t, f, nil, Options{})
		_, err := e.Open(context.Background(), file, doc)
		if !errors.Is(err, wantErr) && !errors.Is(err, ErrNoPlayableFile) {
			t.Errorf("%s: err = %v, want %v", file, err, wantErr)
		}
	}
}

func TestObfuscatedRARSetSniffed(t *testing.T) {
	f := newFakeFetcher()
	video := mkvBytes(33, 1_200_000)
	vols := buildRAR(t, []archivetest.RARFile{{Name: "Real.Name.mkv", Data: video}}, archivetest.RAROptions{Version: 5, VolumeSize: 300_000})
	// Random names, no extensions, yEnc names equally random; posted out of
	// order but with "[n/N]" subject counters.
	var files []postFile
	for i := len(vols) - 1; i >= 0; i-- {
		name := fmt.Sprintf("f%02dq9Xk3Pq9LmZr7Tb2Wc8Hn4", i)
		files = append(files, postFile{name: name, data: vols[i],
			subject: fmt.Sprintf(`[%d/%d] - "%s" yEnc (1/%d)`, i+1, len(vols), name, (len(vols[i])+49_999)/50_000)})
	}
	doc := post(t, f, 50_000, files...)
	e := newTestEngine(t, f, nil, Options{})
	src := openSource(t, e, "obfrar", doc)
	if src.Name() != "Real.Name.mkv" {
		t.Fatalf("name %q", src.Name())
	}
	checkRandomReads(t, src, video, 2, 100)
}
