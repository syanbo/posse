package app

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
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
