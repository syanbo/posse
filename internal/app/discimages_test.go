package app

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 原图像素超过上限时不解码（只读了文件头），显示成文件标签。
func TestDiscThumbRejectsHugeSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewPaletted(image.Rect(0, 0, 6000, 6000), color.Palette{color.Black})
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := decodeDiscThumb(path); err != errImageTooLarge {
		t.Fatalf("huge image decoded: %v", err)
	}
}

// 挤掉的缩略图留下尺寸：滚回来时占位块和原来一样大。
func TestDiscThumbsEvictKeepsSize(t *testing.T) {
	v := &discThumbs{m: map[string]*discThumb{}}
	for i := range discThumbKeep + 1 {
		th := &discThumb{used: int64(i)}
		th.set(image.NewRGBA(image.Rect(0, 0, 200, 100)))
		v.m[string(rune('a'+i))] = th
	}
	v.evict()
	first := v.m["a"]
	if first.bm != nil || first.w != 100 || first.h != 50 {
		t.Fatalf("oldest not evicted or size lost: %+v", first)
	}
	if v.m[string(rune('a'+discThumbKeep))].bm == nil {
		t.Fatal("newest evicted")
	}
}

// 一帧里看得见的图片比缓存多时，只要前 discThumbKeep 张：其余不抢位置，缓存不会互相挤、一直重解。
func TestDiscThumbWantCapped(t *testing.T) {
	a := previewApp(t)
	v := &a.discThumbs
	v.m = map[string]*discThumb{}
	n := discThumbKeep + 6
	for i := range n {
		th := &discThumb{}
		th.set(image.NewRGBA(image.Rect(0, 0, 48, 48)))
		v.m[string(rune('a'+i))] = th
	}
	for frame := range 3 {
		v.wanted = 0
		for i := range n {
			a.discThumb(string(rune('a'+i)), true)
		}
		if v.busy != 0 {
			t.Fatalf("frame %d started decoding", frame)
		}
	}
	if v.m[string(rune('a'+n-1))].used != 0 {
		t.Fatal("image past the budget was touched")
	}
	v.evict()
	for i := range discThumbKeep {
		if v.m[string(rune('a'+i))].bm == nil {
			t.Fatalf("wanted image %d evicted", i)
		}
	}
}

// 点缩略图在窗口里开大图，解好后显示原图尺寸；Esc 关掉，下一帧放掉位图。
func TestDiscImageViewer(t *testing.T) {
	a := previewApp(t)
	previewDiscussion(a)
	previewDiscImages(t, a)
	a.disc.panel, a.state.SidebarHidden = false, true
	a.disc.follow, a.disc.scroll.Y = true, 1e9
	tt := newPreviewTester(a, false)
	for range 3 {
		tt.Frame()
	}
	// 白图在消息区里的中心（1200×760 的窗口、滚到底）；先移过去再按，和真实的点击一样有悬停
	tt.Move(605, 456)
	tt.Frame()
	tt.Press(605, 456)
	tt.Frame()
	tt.Release(605, 456)
	tt.Frame()
	if !a.discView.open || filepath.Base(a.discView.path) != "white.png" {
		t.Fatalf("viewer not opened: %+v", a.discView)
	}
	for i := 0; i < 50 && a.discView.bm == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
	if a.discView.bm == nil || a.discView.size != image.Pt(900, 240) {
		t.Fatalf("viewer image not decoded: %+v", a.discView)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if a.discView.open || a.discView.bm != nil {
		t.Fatalf("viewer not closed and released: %+v", a.discView)
	}
}

// 存储为：复制到目标位置，覆盖已有的文件，不在目录里留下临时文件。
func TestCopyFileTo(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.png"), filepath.Join(dir, "out", "b.png")
	os.WriteFile(src, []byte("new"), 0o600)
	os.Mkdir(filepath.Dir(dst), 0o755)
	os.WriteFile(dst, []byte("old content"), 0o644)
	if err := copyFileTo(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("dst = %q", b)
	}
	if es, _ := os.ReadDir(filepath.Dir(dst)); len(es) != 1 {
		t.Fatalf("leftover files: %v", es)
	}
	if err := copyFileTo(filepath.Join(dir, "missing.png"), dst); err == nil {
		t.Fatal("missing source copied")
	}
}
