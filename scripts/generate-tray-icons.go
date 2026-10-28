//go:build ignore

// Regenerates cmd/ether-tray/assets icons: go run scripts/generate-tray-icons.go cmd/ether-tray/assets
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
)

// Use the raster master rendered from Continuum's SVG. Averaging alpha keeps
// the curves antialiased at tray sizes; each platform gets its own ink color.
func draw(source image.Image, size int, fg color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bounds := source.Bounds()
	fr, fgreen, fb, fa := fg.RGBA()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			x0, x1 := bounds.Min.X+x*bounds.Dx()/size, bounds.Min.X+(x+1)*bounds.Dx()/size
			y0, y1 := bounds.Min.Y+y*bounds.Dy()/size, bounds.Min.Y+(y+1)*bounds.Dy()/size
			var sum uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					_, _, _, alpha := source.At(sx, sy).RGBA()
					sum += uint64(alpha)
				}
			}
			samples := (x1 - x0) * (y1 - y0)
			alpha := uint8(sum / uint64(samples) * uint64(fa) / 65535 / 257)
			img.SetNRGBA(x, y, color.NRGBA{uint8(fr / 257), uint8(fgreen / 257), uint8(fb / 257), alpha})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func main() {
	dir := os.Args[1]
	sourceFile, err := os.Open("web/public/brand/ether-mark.png")
	if err != nil {
		panic(err)
	}
	defer sourceFile.Close()
	source, err := png.Decode(sourceFile)
	if err != nil {
		panic(err)
	}
	black := color.NRGBA{0, 0, 0, 255}
	white := color.NRGBA{255, 255, 255, 255}
	gray := color.NRGBA{140, 140, 140, 255}
	os.WriteFile(dir+"/icon-template.png", draw(source, 32, black), 0644) // macOS template (alpha shape)
	os.WriteFile(dir+"/icon.png", draw(source, 64, black), 0644)
	os.WriteFile(dir+"/icon-white.png", draw(source, 64, white), 0644)
	os.WriteFile(dir+"/icon-off.png", draw(source, 64, gray), 0644)
	// ICO wrapping the 32px black PNG
	png32 := draw(source, 32, black)
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, uint16(0))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	ico.WriteByte(32)
	ico.WriteByte(32)
	ico.WriteByte(0)
	ico.WriteByte(0)
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(32))
	binary.Write(&ico, binary.LittleEndian, uint32(len(png32)))
	binary.Write(&ico, binary.LittleEndian, uint32(6+16))
	ico.Write(png32)
	os.WriteFile(dir+"/icon.ico", ico.Bytes(), 0644)
}
