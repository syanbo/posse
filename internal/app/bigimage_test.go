package app

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/files"
	"posse/locales"
)

// pngClaiming 是一张 2×2 的 PNG，文件头里的宽高改成 w×h（重算 CRC）：DecodeConfig 读到的是 w×h，
// 不用真的生成几亿像素。真去解码会因为数据不够而失败。
func pngClaiming(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{200, 100, 50, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	out := b.Bytes()
	// 8 字节签名之后是 IHDR：长度(4) 类型(4) 宽(4) 高(4) ……，CRC 覆盖类型和数据（13 字节）
	binary.BigEndian.PutUint32(out[16:], uint32(w))
	binary.BigEndian.PutUint32(out[20:], uint32(h))
	binary.BigEndian.PutUint32(out[29:], crc32.ChecksumIEEE(out[12:29]))
	return out
}

// 原图超过 5000 万像素的不解码，报出尺寸；正好 5000 万和更小的照旧解码。
func TestDecodeDocImageLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		return p
	}
	_, _, err := decodeDocImage(write("big.png", pngClaiming(7072, 7071)))
	if big, ok := err.(imageTooLarge); !ok || big.w != 7072 || big.h != 7071 {
		t.Fatalf("over the limit: %v", err)
	}
	// 正好 5000 万像素：不拦，照旧去解码（这张数据不够，解码失败，但不是超限）
	if _, _, err := decodeDocImage(write("edge.png", pngClaiming(10000, 5000))); err == nil {
		t.Fatal("truncated image decoded")
	} else if _, ok := err.(imageTooLarge); ok {
		t.Fatalf("exactly the limit refused: %v", err)
	}
	// 任一边超过 5 万像素也拦（缩小的累加器和目标宽成正比），正好 5 万的放行
	for _, c := range []struct {
		w, h  int
		block bool
	}{{50000, 1, false}, {50001, 1, true}, {1, 50001, true}, {1, 50000, false}} {
		_, _, err := decodeDocImage(write(fmt.Sprintf("side-%dx%d.png", c.w, c.h), pngClaiming(c.w, c.h)))
		big, blocked := err.(imageTooLarge)
		if blocked != c.block || blocked && (big.w != c.w || big.h != c.h) {
			t.Fatalf("%dx%d: %v, want blocked %v", c.w, c.h, err, c.block)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 30, 20)))
	pi, anim, err := decodeDocImage(write("small.png", b.Bytes()))
	if err != nil || anim != nil || pi.Size != image.Pt(30, 20) {
		t.Fatalf("small image: %+v %v %v", pi.Size, anim, err)
	}
}

// 文件标签打开超大的图：显示「太大，不在这里预览」和它的尺寸，不解码。
func TestOpenHugeImageShowsTooLarge(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	path := filepath.Join(root, "huge.png")
	os.WriteFile(path, pngClaiming(8000, 7000), 0o644)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, path, 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "too large", func() bool { return v.cur.kind == docTooLarge })
	if v.cur.imgInfo.w != 8000 || v.cur.imgInfo.h != 7000 || v.cur.img != nil {
		t.Fatalf("doc %+v img %v", v.cur.imgInfo, v.cur.img)
	}
	want := locales.In("zh-Hans").FilesImageTooLarge(8000, 7000)
	until(t, tt, "too-large message", func() bool { return tt.HasText(want) })
	if tt.HasText(locales.In("zh-Hans").FilesTooLarge(files.MaxSize >> 20)) {
		t.Fatal("text-file message shown for an image")
	}
	v.poll.at = time.Now().Add(time.Hour)
}

// 关掉图片标签后文档和它的位图不再被拽着（跟着当前标签换仓库用的 followDoc 不再指着它），能回收。
func TestClosedImageDocCollected(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a) // 关掉最后一个文件后回到终端
	root := filesWorkspace(t, a)
	path := filepath.Join(root, "pic.png")
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 40, 30)))
	os.WriteFile(path, b.Bytes(), 0o644)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, path, 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "image", func() bool { return v.cur.kind == docImage })
	tt.Frame()
	d := v.cur
	if v.repos.followDoc != d {
		t.Fatal("followDoc not tracking the open tab")
	}
	var gone atomic.Bool
	runtime.AddCleanup(d, func(int) { gone.Store(true) }, 0)
	a.dropDoc(v, d)
	tt.Frame()
	if v.repos.followDoc == d {
		t.Fatal("followDoc still points at the closed tab")
	}
	d = nil
	for range 50 {
		runtime.GC()
		if gone.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("closed image document never collected")
}
