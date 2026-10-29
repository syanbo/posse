package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/projicon"
)

// previewIconFile 生成一张测试用的图标（位图是一个强调色圆，SVG 是一颗星），导入 home 后返回文件名。
func previewIconFile(t *testing.T, home, projectID, ext string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "icon"+ext)
	if ext == ".svg" {
		svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#3b82f6" d="M12 2l3 7h7l-5.5 4.5L18.5 21 12 16.5 5.5 21l2-7.5L2 9h7z"/></svg>`
		if err := os.WriteFile(src, []byte(svg), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
		for y := range 200 {
			for x := range 300 {
				if dx, dy := x-150, y-100; dx*dx+dy*dy < 90*90 {
					img.Set(x, y, color.NRGBA{0xd9, 0x77, 0x57, 0xff})
				}
			}
		}
		f, err := os.Create(src)
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, img)
		f.Close()
	}
	name, err := importProjectIcon(home, projectID, src)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestImportProjectIcon(t *testing.T) {
	home := t.TempDir()
	name := previewIconFile(t, home, "p1", ".png")
	f, err := os.Open(filepath.Join(projectIconDir(home), name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	// 300×200 缩到长边 projectIconPx，保持比例
	if cfg.Width != projectIconPx || cfg.Height != projectIconPx*2/3 {
		t.Fatalf("缩放后 %d×%d", cfg.Width, cfg.Height)
	}
	// SVG 画成透明底的 PNG：角上透明，星的中间是原来的颜色
	svg := previewIconFile(t, home, "p1", ".svg")
	data, err := os.ReadFile(filepath.Join(projectIconDir(home), svg))
	if err != nil || filepath.Ext(svg) != ".png" {
		t.Fatalf("SVG 存成了 %s：%v", svg, err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatalf("角上不透明：%v", img.At(0, 0))
	}
	// #3b82f6
	if p := color.NRGBAModel.Convert(img.At(projectIconPx/2, projectIconPx/2)).(color.NRGBA); p.A < 250 || p.B < 230 || p.R > 70 || p.G < 120 || p.G > 140 {
		t.Fatalf("中间的颜色 %v", p)
	}
	bad := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(bad, []byte("not an image"), 0o644)
	if _, err := importProjectIcon(home, "p1", bad); err == nil {
		t.Fatal("读不了的图片应当报错")
	}
}

func TestSetProjectIconRemovesOldImage(t *testing.T) {
	a := &app{home: t.TempDir()}
	old := previewIconFile(t, a.home, "p1", ".png")
	a.setProjectIcon("p1", projectIcon{Image: old})
	a.setProjectIcon("p1", projectIcon{Text: "🚀"})
	if _, err := os.Stat(filepath.Join(projectIconDir(a.home), old)); !os.IsNotExist(err) {
		t.Fatalf("换掉的图片还在：%v", err)
	}
	a.setProjectIcon("p1", projectIcon{})
	if _, ok := a.projIcons["p1"]; ok {
		t.Fatal("恢复默认后还记着图标")
	}
}

// 对话框里写了字又删空，回到原来的图片，不会把图片当成恢复默认
func TestIconEditorKeepsImage(t *testing.T) {
	e := iconEditor{image: "p1-1.png"}
	e.text = "a"
	if e.draft() != (projectIcon{Text: "a"}) {
		t.Fatalf("draft %+v", e.draft())
	}
	e.text = "  "
	if e.draft() != (projectIcon{Image: "p1-1.png"}) {
		t.Fatalf("draft %+v", e.draft())
	}
}

// 项目不在了（CLI 移除也算），轮询时删掉它的图标和图片，图标文件也跟着改
func TestPruneProjectIcons(t *testing.T) {
	a := &app{home: t.TempDir()}
	img := previewIconFile(t, a.home, "p2", ".png")
	a.setProjectIcon("p1", projectIcon{Text: "🚀"})
	a.setProjectIcon("p2", projectIcon{Image: img})
	a.pruneProjectIcons([]project{{ID: "p1"}})
	if _, err := os.Stat(filepath.Join(projectIconDir(a.home), img)); !os.IsNotExist(err) {
		t.Fatalf("图片还在：%v", err)
	}
	if got := projicon.Load(a.home); len(got) != 1 || got["p1"].Text != "🚀" {
		t.Fatalf("icons.json %v", got)
	}
}
