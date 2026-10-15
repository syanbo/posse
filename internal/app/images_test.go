package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
}

func TestListAndScaleImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "2.png"), 2400, 1520)
	writePNG(t, filepath.Join(dir, "10.png"), 300, 200)
	writePNG(t, filepath.Join(dir, "note.png"), 10, 10) // 不是序号命名
	os.WriteFile(filepath.Join(dir, "3.txt"), nil, 0o644)
	items := listImages(dir)
	if len(items) != 2 || items[0].n != 10 || items[1].n != 2 {
		t.Fatalf("items = %+v", items)
	}
	big, err := scaledImage(items[1].path, thumbWidth)
	if err != nil {
		t.Fatal(err)
	}
	if b := big.Bounds(); b.Dx() != thumbWidth || b.Dy() != 1520*thumbWidth/2400 {
		t.Fatalf("scaled to %v", b)
	}
	// 本来就小的不放大
	small, _ := scaledImage(items[0].path, thumbWidth)
	if b := small.Bounds(); b.Dx() != 300 || b.Dy() != 200 {
		t.Fatalf("small scaled to %v", b)
	}
	if got := claudeImageDir("../x", ""); got != "" {
		t.Fatalf("bad session id gave %q", got)
	}
}
