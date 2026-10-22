package app

import (
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func imageDocForTest(w, h int) *fileDoc {
	d := &fileDoc{kind: docImage, img: ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, w, h)))}
	d.imgInfo = imageInfo{w: w, h: h}
	d.iv.vw, d.iv.vh, d.iv.dpr = 800, 600, 2
	return d
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 0.01 }

func TestImageFitZoom(t *testing.T) {
	// 600×1300 的图在 2 倍屏上原大是 300×650 DIP，高度放不下 600-32
	if z := imageDocForTest(600, 1300).fitZoom(); !near(z, 568.0/650) {
		t.Fatalf("fit = %v", z)
	}
	// 比窗口小的图不放大
	if z := imageDocForTest(100, 100).fitZoom(); z != 1 {
		t.Fatalf("small fit = %v", z)
	}
}

func TestImageZoomKeepsPointUnderPointer(t *testing.T) {
	d := imageDocForTest(2000, 2000)
	d.setZoom(1, 600, 200)
	at := func(mx, my float32) (float32, float32) {
		s := d.imageScale(d.curZoom())
		return d.iv.cx + (mx-d.iv.vw/2)/s, d.iv.cy + (my-d.iv.vh/2)/s
	}
	x0, y0 := at(500, 300)
	d.setZoom(2, 500, 300)
	if x1, y1 := at(500, 300); !near(x0, x1) || !near(y0, y1) {
		t.Fatalf("point moved: %v,%v → %v,%v", x0, y0, x1, y1)
	}
}

func TestImageClamp(t *testing.T) {
	d := imageDocForTest(2000, 2000)
	d.setZoom(1, 400, 300)
	// 原大 1000×1000 DIP，视口 800×600：中心最多到边上半个视口
	d.iv.cx, d.iv.cy = -500, 5000
	d.clampImage()
	if !near(d.iv.cx, 800) || !near(d.iv.cy, 2000-600) {
		t.Fatalf("clamped to %v,%v", d.iv.cx, d.iv.cy)
	}
	// 比视口小的方向居中
	d = imageDocForTest(600, 2000)
	d.setZoom(1, 400, 300)
	d.iv.cx = 0
	d.clampImage()
	if !near(d.iv.cx, 300) {
		t.Fatalf("narrow image cx = %v", d.iv.cx)
	}
}
