package transcode

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func fakeJPEG(seed byte, n int) []byte {
	b := make([]byte, n)
	b[0], b[1], b[2] = 0xff, 0xd8, 0xff
	for i := 3; i < n-2; i++ {
		b[i] = seed
	}
	b[n-2], b[n-1] = 0xff, 0xd9
	return b
}

func TestBIFRoundTripSparse(t *testing.T) {
	frames := []BIFFrame{
		{Time: 30 * time.Second, JPEG: fakeJPEG(3, 40)},
		{Time: 0, JPEG: fakeJPEG(1, 24)},
		{Time: 95 * time.Second, JPEG: fakeJPEG(9, 31)},
		{Time: 10 * time.Second, JPEG: fakeJPEG(2, 24)},
	}
	data, err := MarshalBIF(frames)
	if err != nil {
		t.Fatal(err)
	}
	// Header layout.
	if !bytes.Equal(data[:8], bifMagic) {
		t.Errorf("magic = % x", data[:8])
	}
	if got := binary.LittleEndian.Uint32(data[8:]); got != 0 {
		t.Errorf("version = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[12:]); got != 4 {
		t.Errorf("count = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[16:]); got != 1000 {
		t.Errorf("multiplier = %d", got)
	}
	if !bytes.Equal(data[20:64], make([]byte, 44)) {
		t.Error("reserved bytes not zero")
	}
	// Terminator: entry 4 at 64+4*8, offset = total size.
	term := data[64+4*8:]
	if ts := binary.LittleEndian.Uint32(term); ts != 0xffffffff {
		t.Errorf("terminator ts = %#x", ts)
	}
	if off := binary.LittleEndian.Uint32(term[4:]); int(off) != len(data) {
		t.Errorf("terminator offset = %d, want %d", off, len(data))
	}
	wantTotal := 64 + 5*8 + 40 + 24 + 31 + 24
	if len(data) != wantTotal {
		t.Errorf("size = %d, want %d", len(data), wantTotal)
	}

	b, err := ParseBIF(data)
	if err != nil {
		t.Fatal(err)
	}
	if b.Len() != 4 || b.Multiplier != 1000 {
		t.Fatalf("Len=%d Multiplier=%d", b.Len(), b.Multiplier)
	}
	wantTimes := []time.Duration{0, 10 * time.Second, 30 * time.Second, 95 * time.Second}
	wantSeeds := []byte{1, 2, 3, 9}
	for i := range wantTimes {
		if b.Time(i) != wantTimes[i] {
			t.Errorf("frame %d time = %s, want %s", i, b.Time(i), wantTimes[i])
		}
		if got := b.Frame(i); !IsJPEG(got) || got[3] != wantSeeds[i] {
			t.Errorf("frame %d image mismatch", i)
		}
	}
	// FrameAt picks the latest frame at or before the time.
	if got, ok := b.FrameAt(47 * time.Second); !ok || got[3] != 3 {
		t.Errorf("FrameAt(47s) = %v, %v", got, ok)
	}
	if got, ok := b.FrameAt(95 * time.Second); !ok || got[3] != 9 {
		t.Errorf("FrameAt(95s) = %v, %v", got, ok)
	}
	if _, ok := b.FrameAt(-time.Second); ok {
		t.Error("FrameAt(-1s) should miss")
	}
	// Deterministic: same input order, same bytes.
	again, err := MarshalBIF(frames)
	if err != nil || !bytes.Equal(again, data) {
		t.Errorf("not deterministic: %v", err)
	}
	// Duplicate timestamps collapse to the first frame.
	dup, err := MarshalBIF([]BIFFrame{
		{Time: 10 * time.Second, JPEG: fakeJPEG(5, 20)},
		{Time: 10*time.Second + 300*time.Millisecond, JPEG: fakeJPEG(6, 20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := ParseBIF(dup)
	if err != nil || pb.Len() != 1 || pb.Frame(0)[3] != 5 {
		t.Errorf("dup handling: %+v %v", pb, err)
	}
}

func TestBIFErrors(t *testing.T) {
	if _, err := MarshalBIF(nil); err == nil {
		t.Error("empty frames should fail")
	}
	if _, err := MarshalBIF([]BIFFrame{{Time: 0}}); err == nil {
		t.Error("empty image should fail")
	}
	if _, err := MarshalBIF([]BIFFrame{{Time: -time.Second, JPEG: fakeJPEG(1, 10)}}); err == nil {
		t.Error("negative time should fail")
	}
	if _, err := ParseBIF([]byte("tiny")); err == nil {
		t.Error("short input should fail")
	}
	bad := make([]byte, 128)
	if _, err := ParseBIF(bad); err == nil {
		t.Error("bad magic should fail")
	}
	good, _ := MarshalBIF([]BIFFrame{{Time: 0, JPEG: fakeJPEG(1, 20)}})
	trunc := good[:len(good)-5]
	if _, err := ParseBIF(trunc); err == nil {
		t.Error("truncated image data should fail")
	}
	noTerm := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(noTerm[64+8:], 7) // terminator ts
	if _, err := ParseBIF(noTerm); err == nil {
		t.Error("missing terminator should fail")
	}
}
