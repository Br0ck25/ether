package overlays

import (
	"fmt"
	"testing"
)

// Each cold iteration changes only the accent, guaranteeing a new output key
// while decoding the same 500x750 JPEG and painting the same three badges.
func BenchmarkOverlayCold(b *testing.B) {
	s := New(nil, Options{MemoryCacheBytes: -1})
	data := cacheTestImage(b)
	cfg := normalizeSettings(Settings{Enabled: true, Status: true, Quality: true, Ratings: true})
	labels := []string{"NEW SEASON", "4K HDR", "IMDb 8.6"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg.Accent = fmt.Sprintf("#%06x", i%0x1000000)
		if _, _, err := s.composite(data, labels, cfg); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkOverlayMemoryWarm(b *testing.B) {
	s := New(nil, Options{})
	data := cacheTestImage(b)
	cfg := normalizeSettings(Settings{Enabled: true, Status: true, Quality: true, Ratings: true})
	labels := []string{"NEW SEASON", "4K HDR", "IMDb 8.6"}
	if _, _, err := s.composite(data, labels, cfg); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.composite(data, labels, cfg); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkOverlayDiskWarm(b *testing.B) {
	s := New(nil, Options{CacheDir: b.TempDir(), MemoryCacheBytes: -1})
	data := cacheTestImage(b)
	cfg := normalizeSettings(Settings{Enabled: true, Status: true, Quality: true, Ratings: true})
	labels := []string{"NEW SEASON", "4K HDR", "IMDb 8.6"}
	if _, _, err := s.composite(data, labels, cfg); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.composite(data, labels, cfg); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkOverlaySampleWarm(b *testing.B) {
	s := New(nil, Options{})
	cfg := normalizeSettings(Settings{Enabled: true, Status: true, Quality: true, Ratings: true})
	if _, _, err := s.Sample(cfg); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.Sample(cfg); err != nil {
			b.Fatal(err)
		}
	}
}
