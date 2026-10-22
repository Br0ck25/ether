package transcode

import (
	"bytes"
	"github.com/braedonsaunders/ether/internal/core"
	"strings"
	"testing"
)

func TestReplacementPictureBars(t *testing.T) {
	orig, repl := yellowstone()
	orig.Video().Width, orig.Video().Height = 1440, 1080
	repl.Video().Width, repl.Video().Height = 1280, 720
	c, _ := contractOf(orig, copyMKV())
	sample := func(left, right int) []byte {
		b := make([]byte, 320*180*8)
		for f := 0; f < 8; f++ {
			for y := 0; y < 180; y++ {
				for x := left; x < right; x++ {
					b[f*320*180+y*320+x] = 100 + byte(f)
				}
			}
		}
		return b
	}
	for _, tc := range []struct {
		name string
		b    []byte
		want bool
	}{
		{"cheers-pillars", sample(35, 285), true},
		{"true-widescreen", sample(0, 320), false},
		{"black-scene", make([]byte, 320*180*8), false},
		{"one-sided", sample(35, 320), false},
		{"wrong-aspect", sample(70, 250), false},
		{"incomplete", sample(35, 285)[:320*180*7], false},
		{"changing", append(sample(35, 285)[:320*180*7], sample(20, 300)[:320*180]...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crop := detectPictureBars(tc.b, 320, 180, repl.Video(), c)
			if (crop != nil) != tc.want {
				t.Fatalf("crop=%+v", crop)
			}
			if crop == nil {
				return
			}
			if crop.x != 140 || crop.width != 1000 || crop.height != 720 {
				t.Fatalf("crop=%+v", crop)
			}
			d := adaptToContract(copyMKV(), orig, repl, ContainerMatroska, c)
			spec := contractSpec(t, repl, d, c)
			spec.PictureCrop = crop
			for _, hw := range []bool{false, true} {
				if hw {
					spec.Accel = core.HWAccel{Name: AccelVideoToolbox, Encoders: []string{"hevc_videotoolbox"}}
				}
				args := strings.Join(buildArgs(spec), " ")
				mustContain(t, []string{args}, "crop=1000:720:140:0,scale=1440:1036,pad=1440:1080")
				mustNotContain(t, []string{args}, "scale_vt", "hwaccel_output_format")
			}
		})
	}
}

func TestPictureSampleBound(t *testing.T) {
	b := &pictureSample{limit: 4}
	if _, err := b.Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte{5}); err == nil || !bytes.Equal(b.Bytes(), []byte{1, 2, 3, 4}) {
		t.Fatal("unbounded sample")
	}
}
