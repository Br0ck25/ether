package overlays

import (
	"bytes"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func normalizeSettings(v Settings) Settings {
	if v.Style == "" {
		v.Style = "glass"
	}
	if v.Position == "" {
		v.Position = "bottom"
	}
	if v.Accent == "" {
		v.Accent = "#d4a574"
	}
	if v.Size == "" {
		v.Size = "medium"
	}
	v.Accent = strings.ToLower(v.Accent)
	return v
}

func validateSettings(v Settings) error {
	switch v.Style {
	case "glass", "solid", "minimal":
	default:
		return errors.New("style must be glass, solid, or minimal")
	}
	switch v.Position {
	case "bottom", "top":
	default:
		return errors.New("position must be bottom or top")
	}
	switch v.Size {
	case "small", "medium", "large":
	default:
		return errors.New("size must be small, medium, or large")
	}
	if len(v.Accent) != 7 || v.Accent[0] != '#' {
		return errors.New("accent must be a six-digit hex color")
	}
	if _, err := hex.DecodeString(v.Accent[1:]); err != nil {
		return errors.New("accent must be a six-digit hex color")
	}
	return nil
}

// Each render owns its font face: opentype faces contain mutable glyph buffers.
func renderFace(size float64) (font.Face, error) {
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func rounded(dst *image.RGBA, rect image.Rectangle, radius int, c color.NRGBA) {
	// An alpha mask preserves both the source image and the straight-alpha fill.
	mask := image.NewAlpha(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			cx := max(rect.Min.X+radius, min(x, rect.Max.X-radius-1))
			cy := max(rect.Min.Y+radius, min(y, rect.Max.Y-radius-1))
			dist := math.Hypot(float64(x-cx), float64(y-cy))
			coverage := math.Max(0, math.Min(1, float64(radius)+0.5-dist))
			mask.SetAlpha(x, y, color.Alpha{A: uint8(255 * coverage)})
		}
	}
	draw.DrawMask(dst, rect, image.NewUniform(c), image.Point{}, mask, rect.Min, draw.Over)
}

func paintBadges(dst *image.RGBA, labels []string, cfg Settings) error {
	if len(labels) == 0 {
		return nil
	}
	size := 18.0
	switch cfg.Size {
	case "small":
		size = 15
	case "large":
		size = 22
	}
	face, err := renderFace(size)
	if err != nil {
		return err
	}
	defer face.Close()
	accentBytes, _ := hex.DecodeString(cfg.Accent[1:])
	accent := color.NRGBA{R: accentBytes[0], G: accentBytes[1], B: accentBytes[2], A: 255}
	padding, gap, margin := 13, 8, 22
	height := int(size) + 22
	maxWidth := dst.Bounds().Dx() - 2*margin
	type badge struct {
		text          string
		x, row, width int
	}
	var badges []badge
	x, row := margin, 0
	for _, label := range labels[:min(len(labels), 6)] {
		label = strings.Map(func(r rune) rune {
			if r < 32 {
				return -1
			}
			return r
		}, label)
		runes := []rune(label)
		for len(runes) > 0 && font.MeasureString(face, string(runes)).Ceil() > maxWidth-2*padding-12 {
			runes = runes[:len(runes)-1]
		}
		text := string(runes)
		if text != label && len(runes) > 0 {
			text = string(runes[:len(runes)-1]) + "…"
		}
		width := font.MeasureString(face, text).Ceil() + 2*padding + 12
		if x+width > dst.Bounds().Dx()-margin && x > margin {
			row++
			x = margin
		}
		badges = append(badges, badge{text: text, x: x, row: row, width: width})
		x += width + gap
	}
	totalHeight := (row+1)*height + row*gap
	startY := dst.Bounds().Dy() - margin - totalHeight
	if cfg.Position == "top" {
		startY = margin
	}
	// A restrained scrim under the badges keeps text readable on bright artwork.
	for offset := 0; offset < totalHeight+margin*2; offset++ {
		y := dst.Bounds().Dy() - 1 - offset
		if cfg.Position == "top" {
			y = offset
		}
		alpha := uint8(95 * math.Max(0, 1-float64(offset)/float64(totalHeight+margin*2)))
		draw.Draw(dst, image.Rect(0, y, dst.Bounds().Dx(), y+1), image.NewUniform(color.NRGBA{A: alpha}), image.Point{}, draw.Over)
	}
	for _, b := range badges {
		y := startY + b.row*(height+gap)
		rect := image.Rect(b.x, y, b.x+b.width, y+height)
		switch cfg.Style {
		case "solid":
			rounded(dst, rect, 10, color.NRGBA{R: 15, G: 20, B: 29, A: 255})
		case "minimal":
			rounded(dst, rect, 8, color.NRGBA{R: 10, G: 14, B: 22, A: 160})
		default:
			rounded(dst, rect, 11, color.NRGBA{R: 255, G: 255, B: 255, A: 60})
			rounded(dst, rect.Inset(1), 10, color.NRGBA{R: 18, G: 24, B: 34, A: 215})
		}
		rounded(dst, image.Rect(b.x+padding, y+height/2-3, b.x+padding+6, y+height/2+3), 3, accent)
		d := font.Drawer{Dst: dst, Src: image.NewUniform(color.NRGBA{R: 246, G: 245, B: 241, A: 255}), Face: face, Dot: fixed.P(b.x+padding+12, y+(height-face.Metrics().Height.Ceil())/2+face.Metrics().Ascent.Ceil())}
		d.DrawString(b.text)
	}
	return nil
}

// Sample is an original offline illustration. Its fictional facts are deliberately
// separate from Labels, which accepts only observed catalog/release information.
func (s *Service) Sample(cfg Settings) ([]byte, string, error) {
	cfg = normalizeSettings(cfg)
	if err := validateSettings(cfg); err != nil {
		return nil, "", err
	}
	img := image.NewRGBA(image.Rect(0, 0, 500, 750))
	for y := 0; y < 750; y++ {
		for x := 0; x < 500; x++ {
			glow := math.Exp(-(math.Pow(float64(x-300)/210, 2) + math.Pow(float64(y-240)/210, 2)))
			grain := float64((x*17+y*31)%9) - 4
			img.SetRGBA(x, y, color.RGBA{R: uint8(15 + glow*100 + grain), G: uint8(27 + glow*65 + grain), B: uint8(41 + glow*48 + grain), A: 255})
		}
	}
	// Sunset disk, layered coastal cliffs, and water make a legible poster at any size.
	rounded(img, image.Rect(220, 130, 360, 270), 70, color.NRGBA{R: 239, G: 195, B: 143, A: 255})
	for y := 330; y < 750; y++ {
		for x := 0; x < 500; x++ {
			ridge := 355 + 100*math.Sin(float64(x)/125) + 35*math.Cos(float64(x)/43)
			if float64(y) > ridge {
				img.SetRGBA(x, y, color.RGBA{R: 20, G: 47, B: 57, A: 255})
			}
			ridge2 := 490 + 65*math.Cos(float64(x)/95)
			if float64(y) > ridge2 {
				img.SetRGBA(x, y, color.RGBA{R: 11, G: 31, B: 43, A: 255})
			}
			if y > 580 && y%11 == 0 && x > 170 && x < 390 {
				img.SetRGBA(x, y, color.RGBA{R: 40, G: 58, B: 62, A: 255})
			}
		}
	}
	face, err := renderFace(45)
	if err != nil {
		return nil, "", err
	}
	defer face.Close()
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.NRGBA{R: 244, G: 234, B: 218, A: 255}), Face: face}
	for i, line := range []string{"AFTER", "THE TIDE"} {
		d.Dot = fixed.P((500-font.MeasureString(face, line).Ceil())/2, 460+i*52)
		d.DrawString(line)
	}
	small, err := renderFace(11)
	if err != nil {
		return nil, "", err
	}
	defer small.Close()
	d.Face = small
	caption := "ETHER ORIGINAL ART • SAMPLE FACTS"
	d.Dot = fixed.P((500-font.MeasureString(small, caption).Ceil())/2, 545)
	d.DrawString(caption)
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 92}); err != nil {
		return nil, "", err
	}
	var labels []string
	if cfg.Enabled {
		if cfg.Status {
			labels = append(labels, "NEW SEASON")
		}
		if cfg.Quality {
			labels = append(labels, "4K HDR")
		}
		if cfg.Ratings {
			labels = append(labels, "IMDb 8.6")
		}
	}
	return s.composite(raw.Bytes(), labels, cfg)
}
