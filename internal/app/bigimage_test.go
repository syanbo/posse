package app

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
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

// 原图超过 1 亿像素的不解码，报出尺寸；正好 1 亿和更小的照旧解码。
func TestDecodeDocImageLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		return p
	}
	_, _, err := decodeDocImage(write("big.png", pngClaiming(10001, 10000)))
	if big, ok := err.(imageTooLarge); !ok || big.w != 10001 || big.h != 10000 {
		t.Fatalf("over the limit: %v", err)
	}
	// 正好 1 亿像素：不拦，照旧去解码（这张数据不够，解码失败，但不是超限）
	if _, _, err := decodeDocImage(write("edge.png", pngClaiming(10000, 10000))); err == nil {
		t.Fatal("truncated image decoded")
	} else if _, ok := err.(imageTooLarge); ok {
		t.Fatalf("exactly the limit refused: %v", err)
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
	os.WriteFile(path, pngClaiming(20000, 15000), 0o644)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, path, 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "too large", func() bool { return v.cur.kind == docTooLarge })
	if v.cur.imgInfo.w != 20000 || v.cur.imgInfo.h != 15000 || v.cur.img != nil {
		t.Fatalf("doc %+v img %v", v.cur.imgInfo, v.cur.img)
	}
	tt.Frame()
	want := locales.In("zh-Hans").FilesImageTooLarge(20000, 15000)
	if !tt.HasText(want) {
		t.Fatalf("message %q not shown: %q", want, tt.Texts())
	}
	if tt.HasText(locales.In("zh-Hans").FilesTooLarge(files.MaxSize >> 20)) {
		t.Fatal("text-file message shown for an image")
	}
	v.poll.at = time.Now().Add(time.Hour)
}
