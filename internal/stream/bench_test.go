package stream

import (
	"context"
	"io"
	"testing"
	"time"
)

// benchmarkSequential reads a whole file with 32 KiB reads (like
// http.ServeContent) through a fetcher that injects per-article latency.
func benchmarkSequential(b *testing.B, latency time.Duration, opts Options) {
	f := newFakeFetcher()
	const size = 64 << 20
	data := mkvBytes(1, size)
	doc := post(b, f, 750_000, postFile{name: "bench.mkv", data: data})
	f.latency = latency
	buf := make([]byte, 32<<10)
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e := New(f, nil, opts, nil)
		src, err := e.Open(context.Background(), "bench", doc)
		if err != nil {
			b.Fatal(err)
		}
		var off int64
		for {
			n, err := src.ReadAt(buf, off)
			off += int64(n)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		_ = src.Close()
		_ = e.Close()
	}
}

func BenchmarkSequentialNoLatency(b *testing.B) { benchmarkSequential(b, 0, Options{}) }
func BenchmarkSequential20ms(b *testing.B)      { benchmarkSequential(b, 20*time.Millisecond, Options{}) }
func BenchmarkSequential50ms(b *testing.B)      { benchmarkSequential(b, 50*time.Millisecond, Options{}) }
func BenchmarkSequential50msWide(b *testing.B) {
	benchmarkSequential(b, 50*time.Millisecond, Options{ReadAheadParallel: 24, ReadAheadMax: 128 << 20})
}
