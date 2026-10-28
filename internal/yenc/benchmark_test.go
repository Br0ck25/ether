package yenc

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"testing"
)

// Realistic article sizes and wire line lengths, with both output ownership
// modes. CRC remains enabled: a decoder benchmark must measure verified bytes.
func BenchmarkArticle(b *testing.B) {
	for _, line := range []int{128, 256} {
		for _, size := range []int{384 << 10, 768 << 10} {
			data := randBytes(43, size)
			enc, err := EncodeBytes(data, EncodeOptions{Name: "bench.bin", LineLength: line})
			if err != nil {
				b.Fatal(err)
			}
			for _, reuse := range []bool{false, true} {
				b.Run(fmt.Sprintf("line%d/%dKiB/reuse%v", line, size>>10, reuse), func(b *testing.B) {
					var opt DecodeOptions
					if reuse {
						opt.Buffer = make([]byte, 0, size)
					}
					b.SetBytes(int64(size))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if _, err := DecodeLines(&bytesLines{b: enc}, &opt); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func BenchmarkWireLines(b *testing.B) {
	for _, line := range []int{128, 256} {
		for _, distribution := range []string{"random", "noescape", "allescape"} {
			data := randBytes(43, 768<<10)
			switch distribution {
			case "noescape":
				data = bytes.Repeat([]byte{'a'}, len(data))
			case "allescape":
				data = bytes.Repeat([]byte{214, 224, 227, 19}, len(data)/4)
			}
			enc, err := EncodeBytes(data, EncodeOptions{Name: "bench.bin", LineLength: line})
			if err != nil {
				b.Fatal(err)
			}
			var lines [][]byte
			for _, l := range bytes.Split(enc, []byte("\r\n")) {
				if len(l) > 0 && !bytes.HasPrefix(l, []byte("=y")) {
					lines = append(lines, l)
				}
			}
			b.Run(fmt.Sprintf("line%d/%s", line, distribution), func(b *testing.B) {
				dst := make([]byte, line+1)
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					esc := false
					for _, l := range lines {
						_, esc = DecodeLine(dst, l, esc)
					}
				}
			})
		}
	}
}

func BenchmarkArticleCRC(b *testing.B) {
	data := randBytes(43, 768<<10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if crc32.ChecksumIEEE(data) == 0 {
			b.Fatal("unexpected zero CRC")
		}
	}
}
