package par2_test

import (
	"bytes"
	"crypto/md5"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/braedonsaunders/ether/internal/par2"
	"github.com/braedonsaunders/ether/internal/par2/par2test"
)

func rnd(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func sample() []par2test.File {
	return []par2test.File{
		{Name: "Movie.2024.1080p.mkv", Data: rnd(1, 100_000)},
		{Name: "Movie.2024.1080p.nfo", Data: []byte("tiny nfo")},
		{Name: "sub/Movie.2024.srt", Data: rnd(3, 20_000)},
	}
}

func TestParseIndex(t *testing.T) {
	files := sample()
	data, setID := par2test.Index(4096, files)
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 3 || s.Creator != "par2test" || s.Skipped != 0 {
		t.Fatalf("files %d creator %q skipped %d", len(s.Files), s.Creator, s.Skipped)
	}
	for i, f := range s.Files {
		want := files[i]
		if f.Name != want.Name || f.Length != int64(len(want.Data)) || f.SetID != setID || f.SliceSize != 4096 {
			t.Fatalf("file %d = %+v", i, f)
		}
		if f.MD5 != md5.Sum(want.Data) || f.ID != par2test.FileID(want.Name, want.Data) {
			t.Fatalf("file %d hashes", i)
		}
		if wantSlices := (len(want.Data) + 4095) / 4096; len(f.Slices) != wantSlices {
			t.Fatalf("file %d slices %d want %d", i, len(f.Slices), wantSlices)
		}
	}
	if got := s.Files[2].BaseName(); got != "Movie.2024.srt" {
		t.Fatalf("base %q", got)
	}
	if got := s.Named("movie.2024.SRT"); len(got) != 1 || got[0] != s.Files[2] {
		t.Fatalf("Named fold = %v", got)
	}
	if got := s.Named("Movie.2024.1080p.mkv"); len(got) != 1 || got[0] != s.Files[0] {
		t.Fatalf("Named = %v", got)
	}
}

func TestMatch16kAndIdentify(t *testing.T) {
	files := sample()
	data, _ := par2test.Index(4096, files)
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	mkv := files[0].Data
	sum, ok := par2.Hash16k(mkv[:20_000], int64(len(mkv)))
	if !ok {
		t.Fatal("hash16k needs only 16 KiB")
	}
	if m := s.Match16k(sum); len(m) != 1 || m[0].Name != files[0].Name {
		t.Fatalf("Match16k = %v", m)
	}
	if _, ok := par2.Hash16k(mkv[:1000], int64(len(mkv))); ok {
		t.Fatal("short prefix hashed")
	}
	// Small file: the hash covers the whole file.
	if f, byHash := s.Identify([]byte("tiny nfo"), 8); f == nil || !byHash || f.Name != files[1].Name {
		t.Fatalf("Identify nfo = %v %v", f, byHash)
	}
	// Hash from the first article, size from its yEnc header.
	if f, byHash := s.Identify(mkv[:30_000], int64(len(mkv))); f == nil || !byHash || f != s.Files[0] {
		t.Fatalf("Identify mkv = %v %v", f, byHash)
	}
	// Unknown size: hash alone.
	if f, byHash := s.Identify(mkv[:30_000], 0); f != s.Files[0] || !byHash {
		t.Fatalf("Identify no size = %v %v", f, byHash)
	}
	// Too short to hash: a unique length decides.
	if f, byHash := s.Identify(mkv[:100], int64(len(mkv))); f != s.Files[0] || byHash {
		t.Fatalf("Identify by length = %v %v", f, byHash)
	}
	// Wrong content of a unique length is still identified (by length) so
	// the caller can report the mismatch.
	bad := bytes.Clone(mkv)
	bad[5] ^= 1
	f, byHash := s.Identify(bad, int64(len(bad)))
	if f != s.Files[0] || byHash {
		t.Fatalf("Identify corrupt = %v %v", f, byHash)
	}
	var mm *par2.MismatchError
	if _, err := f.Verify(bad[:20_000], int64(len(bad))); !errors.As(err, &mm) || mm.What != "first 16 KiB MD5" {
		t.Fatalf("Verify corrupt = %v", err)
	}
	// Nothing matches.
	if f, _ := s.Identify(rnd(9, 30_000), 12345); f != nil {
		t.Fatalf("Identify foreign = %v", f)
	}
}

func TestIdentifyAmbiguousLength(t *testing.T) {
	// Equal-sized volumes: length cannot decide, the hash must.
	a, b := rnd(10, 50_000), rnd(11, 50_000)
	data, _ := par2test.Index(1024, []par2test.File{{Name: "x.part1.rar", Data: a}, {Name: "x.part2.rar", Data: b}})
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if f, byHash := s.Identify(b[:20_000], 50_000); f == nil || f.Name != "x.part2.rar" || !byHash {
		t.Fatalf("by hash = %v", f)
	}
	if f, _ := s.Identify(b[:100], 50_000); f != nil {
		t.Fatalf("ambiguous length matched %v", f)
	}
}

func TestVerifySlices(t *testing.T) {
	d := rnd(4, 10_000) // 3 slices of 4096, the last zero-padded
	data, _ := par2test.Index(4096, []par2test.File{{Name: "v.mkv", Data: d}})
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	f := s.Files[0]
	// Whole file: length, 16k, three slices, whole MD5.
	if n, err := f.Verify(d, int64(len(d))); err != nil || n != 6 {
		t.Fatalf("Verify whole = %d %v", n, err)
	}
	// A prefix covering one slice (the 16k hash needs the whole small file).
	if n, err := f.Verify(d[:5000], 0); err != nil || n != 1 {
		t.Fatalf("Verify prefix = %d %v", n, err)
	}
	bad := bytes.Clone(d)
	bad[4100] ^= 0xff // slice 1
	var mm *par2.MismatchError
	if _, err := f.Verify(bad[:8192], 0); !errors.As(err, &mm) || mm.What != "slice 1 CRC32" {
		t.Fatalf("Verify bad slice = %v", err)
	}
	if _, err := f.Verify(d[:100], 9999); !errors.As(err, &mm) || mm.What != "length" {
		t.Fatalf("Verify length = %v", err)
	}
	if n, err := f.Verify(d[:100], 0); err != nil || n != 0 {
		t.Fatalf("Verify too short = %d %v", n, err)
	}
}

func TestCorruptTruncatedAndJunk(t *testing.T) {
	files := sample()
	idx, _ := par2test.Index(4096, files)
	// Locate packets to damage the FileDesc of the nfo.
	var pkts [][]byte
	for rest := idx; len(rest) > 0; {
		n := int(uint64(rest[8]) | uint64(rest[9])<<8 | uint64(rest[10])<<16 | uint64(rest[11])<<24)
		pkts = append(pkts, rest[:n])
		rest = rest[n:]
	}
	// Order: main, (desc, ifsc) x3, creator.
	corrupt := bytes.Clone(pkts[3])
	corrupt[len(corrupt)-1] ^= 1
	var data []byte
	data = append(data, []byte("garbage before PAR2")...)
	data = append(data, pkts[0]...)
	data = append(data, pkts[1]...)
	data = append(data, 0, 1, 2) // junk between packets
	data = append(data, pkts[2]...)
	data = append(data, corrupt...)
	data = append(data, []byte(par2.Magic)...) // a lone magic
	data = append(data, pkts[5]...)
	data = append(data, pkts[6]...)
	data = append(data, pkts[1]...) // repeated packet
	data = append(data, pkts[7]...)
	data = append(data, pkts[7][:40]...) // truncated tail
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 2 || s.Files[0].Name != files[0].Name || s.Files[1].Name != files[2].Name {
		t.Fatalf("files = %v", s.SortedNames())
	}
	if s.Skipped < 3 {
		t.Fatalf("skipped = %d", s.Skipped)
	}
	if _, err := par2.Parse([]byte("not a par2 file at all")); !errors.Is(err, par2.ErrNoFiles) {
		t.Fatalf("garbage err = %v", err)
	}
	if _, err := par2.Parse(nil); !errors.Is(err, par2.ErrNoFiles) {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := par2.Parse(idx[:50]); !errors.Is(err, par2.ErrNoFiles) {
		t.Fatalf("truncated err = %v", err)
	}
	// A bad length field (not a multiple of 4, or past the end).
	bad := bytes.Clone(pkts[1])
	bad[8]++
	if _, err := par2.Parse(bad); !errors.Is(err, par2.ErrNoFiles) {
		t.Fatalf("bad length err = %v", err)
	}
}

func TestMultipleRecoverySets(t *testing.T) {
	a, _ := par2test.Index(4096, []par2test.File{{Name: "a.mkv", Data: rnd(5, 30_000)}})
	b, setB := par2test.Index(8192, []par2test.File{{Name: "b.mkv", Data: rnd(6, 40_000)}})
	s, err := par2.Parse(append(append([]byte{}, a...), b...))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 2 || s.Files[1].SetID != setB || s.Files[1].SliceSize != 8192 || s.Files[0].SliceSize != 4096 {
		t.Fatalf("files = %+v", s.Files)
	}
}

func TestIFSCWithoutMainAndUnicodeName(t *testing.T) {
	d := rnd(7, 9000)
	id := par2test.FileID("raw", d)
	var set par2.ID
	data := par2test.Packet(set, par2.TypeFileDesc, par2test.FileDescBody(id, "raw", d))
	data = append(data, par2test.Packet(set, par2.TypeIFSC, par2test.IFSCBody(id, 4096, d))...)
	data = append(data, par2test.Packet(set, par2.TypeUniName, par2test.UniNameBody(id, "Fïlm – 2024.mkv"))...)
	data = append(data, par2test.Packet(set, "PAR 2.0\x00Unknown!", []byte("xxxx"))...)
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	f := s.Files[0]
	if f.Name != "Fïlm – 2024.mkv" {
		t.Fatalf("name %q", f.Name)
	}
	// Without a Main packet the slice size is unknown: no slice checks.
	if f.SliceSize != 0 || f.Slices != nil {
		t.Fatalf("slices without main: %d %d", f.SliceSize, len(f.Slices))
	}
	if s.Skipped != 1 {
		t.Fatalf("unknown packet not skipped: %d", s.Skipped)
	}
}

func TestLatin1Name(t *testing.T) {
	d := []byte("data")
	id := par2test.FileID("x", d)
	data := par2test.Packet(par2.ID{}, par2.TypeFileDesc, par2test.FileDescBody(id, "caf\xe9\x01.mkv", d))
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Files[0].Name != "café.mkv" {
		t.Fatalf("name %q", s.Files[0].Name)
	}
}

func FuzzParse(f *testing.F) {
	idx, _ := par2test.Index(4096, sample())
	f.Add(idx)
	f.Add([]byte(par2.Magic))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := par2.Parse(data)
		if err == nil && len(s.Files) == 0 {
			t.Fatal("no files without error")
		}
	})
}

// A hostile Main packet may announce a slice size of up to 1 TiB; verifying
// a short file's single (padded) slice must not allocate SliceSize bytes.
func TestVerifyHugeSliceSizeDoesNotAllocate(t *testing.T) {
	d := rnd(9, 100)
	id := par2test.FileID("x.mkv", d)
	const huge = int64(1) << 40
	main := par2test.MainBody(huge, []par2.ID{id})
	setID := par2.ID(md5.Sum(main))
	ifsc := append(append([]byte{}, id[:]...), make([]byte, 20)...) // one bogus slice
	var data []byte
	data = append(data, par2test.Packet(setID, par2.TypeMain, main)...)
	data = append(data, par2test.Packet(setID, par2.TypeFileDesc, par2test.FileDescBody(id, "x.mkv", d))...)
	data = append(data, par2test.Packet(setID, par2.TypeIFSC, ifsc)...)
	s, err := par2.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	f := s.Files[0]
	if f.SliceSize != huge || len(f.Slices) != 1 {
		t.Fatalf("slice size %d, %d slices", f.SliceSize, len(f.Slices))
	}
	if n, err := f.Verify(d, int64(len(d))); err != nil || n != 3 {
		t.Fatalf("Verify = %d %v", n, err)
	}
}
