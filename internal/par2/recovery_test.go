package par2

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func oracle(t *testing.T) (*Layout, *Field, []Recovery, [][]byte) {
	t.Helper()
	index, err := os.ReadFile("testdata/oracle.par2")
	if err != nil {
		t.Fatal(err)
	}
	set, err := Parse(index)
	if err != nil {
		t.Fatal(err)
	}
	var id ID
	for v := range set.Main {
		id = v
	}
	l, err := set.RecoveryLayout(id, 4096)
	if err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob("testdata/oracle.vol*.par2")
	if err != nil || len(names) != 1 {
		t.Fatal(names, err)
	}
	volume, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatal(err)
	}
	p := RecoveryPackets(volume, id, l.SliceSize, 16)
	if len(p) != 4 {
		t.Fatalf("parity count %d", len(p))
	}
	var blocks [][]byte
	for _, b := range l.Blocks {
		seed := 0
		for _, c := range []byte(b.File.Name) {
			seed += int(c)
		}
		data := make([]byte, l.SliceSize)
		for j := int64(0); j < b.Length; j++ {
			i := b.Offset + j
			data[j] = byte((i * 73) ^ (i >> 3) ^ int64(seed))
		}
		if !b.Verify(data) {
			t.Fatal("oracle source mismatch")
		}
		blocks = append(blocks, data)
	}
	return l, NewField(), p, blocks
}
func TestRepairIndependentPAR2Oracle(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(map[bool]string{false: "selective", true: "whole"}[whole], func(t *testing.T) {
			l, f, p, blocks := oracle(t)
			holes := []Hole{{0, 100, 600}, {1, 1300, 1600}}
			read := func(ctx context.Context, b int, off int64, dst []byte) error {
				for _, h := range holes {
					if b == h.Block && off < h.End && off+int64(len(dst)) > h.Begin {
						return ErrErasure
					}
				}
				copy(dst, blocks[b][off:off+int64(len(dst))])
				return nil
			}
			fixed, st, err := l.Recover(context.Background(), f, holes, read, p, nil, RepairOptions{WholeSlices: whole})
			if err != nil {
				t.Fatal(err)
			}
			for b, d := range fixed {
				if !bytes.Equal(d, blocks[b]) {
					t.Fatal("wrong output")
				}
			}
			if !whole && st.InputBytes >= int64(len(blocks))*l.SliceSize {
				t.Fatal("selective recovery read the whole set")
			}
		})
	}
}
func TestRepairSummariesSurviveEviction(t *testing.T) {
	l, f, p, blocks := oracle(t)
	s, err := NewSummaries(l, f, []uint32{0, 1, 2, 3}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for b := 2; b < len(blocks); b++ {
		if !s.Observe(b, blocks[b]) || s.Observe(b, blocks[b]) {
			t.Fatal("observation not idempotent")
		}
	}
	read := func(ctx context.Context, b int, off int64, dst []byte) error {
		if b >= 2 {
			t.Fatal("reread evicted summary source")
		}
		if b == 0 && off < 200 && off+int64(len(dst)) > 100 {
			return ErrErasure
		}
		copy(dst, blocks[b][off:off+int64(len(dst))])
		return nil
	}
	fixed, st, err := l.Recover(context.Background(), f, []Hole{{0, 100, 200}}, read, p, s, RepairOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixed[0], blocks[0]) || st.SummaryBytes == 0 {
		t.Fatal("summary not used", st)
	}
}
func TestRepairFailureAndCancellation(t *testing.T) {
	l, f, p, blocks := oracle(t)
	holes := []Hole{{0, 0, 4096}, {1, 0, 4096}}
	read := func(ctx context.Context, b int, off int64, dst []byte) error {
		copy(dst, blocks[b][off:off+int64(len(dst))])
		return nil
	}
	if out, _, err := l.Recover(context.Background(), f, holes, read, p[:1], nil, RepairOptions{}); !errors.Is(err, ErrInsufficient) || out != nil {
		t.Fatal(out, err)
	}
	ctx, c := context.WithCancel(context.Background())
	c()
	if _, _, err := l.Recover(ctx, f, holes, read, p, nil, RepairOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p[0].Data[5] ^= 1
	if out, _, err := l.Recover(context.Background(), f, holes, read, p[:2], nil, RepairOptions{}); err == nil || out != nil {
		t.Fatal("corrupt recovery published")
	}
}
func TestRepairHelpersAndOddOffsets(t *testing.T) {
	l, f, p, blocks := oracle(t)
	read := func(ctx context.Context, b int, off int64, dst []byte) error {
		if b == 1 && off < 602 && off+int64(len(dst)) > 100 {
			return ErrErasure
		}
		copy(dst, blocks[b][off:off+int64(len(dst))])
		return nil
	}
	out, _, err := l.Recover(context.Background(), f, []Hole{{0, 101, 601}}, read, p, nil, RepairOptions{})
	if err != nil || !bytes.Equal(out[0], blocks[0]) {
		t.Fatal(err)
	}
}
func TestRecoveryPacketsRejectCorruptionAndForeignSet(t *testing.T) {
	l, _, _, _ := oracle(t)
	paths, _ := filepath.Glob("testdata/oracle.vol*.par2")
	data, _ := os.ReadFile(paths[0])
	foreign := l.ID
	foreign[0] ^= 1
	if len(RecoveryPackets(data, foreign, l.SliceSize, 16)) != 0 {
		t.Fatal("foreign parity")
	}
	for i := 0; i < len(data); i++ {
		data[i] ^= 1
	}
	if len(RecoveryPackets(data, l.ID, l.SliceSize, 16)) != 0 {
		t.Fatal("corrupt parity")
	}
}
func TestSummariesRejectBadInput(t *testing.T) {
	l, f, _, b := oracle(t)
	s, _ := NewSummaries(l, f, []uint32{0}, 1<<20)
	b[0][0] ^= 1
	if s.Observe(0, b[0]) || s.Seen[0] {
		t.Fatal("corrupt summary")
	}
}

func TestRecoveryScannerSurvivesArticleGaps(t *testing.T) {
	l, _, expected, _ := oracle(t)
	paths, _ := filepath.Glob("testdata/oracle.vol*.par2")
	data, _ := os.ReadFile(paths[0])
	var packets [][]byte
	for at := 0; at+HeaderSize <= len(data); {
		p, ok := packetAt(data, at)
		if !ok {
			t.Fatal("bad oracle")
		}
		if string(p[48:64]) == TypeRecovery {
			packets = append(packets, bytes.Clone(p))
		}
		at += len(p)
	}
	s := RecoveryScanner{ID: l.ID, SliceSize: l.SliceSize}
	var got []Recovery
	for off := 0; off < len(packets[0]); off += 37 {
		got = append(got, s.Push(packets[0][off:min(off+37, len(packets[0]))], 16)...)
	}
	s.Push(packets[1][:100], 16)
	s.Reset() // one NNTP article was unavailable
	got = append(got, s.Push(append(bytes.Clone(packets[1][200:]), packets[2]...), 16)...)
	if len(got) != 2 || got[0].Exponent != expected[0].Exponent || got[1].Exponent != expected[2].Exponent {
		t.Fatalf("lost valid packets around gap: %v", len(got))
	}
	// A corrupt plausible outer header must not hide a valid nested packet.
	bad := make([]byte, HeaderSize)
	copy(bad, Magic)
	binary.LittleEndian.PutUint64(bad[8:16], uint64(HeaderSize+len(packets[3])))
	bad = append(bad, packets[3]...)
	s.Reset()
	got = s.Push(bad, 16)
	if len(got) != 1 || got[0].Exponent != expected[3].Exponent {
		t.Fatal("failed to resynchronize")
	}
}
