package yenc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestMaxSizeWithReusedBuffer(t *testing.T) {
	// A dishonest size takes the direct decode path when a reused buffer has
	// spare capacity. Output limits must not depend on that capacity.
	for _, hint := range []string{"0", "1"} {
		body := []byte("=ybegin size=" + hint + " name=x\nabc\n=yend size=3\n")
		for _, capacity := range []int{0, 1, 2, 4096} {
			p, err := DecodeLines(&bytesLines{b: body}, &DecodeOptions{MaxSize: 2, Buffer: make([]byte, 0, capacity)})
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("hint %s capacity %d: part %v error %v", hint, capacity, p, err)
			}
		}
	}
}

func TestSmallLimitBoundsInitialAllocation(t *testing.T) {
	p, err := DecodeLines(&bytesLines{b: []byte("=ybegin size=0 name=x\n=yend size=0\n")}, &DecodeOptions{MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if cap(p.Data) > 1 {
		t.Fatalf("capacity %d exceeds limit", cap(p.Data))
	}
}

func TestDecodedLimitCountsEscapesNotWireBytes(t *testing.T) {
	data := bytes.Repeat([]byte{214}, 128)
	enc, err := EncodeBytes(data, EncodeOptions{Name: "escaped", LineLength: 256})
	if err != nil {
		t.Fatal(err)
	}
	for _, capacity := range []int{0, len(data), 4096} {
		p, err := DecodeLines(&bytesLines{b: enc}, &DecodeOptions{MaxSize: int64(len(data)), Buffer: make([]byte, 0, capacity)})
		if err != nil || !bytes.Equal(p.Data, data) {
			t.Fatalf("capacity %d: %v", capacity, err)
		}
		if cap(p.Data) > len(data) {
			t.Fatalf("capacity %d exceeds limit", cap(p.Data))
		}
	}
}

func TestDanglingEscapeAtTrailer(t *testing.T) {
	for _, middle := range []string{"=\n", "=\n\n", "abc=\n"} {
		size := "0"
		if strings.HasPrefix(middle, "abc") {
			size = "3"
		}
		body := "=ybegin size=" + size + " name=x\n" + middle + "=yend size=" + size + "\n"
		if _, err := Decode([]byte(body)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%q: %v", middle, err)
		}
	}
}

func TestPreambleAndEmptyBodyAreBounded(t *testing.T) {
	body := "=ybegin size=0 name=x\n=yend size=0\n"
	if _, err := Decode([]byte(strings.Repeat("\n", MaxPreambleSize) + body)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("preamble: %v", err)
	}
	body = "=ybegin size=0 name=x\n" + strings.Repeat("\n", MaxPreambleSize) + "=yend size=0\n"
	if _, err := DecodeLines(&bytesLines{b: []byte(body)}, &DecodeOptions{MaxSize: 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("body: %v", err)
	}
}

func TestEncodedLineLimit(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		line := strings.Repeat("a", MaxLineSize+1) + suffix
		for _, bufferSize := range []int{16, 32 << 10, 2 << 20} {
			lr := NewLineReader(bufio.NewReaderSize(strings.NewReader(line), bufferSize))
			if _, err := lr.ReadLine(); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("buffer %d suffix %q: %v", bufferSize, suffix, err)
			}
		}
		if _, err := Decode([]byte(line)); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("Decode: %v", err)
		}
		body := "=ybegin size=1 name=x\n" + line
		if _, err := Decode([]byte(body)); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("body: %v", err)
		}
	}
}

func TestLineReaderBoundary(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		input := strings.Repeat("a", MaxLineSize) + suffix
		lr := NewLineReader(strings.NewReader(input))
		line, err := lr.ReadLine()
		if err != nil || len(line) != MaxLineSize {
			t.Fatalf("suffix %q: len %d err %v", suffix, len(line), err)
		}
		if _, err := lr.ReadLine(); !errors.Is(err, io.EOF) {
			t.Fatalf("EOF: %v", err)
		}
	}
}

func FuzzDecodeBoundedBuffer(f *testing.F) {
	f.Add([]byte("=ybegin size=0 name=x\nabc\n=yend size=3\n"), uint16(2), true)
	enc, _ := EncodeBytes([]byte("hello"), EncodeOptions{Name: "seed"})
	f.Add(enc, uint16(5), false)
	f.Fuzz(func(t *testing.T, encoded []byte, limit uint16, reuse bool) {
		maxSize := int64(limit) + 1
		opt := DecodeOptions{MaxSize: maxSize}
		if reuse {
			opt.Buffer = make([]byte, 0, 128<<10)
		}
		p, _ := DecodeLines(&bytesLines{b: encoded}, &opt)
		if p != nil && int64(len(p.Data)) > maxSize {
			t.Fatalf("decoded %d exceeds limit %d", len(p.Data), maxSize)
		}
	})
}

func TestWholeAndPartCRCBothVerified(t *testing.T) {
	data := []byte("complete file in one part")
	enc, err := EncodeBytes(data, EncodeOptions{Name: "file", Part: 1, Total: 1, HasFileCRC: true, FileCRC: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(enc); !errors.Is(err, ErrCRCMismatch) {
		t.Fatalf("incorrect whole CRC accepted with correct part CRC: %v", err)
	}
	// Whole-file CRC cannot be verified using a partial file's bytes.
	enc, err = EncodeBytes(data, EncodeOptions{Name: "file", Part: 1, Total: 2, FileSize: 100, HasFileCRC: true, FileCRC: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(enc); err != nil {
		t.Fatalf("partial article: %v", err)
	}
}
