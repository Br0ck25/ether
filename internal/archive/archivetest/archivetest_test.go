package archivetest_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ether-media/ether/internal/archive"
	"github.com/ether-media/ether/internal/archive/archivetest"
)

func payload(n int, seed byte) []byte {
	b := make([]byte, n)
	x := uint32(seed) + 1
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func TestBuildRARVolumeSizes(t *testing.T) {
	for _, v := range []int{4, 5} {
		for _, vs := range []int64{150, 151, 199, 256, 1000, 4096} {
			single := []archivetest.RARFile{{Name: "movie.mkv", Data: payload(20000, 1)}}
			vols, err := archivetest.BuildRAR(single, archivetest.RAROptions{Version: v, VolumeSize: vs})
			if err != nil {
				t.Fatal(err)
			}
			for i, b := range vols[:len(vols)-1] {
				if int64(len(b)) != vs {
					t.Fatalf("rar%d -v%d: volume %d is %d bytes", v, vs, i, len(b))
				}
			}
			if last := int64(len(vols[len(vols)-1])); last > vs {
				t.Fatalf("rar%d -v%d: last volume %d bytes", v, vs, last)
			}
			checkRoundTrip(t, vols, single)

			multi := []archivetest.RARFile{
				{Name: "a.mkv", Data: payload(3000, 2)},
				{Name: "b/", Dir: true},
				{Name: "b/c.nfo", Data: payload(10, 3)},
				{Name: "d.bin", Data: payload(777, 4)},
			}
			vols, err = archivetest.BuildRAR(multi, archivetest.RAROptions{Version: v, VolumeSize: vs})
			if err != nil {
				t.Fatal(err)
			}
			for i, b := range vols[:len(vols)-1] {
				if int64(len(b)) > vs {
					t.Fatalf("rar%d -v%d: volume %d is %d bytes", v, vs, i, len(b))
				}
			}
			checkRoundTrip(t, vols, multi)
		}
	}
}

// TestBuildRARExactSweep checks every volume size in a range, crossing the
// vint length steps of the RAR5 data size (127/128) and volume number.
func TestBuildRARExactSweep(t *testing.T) {
	data := payload(20000, 9)
	for _, v := range []int{4, 5} {
		for vs := int64(120); vs < 420; vs++ {
			vols, err := archivetest.BuildRAR([]archivetest.RARFile{{Name: "m.mkv", Data: data}}, archivetest.RAROptions{Version: v, VolumeSize: vs})
			if err != nil {
				t.Fatal(err)
			}
			for i, b := range vols[:len(vols)-1] {
				if int64(len(b)) != vs {
					t.Fatalf("rar%d -v%d: volume %d is %d bytes", v, vs, i, len(b))
				}
			}
			if vs%37 == 0 {
				checkRoundTrip(t, vols, []archivetest.RARFile{{Name: "m.mkv", Data: data}})
			}
		}
	}
}

func checkRoundTrip(t *testing.T, bufs [][]byte, files []archivetest.RARFile) {
	t.Helper()
	vols := make([]archive.Volume, len(bufs))
	for i, b := range bufs {
		vols[i] = archive.Volume{R: bytes.NewReader(b), Size: int64(len(b))}
	}
	members, err := archive.ListRAR(vols)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != len(files) {
		t.Fatalf("%d members, want %d", len(members), len(files))
	}
	for i, f := range files {
		if f.Dir {
			continue
		}
		r, err := archive.NewMemberReader(members[i], vols)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(io.NewSectionReader(r, 0, r.Size()))
		if err != nil || !bytes.Equal(got, f.Data) {
			t.Fatalf("%s: payload mismatch (%v)", f.Name, err)
		}
	}
}

func TestBuildRARErrors(t *testing.T) {
	if _, err := archivetest.BuildRAR(nil, archivetest.RAROptions{Version: 3}); err == nil {
		t.Fatal("version 3 accepted")
	}
	files := []archivetest.RARFile{{Name: "x", Data: payload(100, 1)}}
	if _, err := archivetest.BuildRAR(files, archivetest.RAROptions{VolumeSize: 40}); !errors.Is(err, archivetest.ErrVolumeTooSmall) {
		t.Fatalf("tiny volume: %v", err)
	}
	vols, err := archivetest.BuildRAR(files, archivetest.RAROptions{HeaderEncrypted: true})
	if err != nil || len(vols) != 1 {
		t.Fatal(err)
	}
	if _, err := archive.ReadRARVolume(bytes.NewReader(vols[0]), int64(len(vols[0])), nil); !errors.Is(err, archive.ErrEncrypted) {
		t.Fatalf("header encrypted: %v", err)
	}
}

func TestBuild7zCopy(t *testing.T) {
	b := archivetest.Build7zCopy([]archivetest.SevenZipFile{{Name: "a", Data: []byte("hello")}, {Name: "b", Data: []byte("world")}}, true)
	members, err := archive.List7z(bytes.NewReader(b), int64(len(b)))
	if err != nil || len(members) != 2 || members[1].Extents[0].Offset != 32+5 {
		t.Fatalf("%+v %v", members, err)
	}
}
