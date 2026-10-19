package artwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/bits"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// synthetic makes a w×h image with a diagonal gradient in a base colour.
func synthetic(w, h int, base color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f := float64(x+y) / float64(w+h)
			img.SetRGBA(x, y, color.RGBA{uint8(float64(base.R) * (0.5 + f/2)), uint8(float64(base.G) * (0.5 + f/2)), uint8(float64(base.B) * (0.5 + f/2)), 255})
		}
	}
	return img
}

func posters(n int) []image.Image {
	palette := []color.RGBA{{220, 40, 40, 255}, {40, 160, 220, 255}, {240, 200, 40, 255}, {60, 200, 90, 255}, {170, 60, 200, 255}, {240, 120, 30, 255}}
	out := make([]image.Image, n)
	for i := range out {
		out[i] = synthetic(120, 180, palette[i%len(palette)])
	}
	return out
}

// dHash is a 64-bit difference hash of an image, for tolerant goldens.
func dHash(img image.Image) uint64 {
	b := img.Bounds()
	var h uint64
	gray := func(x, y int) float64 {
		px := b.Min.X + x*b.Dx()/9
		py := b.Min.Y + y*b.Dy()/8
		r, g, bl, _ := img.At(px, py).RGBA()
		return 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			h <<= 1
			if gray(x, y) < gray(x+1, y) {
				h |= 1
			}
		}
	}
	return h
}

// brightIn reports the share of near-white pixels in a band (the title).
func brightIn(img *image.RGBA, y0, y1 float64) float64 {
	b := img.Bounds()
	n, bright := 0, 0
	for y := int(float64(b.Dy()) * y0); y < int(float64(b.Dy())*y1); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := img.RGBAAt(x, y)
			n++
			if c.R > 235 && c.G > 235 && c.B > 235 {
				bright++
			}
		}
	}
	return float64(bright) / float64(n)
}

func TestComposeModes(t *testing.T) {
	backdrop := synthetic(320, 180, color.RGBA{30, 90, 160, 255})
	cases := []struct {
		name      string
		req       PosterRequest
		posters   []image.Image
		backdrops []image.Image
		golden    uint64
	}{
		{"collage-3x2", PosterRequest{Title: "Best of the 80s", Subtitle: "100 FILMS", Mode: "collage", Width: 300}, posters(6), nil, goldenCollage},
		{"collage-2x2", PosterRequest{Title: "Mind-Bending", Mode: "auto", Width: 300}, posters(4), nil, goldenCollage2},
		{"backdrop", PosterRequest{Title: "Directed by Christopher Nolan", Subtitle: "14 FILMS", Mode: "backdrop", Accent: "#8b0000", Width: 300}, posters(2), []image.Image{backdrop}, goldenBackdrop},
		{"type", PosterRequest{Title: "Coming Soon", Mode: "auto", Accent: "#113ccf", Width: 300}, nil, nil, goldenType},
	}
	for _, c := range cases {
		img := Compose(c.req, c.posters, c.backdrops)
		if b := img.Bounds(); b.Dx() != 300 || b.Dy() != 450 {
			t.Fatalf("%s: size %v", c.name, b)
		}
		if share := brightIn(img, 0.55, 1); share < 0.01 {
			t.Errorf("%s: no title text in the lower half (%.4f)", c.name, share)
		}
		got := dHash(img)
		if d := bits.OnesCount64(got ^ c.golden); d > 8 {
			t.Errorf("%s: dHash %#x differs from golden %#x by %d bits", c.name, got, c.golden, d)
		}
	}
}

// Goldens: dHash of each composition (tolerance 8 of 64 bits). Regenerate
// with ARTWORK_GOLDEN=1 go test -run TestPrintGoldens -v.
const (
	goldenCollage  uint64 = 0xdfdfdfdfdf0094
	goldenCollage2 uint64 = 0x7ffffffff7f7d3d4
	goldenBackdrop uint64 = 0xfffffffff7ff2a94
	goldenType     uint64 = 0x1000101000040042
)

func TestComposeFallbacksAndAccent(t *testing.T) {
	// Collage without enough posters falls back to the single poster.
	img := Compose(PosterRequest{Title: "Solo", Mode: "collage", Width: 200}, posters(1), nil)
	if img.Bounds().Dx() != 200 || img.Bounds().Dy() != 300 {
		t.Fatalf("size %v", img.Bounds())
	}
	// Width is clamped.
	if b := Compose(PosterRequest{Title: "Tiny", Width: 10}, nil, nil).Bounds(); b.Dx() != minWidth {
		t.Fatalf("min width: %v", b)
	}
	if b := Compose(PosterRequest{Title: "Huge", Width: 9000}, nil, nil).Bounds(); b.Dx() != maxWidth {
		t.Fatalf("max width: %v", b)
	}
	// The accent colour tints a typographic poster.
	red := Compose(PosterRequest{Title: "Red", Accent: "#e50914", Width: 200}, nil, nil)
	c := red.RGBAAt(5, 5)
	if c.R <= c.G || c.R <= c.B {
		t.Fatalf("accent not applied: %+v", c)
	}
	// A very long title still fits (wrapped and shrunk, ellipsized past 3 lines).
	long := Compose(PosterRequest{Title: strings.Repeat("Supercalifragilistic ", 12), Width: 300}, posters(6), nil)
	if brightIn(long, 0.55, 1) == 0 {
		t.Fatal("long title missing")
	}
	if _, ok := parseHex("#zzzzzz"); ok {
		t.Fatal("bad hex parsed")
	}
	if a := deriveAccent(synthetic(40, 60, color.RGBA{200, 30, 30, 255})); a.R <= a.G {
		t.Fatalf("derived accent: %+v", a)
	}
}

func TestPrintGoldens(t *testing.T) {
	if testing.Short() || !strings.EqualFold(getenv("ARTWORK_GOLDEN"), "1") {
		t.Skip("set ARTWORK_GOLDEN=1 to print goldens")
	}
	backdrop := synthetic(320, 180, color.RGBA{30, 90, 160, 255})
	fmt.Printf("collage  %#x\n", dHash(Compose(PosterRequest{Title: "Best of the 80s", Subtitle: "100 FILMS", Mode: "collage", Width: 300}, posters(6), nil)))
	fmt.Printf("collage2 %#x\n", dHash(Compose(PosterRequest{Title: "Mind-Bending", Mode: "auto", Width: 300}, posters(4), nil)))
	fmt.Printf("backdrop %#x\n", dHash(Compose(PosterRequest{Title: "Directed by Christopher Nolan", Subtitle: "14 FILMS", Mode: "backdrop", Accent: "#8b0000", Width: 300}, posters(2), []image.Image{backdrop})))
	fmt.Printf("type     %#x\n", dHash(Compose(PosterRequest{Title: "Coming Soon", Mode: "auto", Accent: "#113ccf", Width: 300}, nil, nil)))
}

// fakeSource serves synthetic posters and counts loads.
type fakeSource struct {
	loads atomic.Int32
	fail  bool
}

func (f *fakeSource) Get(ctx context.Context, path string, w int) ([]byte, string, error) {
	f.loads.Add(1)
	if f.fail {
		return nil, "", ErrNotFound
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, synthetic(60, 90, color.RGBA{200, 100, 50, 255}))
	return buf.Bytes(), "image/png", nil
}

func TestRendererCachesOnDiskAndCoalesces(t *testing.T) {
	src := &fakeSource{}
	dir := t.TempDir()
	r := NewRenderer(src, dir, nil)
	req := PosterRequest{Title: "Cached", Posters: []string{"/a.jpg", "/b.jpg", "/c.jpg", "/d.jpg"}, Width: 240}
	var wg sync.WaitGroup
	results := make([][]byte, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data, ct, err := r.Poster(context.Background(), req)
			if err != nil || ct != "image/jpeg" {
				t.Errorf("poster: %v %s", err, ct)
			}
			results[i] = data
		}(i)
	}
	wg.Wait()
	if n := src.loads.Load(); n != 4 {
		t.Fatalf("concurrent renders should share one render (4 loads), got %d", n)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(results[0]))
	if err != nil || cfg.Width != 240 || cfg.Height != 360 {
		t.Fatalf("jpeg: %+v %v", cfg, err)
	}
	// A new renderer over the same directory serves from disk.
	r2 := NewRenderer(src, dir, nil)
	if data, _, err := r2.Poster(context.Background(), req); err != nil || !bytes.Equal(data, results[0]) || src.loads.Load() != 4 {
		t.Fatalf("disk cache: %v loads=%d", err, src.loads.Load())
	}
	// Changing anything that affects pixels misses the cache.
	req.Accent = "#123456"
	if _, _, err := r2.Poster(context.Background(), req); err != nil || src.loads.Load() != 8 {
		t.Fatalf("accent change should re-render: %v loads=%d", err, src.loads.Load())
	}
	// Unloadable member art still yields a typographic poster.
	bad := NewRenderer(&fakeSource{fail: true}, "", nil)
	if data, _, err := bad.Poster(context.Background(), PosterRequest{Title: "No Art", Posters: []string{"/x.jpg"}}); err != nil || len(data) == 0 {
		t.Fatalf("fallback: %v", err)
	}
}

func TestImagesProxyValidatesCachesAndCoalesces(t *testing.T) {
	var hits atomic.Int32
	jpg := func() []byte {
		var buf bytes.Buffer
		_ = jpeg.Encode(&buf, synthetic(20, 30, color.RGBA{10, 20, 30, 255}), nil)
		return buf.Bytes()
	}()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond)
		switch {
		case strings.HasSuffix(r.URL.Path, "/missing.jpg"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/page.jpg"):
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not an image</html>"))
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpg)
		}
	}))
	defer srv.Close()
	var sizes []string
	var mu sync.Mutex
	urlFor := func(path, size string) string {
		mu.Lock()
		sizes = append(sizes, size)
		mu.Unlock()
		return srv.URL + "/t/p/" + size + path
	}
	dir := t.TempDir()
	im := NewImages(ImagesOptions{Dir: dir, ImageURL: urlFor})
	ctx := context.Background()
	for _, bad := range []string{"", "abc.jpg", "/../etc/passwd", "/a/b.jpg", "/x.exe", "/" + strings.Repeat("a", 200) + ".jpg", "//evil.com/x.jpg"} {
		if _, _, err := im.Get(ctx, bad, 300); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%q: want ErrInvalidPath, got %v", bad, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, ct, err := im.Get(ctx, "/poster_1-a.jpg", 300)
			if err != nil || ct != "image/jpeg" || !bytes.Equal(data, jpg) {
				t.Errorf("get: %v %s", err, ct)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("concurrent gets should share one fetch, got %d", hits.Load())
	}
	if sizes[0] != "w300" {
		t.Fatalf("size: %v", sizes)
	}
	// Memory hit.
	if _, _, err := im.Get(ctx, "/poster_1-a.jpg", 250); err != nil || hits.Load() != 1 {
		t.Fatalf("memory cache: %v %d", err, hits.Load())
	}
	// Disk hit from a fresh store.
	im2 := NewImages(ImagesOptions{Dir: dir, ImageURL: urlFor})
	if _, _, err := im2.Get(ctx, "/poster_1-a.jpg", 300); err != nil || hits.Load() != 1 {
		t.Fatalf("disk cache: %v %d", err, hits.Load())
	}
	if _, _, err := im.Get(ctx, "/missing.jpg", 300); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if _, _, err := im.Get(ctx, "/page.jpg", 300); err == nil {
		t.Fatal("non-image accepted")
	}
	if _, _, err := im.GetURL(ctx, "file:///etc/passwd"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("GetURL scheme: %v", err)
	}
	if data, _, err := im.GetURL(ctx, srv.URL+"/custom/art.jpg"); err != nil || !bytes.Equal(data, jpg) {
		t.Fatalf("GetURL: %v", err)
	}
}

func TestSizeAndDiskTrim(t *testing.T) {
	for w, want := range map[int]string{0: "original", 1: "w92", 300: "w300", 301: "w342", 780: "w780", 1000: "w1280", 5000: "w1280"} {
		if got := Size(w); got != want {
			t.Errorf("Size(%d) = %s, want %s", w, got, want)
		}
	}
	dir := t.TempDir()
	im := NewImages(ImagesOptions{Dir: dir, DiskBytes: 1000})
	for i := 0; i < 10; i++ {
		im.writeDisk(fmt.Sprintf("k%d", i), bytes.Repeat([]byte{byte(i)}, 300))
	}
	if used := dirSize(dir); used > 1000 {
		t.Fatalf("disk cache not trimmed: %d bytes", used)
	}
}
