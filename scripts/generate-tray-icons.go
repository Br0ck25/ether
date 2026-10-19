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

// Draw a simple monochrome "E" glyph (three horizontal bars + spine) so the
// icon reads at 16px and works as a macOS template image (alpha only).
func draw(size int, fg color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	m := size / 6 // margin
	t := size / 6 // stroke
	if t < 1 {
		t = 1
	}
	// spine
	for y := m; y < size-m; y++ {
		for x := m; x < m+t; x++ {
			img.Set(x, y, fg)
		}
	}
	bar := func(y0, x1 int) {
		for y := y0; y < y0+t; y++ {
			for x := m; x < x1; x++ {
				img.Set(x, y, fg)
			}
		}
	}
	bar(m, size-m)              // top
	bar(size/2-t/2, size-m-t/2) // middle (shorter)
	bar(size-m-t, size-m)       // bottom
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func main() {
	dir := os.Args[1]
	black := color.NRGBA{0, 0, 0, 255}
	white := color.NRGBA{255, 255, 255, 255}
	gray := color.NRGBA{140, 140, 140, 255}
	os.WriteFile(dir+"/icon-template.png", draw(32, black), 0644) // macOS template (alpha shape)
	os.WriteFile(dir+"/icon.png", draw(64, black), 0644)
	os.WriteFile(dir+"/icon-white.png", draw(64, white), 0644)
	os.WriteFile(dir+"/icon-off.png", draw(64, gray), 0644)
	// ICO wrapping the 32px black PNG
	png32 := draw(32, black)
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
