package archive

import (
	"bytes"
	"io"
	"testing"

	at "github.com/braedonsaunders/ether/internal/archive/archivetest"
)

// exerciseMembers reads a little from every streamable member; it must never
// panic whatever the parser returned.
func exerciseMembers(t *testing.T, members []Member, vols []Volume) {
	buf := make([]byte, 64)
	for _, m := range members {
		mr, err := NewMemberReader(m, vols)
		if err != nil {
			continue
		}
		for _, off := range []int64{0, mr.Size() / 2, mr.Size() - 1} {
			if off < 0 {
				continue
			}
			if _, err := mr.ReadAt(buf, off); err != nil && err != io.EOF {
				t.Fatalf("member %q ReadAt(%d): %v", m.Name, off, err)
			}
		}
	}
}

func FuzzReadRARVolume(f *testing.F) {
	small := []at.RARFile{
		{Name: "a/b.mkv", Data: []byte("hello world, stored data")},
		{Name: "dir", Dir: true},
		{Name: "ünï.bin", Data: []byte{1, 2, 3}, Large: true},
		{Name: "c.bin", Data: []byte("compressed?"), Method: 3, Solid: true, Encrypted: true},
	}
	for _, v := range []int{4, 5} {
		for _, o := range []at.RAROptions{
			{Version: v},
			{Version: v, ServiceBlocks: true},
			{Version: v, VolumeSize: 120},
			{Version: v, HeaderEncrypted: true},
		} {
			vols, err := at.BuildRAR(small, o)
			if err != nil {
				f.Fatal(err)
			}
			for _, b := range vols {
				f.Add(b)
			}
		}
	}
	for _, n := range []string{"compressed.rar", "encrypted.rar", "solid.rar", "multi.part3.rar"} {
		f.Add(readFixture(f, "altmount", n))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), nil)
		if err != nil {
			return
		}
		for _, file := range v.Files {
			if file.DataOffset < 0 || file.PackedSize < 0 || file.DataOffset+file.PackedSize > int64(len(b)) {
				t.Fatalf("file outside volume: %+v", file)
			}
		}
		members, err := ComposeRAR([]*RARVolume{v})
		if err != nil {
			return
		}
		exerciseMembers(t, members, memVols(b))
	})
}

func FuzzList7z(f *testing.F) {
	// Small seeds only: the fuzzer minimises slowly on large inputs.
	for _, n := range []string{"store_many.7z", "bcj_lzma.7z"} {
		f.Add(readFixture(f, "7z", n))
	}
	f.Add(at.Build7zCopy([]at.SevenZipFile{{Name: "a", Data: []byte("abc")}, {Name: "d", Dir: true}, {Name: "e"}, {Name: "b", Data: []byte("xyz")}}, true))
	f.Add(at.Build7zCopy([]at.SevenZipFile{{Name: "a", Data: []byte("abc")}, {Name: "b", Data: []byte("x")}}, false))
	f.Fuzz(func(t *testing.T, b []byte) {
		members, err := List7z(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		for _, m := range members {
			for _, e := range m.Extents {
				if e.Offset < 0 || e.Size < 0 || e.Offset+e.Size > int64(len(b)) {
					t.Fatalf("extent outside archive: %+v", e)
				}
			}
		}
		exerciseMembers(t, members, memVols(b))
	})
}
