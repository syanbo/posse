package collaboration

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// 宽图从中间裁：左红、中绿、右蓝三等份，裁出来应是绿色。
func TestNormalizeAvatarCropsCenter(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 900, 300))
	for x := range 900 {
		c := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}}[x/300]
		for y := range 300 {
			src.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, err := NormalizeAvatar(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []image.Point{{10, 10}, {128, 128}, {245, 245}} {
		r, g, b, _ := img.At(p.X, p.Y).RGBA()
		if g>>8 < 200 || r>>8 > 60 || b>>8 > 60 {
			t.Fatalf("pixel %v is %d,%d,%d, want green", p, r>>8, g>>8, b>>8)
		}
	}
}
