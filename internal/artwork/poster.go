package artwork

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // decode PNG artwork
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/braedonsaunders/ether/internal/flight"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp" // decode WebP artwork
)

// PosterRequest describes a collection poster.
type PosterRequest struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"` // small caps line above the title, e.g. "24 FILMS"
	Mode     string   `json:"mode"`               // auto | collage | backdrop
	Accent   string   `json:"accent,omitempty"`   // "#rrggbb"; derived from the artwork when empty
	Posters  []string `json:"posters,omitempty"`  // member poster paths (up to 6 used)
	// Backdrops are member backdrop paths (the first that loads is used).
	Backdrops []string `json:"backdrops,omitempty"`
	Width     int      `json:"width"` // output width; height is 1.5×
}

// ImageSource loads TMDB images; *Images satisfies it.
type ImageSource interface {
	Get(ctx context.Context, path string, w int) ([]byte, string, error)
}

// Renderer makes collection posters and caches them on disk, keyed by
// everything that affects the pixels (title, mode, accent, members, size).
type Renderer struct {
	src ImageSource
	dir string
	log *slog.Logger

	flights flight.Group[string, []byte]
}

// NewRenderer creates a renderer. dir "" disables the disk cache.
func NewRenderer(src ImageSource, dir string, log *slog.Logger) *Renderer {
	if log == nil {
		log = slog.Default()
	}
	if dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return &Renderer{src: src, dir: dir, log: log}
}

const (
	defaultWidth = 600
	minWidth     = 150
	maxWidth     = 1200
)

func clampWidth(w int) int {
	switch {
	case w <= 0:
		return defaultWidth
	case w < minWidth:
		return minWidth
	case w > maxWidth:
		return maxWidth
	}
	return w
}

// Poster returns a JPEG poster for req.
func (r *Renderer) Poster(ctx context.Context, req PosterRequest) ([]byte, string, error) {
	req.Width = clampWidth(req.Width)
	if len(req.Posters) > 6 {
		req.Posters = req.Posters[:6]
	}
	if len(req.Backdrops) > 3 {
		req.Backdrops = req.Backdrops[:3]
	}
	key := cacheKey(req)
	path := ""
	if r.dir != "" {
		path = filepath.Join(r.dir, key+".jpg")
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data, "image/jpeg", nil
		}
	}
	data, err := r.flights.DoDetached(ctx, key, func(ctx context.Context) ([]byte, error) {
		data, transient, err := r.render(ctx, req)
		// A poster drawn while member art was temporarily unreachable is
		// served but not persisted, so the next request retries the real
		// artwork.
		if err == nil && path != "" && !transient {
			tmp := path + ".tmp"
			if werr := os.WriteFile(tmp, data, 0o644); werr == nil {
				if os.Rename(tmp, path) != nil {
					_ = os.Remove(tmp)
				}
			}
			r.trim()
		}
		return data, err
	})
	return data, "image/jpeg", err
}

// trim keeps the poster cache under 200 MiB.
func (r *Renderer) trim() {
	if dirSize(r.dir) > 200<<20 {
		trimDir(r.dir, 160<<20)
	}
}

func cacheKey(req PosterRequest) string {
	b, _ := json.Marshal(req)
	sum := sha1.Sum(append([]byte("v1:"), b...))
	return hex.EncodeToString(sum[:])[:24]
}

// render draws the poster. transient reports that some member art failed
// for a reason other than being absent upstream (timeout, 5xx, ...).
func (r *Renderer) render(ctx context.Context, req PosterRequest) (data []byte, transient bool, err error) {
	w := req.Width
	type loaded struct {
		img       image.Image
		ok        bool
		transient bool
	}
	posters := make([]loaded, len(req.Posters))
	backdrops := make([]loaded, len(req.Backdrops))
	var wg sync.WaitGroup
	load := func(dst *loaded, path string, width int) {
		defer wg.Done()
		if r.src == nil {
			return
		}
		data, _, err := r.src.Get(ctx, path, width)
		if err != nil {
			dst.transient = !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalidPath)
			return
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return
		}
		dst.img, dst.ok = img, true
	}
	tile := w / 2
	for i, p := range req.Posters {
		wg.Add(1)
		go load(&posters[i], p, tile)
	}
	mode := req.Mode
	if mode != "collage" {
		for i, p := range req.Backdrops {
			wg.Add(1)
			go load(&backdrops[i], p, w*3)
		}
	}
	wg.Wait()
	var ps, bs []image.Image
	for _, p := range posters {
		if p.ok {
			ps = append(ps, p.img)
		}
		transient = transient || p.transient
	}
	for _, b := range backdrops {
		if b.ok {
			bs = append(bs, b.img)
		}
		transient = transient || b.transient
	}
	img := Compose(req, ps, bs)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), transient, nil
}

// Compose draws a poster from already-decoded images. It is pure, so tests
// can check its output without a network.
//
//	collage   2×2 (4+ posters) or 3×2 (6 posters) tiles under a gradient
//	backdrop  a member backdrop cropped to portrait with a title treatment
//	auto      collage with 4+ posters, else backdrop, else a single poster,
//	          else a typographic poster in the accent colour
func Compose(req PosterRequest, posters, backdrops []image.Image) *image.RGBA {
	w := clampWidth(req.Width)
	h := w * 3 / 2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))

	accent, ok := parseHex(req.Accent)
	if !ok {
		switch {
		case len(backdrops) > 0 && req.Mode != "collage":
			accent = deriveAccent(backdrops[0])
		case len(posters) > 0:
			accent = deriveAccent(posters[0])
		default:
			accent = color.RGBA{0x1f, 0x2a, 0x44, 0xff}
		}
	}
	dark := shade(accent, 0.35)

	mode := req.Mode
	if mode == "" || mode == "auto" {
		switch {
		case len(posters) >= 4:
			mode = "collage"
		case len(backdrops) > 0:
			mode = "backdrop"
		case len(posters) > 0:
			mode = "poster"
		default:
			mode = "type"
		}
	}
	if mode == "collage" && len(posters) < 4 {
		mode = "backdrop"
	}
	if mode == "backdrop" && len(backdrops) == 0 {
		if len(posters) > 0 {
			mode = "poster"
		} else {
			mode = "type"
		}
	}

	textTop := 0.62 // where the title block may start, as a fraction of h
	switch mode {
	case "collage":
		fillGradient(dst, dark, shade(accent, 0.15))
		drawCollage(dst, posters)
		// Fade the lower part into the accent so the title sits on colour.
		verticalFade(dst, 0.42, 0.78, shade(accent, 0.22), 0.96)
		textTop = 0.66
	case "backdrop":
		cover(dst, backdrops[0], 0.5)
		tint(dst, accent, 0.18)
		verticalFade(dst, 0.30, 0.92, shade(accent, 0.18), 0.94)
		verticalFadeTop(dst, 0.18, color.RGBA{0, 0, 0, 255}, 0.45)
	case "poster":
		cover(dst, posters[0], 0.5)
		tint(dst, accent, 0.12)
		verticalFade(dst, 0.40, 0.90, shade(accent, 0.18), 0.95)
	default: // type
		diagonalGradient(dst, shade(accent, 0.75), shade(accent, 0.22))
		drawRings(dst, shade(accent, 1.25))
		textTop = 0.38
	}
	drawTitleBlock(dst, req.Title, req.Subtitle, accent, textTop)
	return dst
}

// ---------------------------------------------------------------------------
// Layout

func drawCollage(dst *image.RGBA, posters []image.Image) {
	w := dst.Bounds().Dx()
	cols, rows := 2, 2
	if len(posters) >= 6 {
		cols, rows = 3, 2
	}
	gap := max(2, w/160)
	tileW := (w - gap*(cols+1)) / cols
	tileH := tileW * 3 / 2
	// Slight upward offset so the top row is cropped like a film strip.
	y0 := gap
	if cols == 2 {
		y0 = -tileH / 6
	}
	i := 0
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if i >= len(posters) {
				return
			}
			x := gap + c*(tileW+gap)
			y := y0 + r*(tileH+gap)
			rect := image.Rect(x, y, x+tileW, y+tileH).Intersect(dst.Bounds())
			if !rect.Empty() {
				coverRect(dst, image.Rect(x, y, x+tileW, y+tileH), posters[i])
			}
			i++
		}
	}
}

// cover scales src to fill dst (cropping), centred at focusX.
func cover(dst *image.RGBA, src image.Image, focusX float64) {
	b := dst.Bounds()
	sb := src.Bounds()
	scale := math.Max(float64(b.Dx())/float64(sb.Dx()), float64(b.Dy())/float64(sb.Dy()))
	sw := int(math.Ceil(float64(sb.Dx()) * scale))
	sh := int(math.Ceil(float64(sb.Dy()) * scale))
	ox := int(float64(sw-b.Dx()) * focusX)
	oy := (sh - b.Dy()) / 2
	target := image.Rect(-ox, -oy, -ox+sw, -oy+sh)
	draw.ApproxBiLinear.Scale(dst, target, src, sb, draw.Src, nil)
}

// coverRect scales src to fill r on dst (cropping to r).
func coverRect(dst *image.RGBA, r image.Rectangle, src image.Image) {
	sb := src.Bounds()
	scale := math.Max(float64(r.Dx())/float64(sb.Dx()), float64(r.Dy())/float64(sb.Dy()))
	sw := int(math.Ceil(float64(sb.Dx()) * scale))
	sh := int(math.Ceil(float64(sb.Dy()) * scale))
	ox := (sw - r.Dx()) / 2
	oy := (sh - r.Dy()) / 2
	tile := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.CatmullRom.Scale(tile, image.Rect(-ox, -oy, -ox+sw, -oy+sh), src, sb, draw.Src, nil)
	draw.Draw(dst, r.Intersect(dst.Bounds()), tile, image.Point{X: max(0, -r.Min.X), Y: max(0, -r.Min.Y)}, draw.Src)
}

func fillGradient(dst *image.RGBA, top, bottom color.RGBA) {
	b := dst.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		t := float64(y-b.Min.Y) / float64(max(1, b.Dy()-1))
		c := lerp(top, bottom, t)
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.SetRGBA(x, y, c)
		}
	}
}

func diagonalGradient(dst *image.RGBA, a, b color.RGBA) {
	r := dst.Bounds()
	den := float64(r.Dx() + r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dst.SetRGBA(x, y, lerp(a, b, float64(x+y)/den))
		}
	}
}

// drawRings adds soft concentric arcs to typographic posters.
func drawRings(dst *image.RGBA, c color.RGBA) {
	b := dst.Bounds()
	cx, cy := float64(b.Dx())*0.85, float64(b.Dy())*0.12
	step := float64(b.Dx()) / 9
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			m := math.Mod(d, step) / step
			if m < 0.06 {
				dst.SetRGBA(x, y, lerp(dst.RGBAAt(x, y), c, 0.18))
			}
		}
	}
}

// verticalFade blends towards c from fraction from (alpha 0) to fraction
// to (alpha maxA) and holds maxA below.
func verticalFade(dst *image.RGBA, from, to float64, c color.RGBA, maxA float64) {
	b := dst.Bounds()
	h := float64(b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		f := float64(y-b.Min.Y) / h
		var a float64
		switch {
		case f <= from:
			continue
		case f >= to:
			a = maxA
		default:
			t := (f - from) / (to - from)
			a = maxA * t * t * (3 - 2*t) // smoothstep
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.SetRGBA(x, y, lerp(dst.RGBAAt(x, y), c, a))
		}
	}
}

// verticalFadeTop darkens the top edge (a vignette for backdrops).
func verticalFadeTop(dst *image.RGBA, to float64, c color.RGBA, maxA float64) {
	b := dst.Bounds()
	h := float64(b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		f := float64(y-b.Min.Y) / h
		if f >= to {
			return
		}
		a := maxA * (1 - f/to)
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.SetRGBA(x, y, lerp(dst.RGBAAt(x, y), c, a))
		}
	}
}

func tint(dst *image.RGBA, c color.RGBA, a float64) {
	b := dst.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.SetRGBA(x, y, lerp(dst.RGBAAt(x, y), c, a))
		}
	}
}

// ---------------------------------------------------------------------------
// Text

var (
	fontOnce          sync.Once
	boldFont, medFont *opentype.Font
	errFont           error
)

func fonts() (*opentype.Font, *opentype.Font, error) {
	fontOnce.Do(func() {
		boldFont, errFont = opentype.Parse(gobold.TTF)
		if errFont == nil {
			medFont, errFont = opentype.Parse(gomedium.TTF)
		}
	})
	return boldFont, medFont, errFont
}

// face makes a font face. Faces are not safe for concurrent use, so each
// render makes its own.
func face(f *opentype.Font, size float64) font.Face {
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	return fc
}

// drawTitleBlock sets the subtitle, an accent rule and the title (wrapped
// to at most three lines, sized to fit) in the lower part of the poster.
func drawTitleBlock(dst *image.RGBA, title, subtitle string, accent color.RGBA, top float64) {
	bold, med, err := fonts()
	if err != nil {
		return
	}
	b := dst.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	maxW := w * 0.86
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Collection"
	}
	var lines []string
	var fc font.Face
	size := w * 0.115
	for ; size >= w*0.05; size *= 0.92 {
		if fc != nil {
			fc.Close()
		}
		fc = face(bold, size)
		if fc == nil {
			return
		}
		lines = wrap(fc, title, maxW)
		if len(lines) <= 3 && widest(fc, lines) <= maxW {
			break
		}
	}
	if len(lines) > 3 {
		lines = lines[:3]
		lines[2] = ellipsize(fc, lines[2], maxW)
	}
	lineH := size * 1.08
	blockH := lineH * float64(len(lines))
	subSize := math.Max(10, w*0.032)
	gapSub := subSize * 1.6
	bottomPad := h * 0.075
	y := h - bottomPad - blockH
	minY := h * top
	if subtitle != "" {
		minY += gapSub
	}
	if y < minY {
		y = minY
	}
	if subtitle != "" {
		sf := face(med, subSize)
		if sf != nil {
			defer sf.Close()
			sub := strings.ToUpper(subtitle)
			track := subSize * 0.18
			sw := measureTracked(sf, sub, track)
			sy := y - gapSub + subSize*0.2
			light := shade(accent, 1.9)
			drawTracked(dst, sf, sub, (w-sw)/2, sy, track, color.RGBA{light.R, light.G, light.B, 235})
			// Accent rule between subtitle and title.
			ruleW := w * 0.08
			ry := int(sy + subSize*0.55)
			rx0 := int((w - ruleW) / 2)
			for yy := ry; yy < ry+max(2, int(w/300)); yy++ {
				for xx := rx0; xx < rx0+int(ruleW); xx++ {
					if image.Pt(xx, yy).In(b) {
						dst.SetRGBA(xx, yy, shade(accent, 1.6))
					}
				}
			}
		}
	}
	defer fc.Close()
	for i, line := range lines {
		lw := measure(fc, line)
		x := (w - lw) / 2
		baseline := y + lineH*float64(i) + size*0.82
		shadow := math.Max(1, size/22)
		drawString(dst, fc, line, x+shadow, baseline+shadow, color.RGBA{0, 0, 0, 150})
		drawString(dst, fc, line, x, baseline, color.RGBA{255, 255, 255, 255})
	}
}

func drawString(dst *image.RGBA, fc font.Face, s string, x, y float64, c color.RGBA) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: fc,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	d.DrawString(s)
}

func measure(fc font.Face, s string) float64 {
	return float64(font.MeasureString(fc, s)) / 64
}

func widest(fc font.Face, lines []string) float64 {
	m := 0.0
	for _, l := range lines {
		m = math.Max(m, measure(fc, l))
	}
	return m
}

// wrap breaks text into lines no wider than maxW (long words stay whole).
func wrap(fc font.Face, text string, maxW float64) []string {
	words := strings.Fields(text)
	var lines []string
	cur := ""
	for _, word := range words {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if cur != "" && measure(fc, try) > maxW {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur = try
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func ellipsize(fc font.Face, s string, maxW float64) string {
	r := []rune(s)
	for len(r) > 1 && measure(fc, string(r)+"…") > maxW {
		r = r[:len(r)-1]
	}
	return strings.TrimRightFunc(string(r), unicode.IsSpace) + "…"
}

// ---------------------------------------------------------------------------
// Colour

// measureTracked measures s with extra tracking (letter spacing) in pixels.
func measureTracked(fc font.Face, s string, track float64) float64 {
	n := 0
	for range s {
		n++
	}
	if n == 0 {
		return 0
	}
	return measure(fc, s) + track*float64(n-1)
}

// drawTracked draws s rune by rune with extra tracking.
func drawTracked(dst *image.RGBA, fc font.Face, s string, x, y, track float64, c color.RGBA) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: fc,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			d.Dot.X += fc.Kern(prev, r) + fixed.Int26_6(track*64)
		}
		d.DrawString(string(r))
		prev = r
	}
}

func parseHex(s string) (color.RGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, true
}

// deriveAccent picks a rich colour from an image: the average of its more
// saturated pixels, normalised to a mid lightness.
func deriveAccent(img image.Image) color.RGBA {
	b := img.Bounds()
	step := max(1, min(b.Dx(), b.Dy())/48)
	var rs, gs, bs, ws float64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, _ := img.At(x, y).RGBA()
			fr, fg, fb := float64(r>>8), float64(g>>8), float64(bl>>8)
			mx := math.Max(fr, math.Max(fg, fb))
			mn := math.Min(fr, math.Min(fg, fb))
			sat := 0.0
			if mx > 0 {
				sat = (mx - mn) / mx
			}
			wgt := 0.05 + sat*sat
			if mx < 25 || mn > 235 {
				wgt *= 0.1 // near black/white says little about the palette
			}
			rs += fr * wgt
			gs += fg * wgt
			bs += fb * wgt
			ws += wgt
		}
	}
	if ws == 0 {
		return color.RGBA{0x1f, 0x2a, 0x44, 0xff}
	}
	c := color.RGBA{uint8(rs / ws), uint8(gs / ws), uint8(bs / ws), 255}
	// Normalise brightness so text stays legible on the derived tone.
	lum := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	if lum < 1 {
		return color.RGBA{0x1f, 0x2a, 0x44, 0xff}
	}
	return shade(c, 110/lum)
}

// shade scales a colour's channels (k < 1 darkens, k > 1 lightens towards
// white).
func shade(c color.RGBA, k float64) color.RGBA {
	f := func(v uint8) uint8 {
		x := float64(v)
		if k <= 1 {
			x *= k
		} else {
			x = x + (255-x)*(1-1/k)
		}
		return uint8(math.Max(0, math.Min(255, math.Round(x))))
	}
	return color.RGBA{f(c.R), f(c.G), f(c.B), 255}
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return color.RGBA{b.R, b.G, b.B, 255}
	}
	m := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 255}
}

// String describes a request (logs).
func (r PosterRequest) String() string {
	return fmt.Sprintf("%q mode=%s posters=%d backdrops=%d w=%d", r.Title, r.Mode, len(r.Posters), len(r.Backdrops), r.Width)
}
