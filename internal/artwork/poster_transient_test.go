package artwork

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"image/png"
	"sync/atomic"
	"testing"
)

type flakySource struct {
	loads atomic.Int32
	down  atomic.Bool
}

func (f *flakySource) Get(ctx context.Context, path string, w int) ([]byte, string, error) {
	f.loads.Add(1)
	if f.down.Load() {
		return nil, "", errors.New("upstream timeout")
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, synthetic(60, 90, color.RGBA{10, 200, 50, 255}))
	return buf.Bytes(), "image/png", nil
}

// A poster rendered while member art was transiently unreachable must not be
// persisted, or the degraded typographic fallback is served forever.
func TestRendererDoesNotPersistTransientFallback(t *testing.T) {
	src := &flakySource{}
	src.down.Store(true)
	r := NewRenderer(src, t.TempDir(), nil)
	req := PosterRequest{Title: "Flaky", Posters: []string{"/a.jpg"}, Width: 240}
	degraded, _, err := r.Poster(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	src.down.Store(false)
	before := src.loads.Load()
	healed, _, err := r.Poster(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if src.loads.Load() == before || bytes.Equal(degraded, healed) {
		t.Fatal("transient fallback poster was cached on disk")
	}
	// The complete render is cached.
	before = src.loads.Load()
	if _, _, err := r.Poster(context.Background(), req); err != nil || src.loads.Load() != before {
		t.Fatalf("complete poster not cached: %v", err)
	}
}
