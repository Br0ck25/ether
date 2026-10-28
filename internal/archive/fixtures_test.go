package archive

import (
	"bytes"
	"errors"
	"testing"
)

// Real WinRAR-produced RAR5 archives from AltMount (MIT), see
// THIRD_PARTY_NOTICES.md. payload_a.bin is 200000 bytes of a repeating
// 0..255 pattern.

func altVols(t *testing.T, names ...string) []Volume {
	t.Helper()
	bufs := make([][]byte, len(names))
	for i, n := range names {
		bufs[i] = readFixture(t, "altmount", n)
	}
	return memVols(bufs...)
}

func TestAltMountRARSingle(t *testing.T) {
	want := readFixture(t, "altmount", "payload_a.bin")
	vols := altVols(t, "archive.rar")
	rr := &recReader{r: vols[0].R}
	v, err := ReadRARVolume(rr, vols[0].Size, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != FormatRAR5 || v.MultiVolume || !v.First || !v.EndFound || len(v.Files) != 1 {
		t.Fatalf("volume: %+v", v)
	}
	f := v.Files[0]
	if f.Name != "payload_a.bin" || !f.Stored || f.PackedSize != 200000 || f.UnpackedSize != 200000 || !f.HasCRC {
		t.Fatalf("file: %+v", f)
	}
	rr.assertNoDataReads(t, f.DataOffset, f.PackedSize, f.Name)
	members, err := ListRAR(vols)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Streamable() != nil {
		t.Fatalf("members: %+v", members)
	}
	mr, err := NewMemberReader(members[0], vols)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, mr, mr.Size()), want) {
		t.Fatal("payload mismatch")
	}
	checkRandomReads(t, mr, want, nil, 7)
}

func TestAltMountRARMulti(t *testing.T) {
	want := readFixture(t, "altmount", "payload_a.bin")
	vols := altVols(t, "archive.part01.rar", "archive.part02.rar")
	for i, vol := range vols {
		rr := &recReader{r: vol.R}
		v, err := ReadRARVolume(rr, vol.Size, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !v.MultiVolume || v.Number != i || v.First != (i == 0) || len(v.Files) != 1 {
			t.Fatalf("volume %d: %+v", i, v)
		}
		f := v.Files[0]
		if f.SplitBefore != (i == 1) || f.SplitAfter != (i == 0) || f.Name != "payload_a.bin" {
			t.Fatalf("volume %d file: %+v", i, f)
		}
		if i == 0 && (v.EndFound || rr.maxEnd() > f.DataOffset) {
			t.Fatalf("volume 0: read past the split file's header (EndFound %v, max %d)", v.EndFound, rr.maxEnd())
		}
		if i == 1 && (!v.EndFound || v.MoreVolumes) {
			t.Fatalf("volume 1: end %v more %v", v.EndFound, v.MoreVolumes)
		}
	}
	members, err := ListRAR(vols)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || len(members[0].Extents) != 2 || members[0].Size != 200000 {
		t.Fatalf("members: %+v", members)
	}
	mr, err := NewMemberReader(members[0], vols)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, mr, mr.Size()), want) {
		t.Fatal("payload mismatch")
	}
	checkRandomReads(t, mr, want, extentCuts(members[0]), 8)
	if _, err := ListRAR(vols[:1]); !errors.Is(err, ErrMissingVolume) {
		t.Fatalf("missing part02: %v", err)
	}
}

func TestAltMountRARCompressedSolidEncrypted(t *testing.T) {
	members, err := ListRAR(altVols(t, "compressed.rar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Name != "release.nzb" || members[0].Size != 269 ||
		members[0].Extents[0].Size != 212 || members[0].Stored || !errors.Is(members[0].Streamable(), ErrCompressed) {
		t.Fatalf("compressed: %+v", members)
	}

	members, err = ListRAR(altVols(t, "solid.rar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].Name != "sidecar.nfo" || members[0].Size != 500 ||
		members[1].Name != "release.nzb" || members[1].Size != 270 {
		t.Fatalf("solid: %+v", members)
	}
	if !errors.Is(members[0].Streamable(), ErrCompressed) || !members[1].Solid || !errors.Is(members[1].Streamable(), ErrSolid) {
		t.Fatalf("solid flags: %+v", members)
	}
	if v, err := ReadRARVolume(altVols(t, "solid.rar")[0].R, altVols(t, "solid.rar")[0].Size, nil); err != nil || !v.Solid {
		t.Fatalf("solid archive flag: %+v %v", v, err)
	}

	if _, err := ListRAR(altVols(t, "encrypted.rar")); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("encrypted: %v", err)
	}

	// A compressed file split over three volumes still composes.
	vols := altVols(t, "multi.part1.rar", "multi.part2.rar", "multi.part3.rar")
	members, err = ListRAR(vols)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Name != "release.nzb" || len(members[0].Extents) != 3 ||
		!errors.Is(members[0].Streamable(), ErrCompressed) {
		t.Fatalf("multi: %+v", members)
	}
	var packed int64
	for i, e := range members[0].Extents {
		if e.Volume != i || e.Offset+e.Size > vols[i].Size {
			t.Fatalf("multi extent %d: %+v", i, e)
		}
		packed += e.Size
	}
	// Sizes as reported by 7-Zip's RAR5 reader (7zz l -slt) for this fixture.
	if members[0].Size != 10945 || packed != 8482 {
		t.Fatalf("multi sizes: unpacked %d packed %d", members[0].Size, packed)
	}
}
