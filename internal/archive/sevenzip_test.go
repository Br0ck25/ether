package archive

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	at "github.com/ether-media/ether/internal/archive/archivetest"
)

// Fixtures in testdata/7z are produced by testdata/gen7z.sh with 7zz.

func list7zFixture(t *testing.T, name string) ([]Member, []byte) {
	t.Helper()
	b := readFixture(t, "7z", name)
	rr := &recReader{r: bytes.NewReader(b)}
	members, err := List7z(rr, int64(len(b)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	// Only the signature header and the (packed) header are read.
	for _, m := range members {
		for _, e := range m.Extents {
			rr.assertNoDataReads(t, e.Offset, e.Size, m.Name)
		}
	}
	if len(rr.reads) > 3 {
		t.Fatalf("%s: %d reads", name, len(rr.reads))
	}
	return members, b
}

func byName(members []Member) map[string]Member {
	out := make(map[string]Member, len(members))
	for _, m := range members {
		out[m.Name] = m
	}
	return out
}

func assertStored(t *testing.T, m Member, vols []Volume, want []byte) {
	t.Helper()
	if m.Streamable() != nil || !m.Stored || m.Size != int64(len(want)) || m.Method != "copy" {
		t.Fatalf("member %+v (streamable: %v), want stored %d bytes", m, m.Streamable(), len(want))
	}
	mr, err := NewMemberReader(m, vols)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, mr, mr.Size()), want) {
		t.Fatalf("%q: payload mismatch", m.Name)
	}
	checkRandomReads(t, mr, want, extentCuts(m), uint64(len(want)))
}

func TestList7zStoreSingle(t *testing.T) {
	members, b := list7zFixture(t, "store_single.7z")
	if len(members) != 1 || members[0].Name != "rand_a.bin" {
		t.Fatalf("members: %+v", members)
	}
	assertStored(t, members[0], memVols(b), readFixture(t, "7z", "rand_a.bin"))
}

func TestList7zStoreMulti(t *testing.T) {
	for _, name := range []string{"store_multi.7z", "store_plainhdr.7z"} {
		t.Run(name, func(t *testing.T) {
			members, b := list7zFixture(t, name)
			if name == "store_plainhdr.7z" {
				// Header is a plain kHeader, not kEncodedHeader.
				if hdr := b[len(b)-int(leU64(b[20:]))]; hdr != sz7Header {
					t.Fatalf("header byte %#x", hdr)
				}
			}
			m := byName(members)
			vols := memVols(b)
			assertStored(t, m["rand_b.bin"], vols, readFixture(t, "7z", "rand_b.bin"))
			assertStored(t, m["sub/rand_c.bin"], vols, readFixture(t, "7z", "rand_c.bin"))
			if d := m["sub"]; !d.Dir || d.Size != 0 || !errors.Is(d.Streamable(), ErrUnsupported) {
				t.Fatalf("sub dir: %+v", d)
			}
			if name == "store_plainhdr.7z" {
				if len(members) != 3 {
					t.Fatalf("members: %+v", members)
				}
				return
			}
			if len(members) != 6 {
				t.Fatalf("members: %+v", members)
			}
			assertStored0(t, m["empty.txt"], vols)
			if d := m["emptydir"]; !d.Dir {
				t.Fatalf("emptydir: %+v", d)
			}
			assertStored(t, m["rand_a.bin"], vols, readFixture(t, "7z", "rand_a.bin"))
		})
	}
}

func leU64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func assertStored0(t *testing.T, m Member, vols []Volume) {
	t.Helper()
	if m.Dir || m.Size != 0 || len(m.Extents) != 0 || m.Streamable() != nil {
		t.Fatalf("empty file: %+v", m)
	}
	mr, err := NewMemberReader(m, vols)
	if err != nil || mr.Size() != 0 {
		t.Fatalf("empty reader: %v", err)
	}
}

func TestList7zEncodedHeaderManyFiles(t *testing.T) {
	members, b := list7zFixture(t, "store_many.7z")
	// The header is LZMA-encoded (kEncodedHeader + 030101 coder).
	hdrOff := 32 + int(leU64(b[12:]))
	if b[hdrOff] != sz7EncodedHeader || !bytes.Contains(b[hdrOff:], []byte{0x23, 0x03, 0x01, 0x01, 0x05}) {
		t.Fatalf("fixture header is not LZMA-encoded: % x", b[hdrOff:hdrOff+16])
	}
	if len(members) != 200 {
		t.Fatalf("%d members", len(members))
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	vols := memVols(b)
	for i, m := range members {
		num := fmt.Sprintf("%03d", i)
		if !strings.HasSuffix(m.Name, "f"+num+".txt") {
			t.Fatalf("member %d name %q", i, m.Name)
		}
		assertStored(t, m, vols, []byte("small file "+num+"\n"))
	}
}

func TestList7zMultiVolume(t *testing.T) {
	var bufs [][]byte
	for _, n := range []string{"store_vol.7z.001", "store_vol.7z.002", "store_vol.7z.003"} {
		bufs = append(bufs, readFixture(t, "7z", n))
	}
	vols := memVols(bufs...)
	mr := NewMultiReader(vols)
	if mr.Size() != int64(len(bufs[0])+len(bufs[1])+len(bufs[2])) {
		t.Fatalf("MultiReader size %d", mr.Size())
	}
	joined := bytes.Join(bufs, nil)
	checkRandomReads(t, mr, joined, []int64{int64(len(bufs[0])), int64(len(bufs[0]) + len(bufs[1]))}, 3)
	members, err := List7z(mr, mr.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members: %+v", members)
	}
	m := byName(members)
	whole := []Volume{{R: mr, Size: mr.Size()}}
	wantA := readFixture(t, "7z", "rand_a.bin")
	assertStored(t, m["rand_a.bin"], whole, wantA)
	assertStored(t, m["rand_b.bin"], whole, readFixture(t, "7z", "rand_b.bin"))

	// Map the concatenated extents back onto the individual volumes.
	split, err := SplitExtents(vols, m["rand_a.bin"].Extents)
	if err != nil {
		t.Fatal(err)
	}
	if len(split) != 2 || split[0].Volume != 0 || split[1].Volume != 1 || split[1].Offset != 0 {
		t.Fatalf("split extents: %+v", split)
	}
	perVol := m["rand_a.bin"]
	perVol.Extents = split
	assertStored(t, perVol, vols, wantA)
	if _, err := SplitExtents(vols[:1], m["rand_b.bin"].Extents); !errors.Is(err, ErrMissingVolume) {
		t.Fatalf("SplitExtents past end: %v", err)
	}
	if _, err := SplitExtents(vols, []Extent{{Volume: 1}}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("SplitExtents bad volume: %v", err)
	}
	// Without the last volume the header is unreachable.
	if _, err := List7z(NewMultiReader(vols[:2]), int64(len(bufs[0])+len(bufs[1]))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing last volume: %v", err)
	}
}

func TestList7zCompressedAndEncrypted(t *testing.T) {
	members, _ := list7zFixture(t, "compressed.7z")
	m := byName(members)
	for _, n := range []string{"rand_b.bin", "sub/rand_c.bin"} {
		if x := m[n]; x.Stored || x.Method != "lzma2" || len(x.Extents) != 0 || !errors.Is(x.Streamable(), ErrCompressed) {
			t.Fatalf("%s: %+v", n, x)
		}
	}
	if m["rand_b.bin"].Size != 33333 || m["sub/rand_c.bin"].Size != 5000 || !m["sub"].Dir {
		t.Fatalf("compressed sizes: %+v", members)
	}

	members, _ = list7zFixture(t, "bcj_lzma.7z")
	if len(members) != 1 || members[0].Size != 5000 || !strings.Contains(members[0].Method, "bcj") ||
		!strings.Contains(members[0].Method, "lzma") || !errors.Is(members[0].Streamable(), ErrCompressed) {
		t.Fatalf("bcj: %+v", members)
	}

	members, b := list7zFixture(t, "enc_content.7z")
	if len(members) != 1 || members[0].Name != "rand_b.bin" || !members[0].Encrypted ||
		!strings.Contains(members[0].Method, "aes") || !errors.Is(members[0].Streamable(), ErrEncrypted) {
		t.Fatalf("encrypted content: %+v", members)
	}
	if _, err := NewMemberReader(members[0], memVols(b)); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("NewMemberReader(encrypted): %v", err)
	}

	b = readFixture(t, "7z", "enc_header.7z")
	if _, err := List7z(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("encrypted header: %v", err)
	}
}

func TestList7zSolidCopyFolder(t *testing.T) {
	files := []at.SevenZipFile{
		{Name: "a.mkv", Data: randBytes(40, 7000)},
		{Name: "dir", Dir: true},
		{Name: "dir/b.nfo", Data: randBytes(41, 123)},
		{Name: "empty"},
		{Name: "c.bin", Data: randBytes(42, 1)},
		{Name: "ünï.mkv", Data: randBytes(43, 999)},
	}
	for _, solid := range []bool{true, false} {
		b := at.Build7zCopy(files, solid)
		members, err := List7z(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("solid=%v: %v", solid, err)
		}
		if len(members) != len(files) {
			t.Fatalf("solid=%v: %+v", solid, members)
		}
		vols := memVols(b)
		for i, f := range files {
			m := members[i]
			if m.Name != f.Name || m.Dir != f.Dir {
				t.Fatalf("member %d: %+v", i, m)
			}
			switch {
			case f.Dir:
			case len(f.Data) == 0:
				assertStored0(t, m, vols)
			default:
				assertStored(t, m, vols, f.Data)
			}
		}
	}
	// Empty archive (no files at all).
	b := at.Build7zCopy(nil, false)
	if members, err := List7z(bytes.NewReader(b), int64(len(b))); err != nil || len(members) != 0 {
		t.Fatalf("empty: %v %v", members, err)
	}
}

func Test7zNumber(t *testing.T) {
	for _, v := range []uint64{0, 1, 0x7f, 0x80, 0x3fff, 0x4000, 0x1fffff, 0x200000, 1 << 32, 1<<56 - 1, 1 << 56, 1<<64 - 1} {
		enc := at.Number7z(v)
		b := sz7Buf{b: enc}
		if got := b.number(); got != v || b.err != nil || len(b.b) != 0 {
			t.Fatalf("number(% x) = %d (err %v, left %d), want %d", enc, got, b.err, len(b.b), v)
		}
	}
	b := sz7Buf{b: []byte{0xc0, 0x01}}
	if b.number(); !errors.Is(b.err, ErrCorrupt) {
		t.Fatalf("truncated number: %v", b.err)
	}
}

func TestList7zCorrupt(t *testing.T) {
	for _, name := range []string{"store_many.7z", "store_plainhdr.7z", "bcj_lzma.7z", "enc_header.7z"} {
		b := readFixture(t, "7z", name)
		hdrStart := 32 + int(leU64(b[12:]))
		// Truncations never panic; ones that cut the header are ErrCorrupt.
		for cut := 0; cut < len(b); cut += 1 + cut/200 {
			_, err := List7z(bytes.NewReader(b[:cut]), int64(cut))
			if cut < 6 {
				if !errors.Is(err, ErrNotArchive) {
					t.Fatalf("%s cut %d: %v", name, cut, err)
				}
			} else if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s cut %d: %v", name, cut, err)
			}
		}
		// Byte flips in the signature header or header fail the CRCs.
		for _, i := range []int{8, 13, 20, 28, hdrStart, len(b) - 1} {
			c := bytes.Clone(b)
			c[i] ^= 0x10
			if _, err := List7z(bytes.NewReader(c), int64(len(c))); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s flip %d: %v", name, i, err)
			}
		}
		// Packed header bytes (encoded headers) are covered by the decoded
		// CRC or the decoder; flips never panic.
		for i := 32; i < hdrStart; i++ {
			c := bytes.Clone(b)
			c[i] ^= 0x5a
			_, _ = List7z(bytes.NewReader(c), int64(len(c)))
		}
	}
	b := readFixture(t, "7z", "store_single.7z")
	c := bytes.Clone(b)
	c[6] = 1
	if _, err := List7z(bytes.NewReader(c), int64(len(c))); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("major version 1: %v", err)
	}
	if _, err := List7z(bytes.NewReader([]byte("Rar!\x1a\x07\x00 not 7z at all......................")), 40); !errors.Is(err, ErrNotArchive) {
		t.Fatalf("not 7z: %v", err)
	}
	if _, err := List7z(bytes.NewReader(b[:20]), 20); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short signature header: %v", err)
	}
}

func TestList7zReaderErrorsPropagate(t *testing.T) {
	for _, name := range []string{"store_single.7z", "store_many.7z"} {
		b := readFixture(t, "7z", name)
		hdrStart := int64(32 + leU64(b[12:]))
		for _, p := range []int64{0, 10, 33, hdrStart + 1, int64(len(b)) - 1} {
			_, err := List7z(failReader{bytes.NewReader(b), p}, int64(len(b)))
			if !errors.Is(err, errSentinel) || errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s fail at %d: %v", name, p, err)
			}
		}
	}
}
