package diagram

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func testStyle() Style {
	return Style{
		Bg: ui.Hex("#141413"), Fg: ui.Hex("#ECEAE4"), Muted: ui.Hex("#9A978F"), Line: ui.Hex("#9A978F"),
		Surface: ui.Hex("#222220"), SurfaceAlt: ui.Hex("#2C2C29"), Border: ui.Hex("#5A5853"), Accent: ui.Hex("#D97757"),
		Danger: ui.Hex("#EF4444"), Mono: "monospace", Weight: 400, BoldWeight: 600, MonoWeight: 400,
	}
}

func TestRenderTiles(t *testing.T) {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for i := range 30 {
		fmt.Fprintf(&b, "N%d[Step %d<br/>more] --> N%d\n", i, i, i+1)
	}
	d := Render(b.String(), testStyle())
	if d.Err != "" {
		t.Fatal(d.Err)
	}
	if d.h <= tileSide || len(d.tiles) < 2 {
		t.Fatalf("%vx%v in %d tiles", d.w, d.h, len(d.tiles))
	}
	area := float32(0)
	for _, tile := range d.tiles {
		w, h := tile.svg.Size()
		if tile.w > tileSide || tile.h > tileSide || w != tile.w || h != tile.h {
			t.Errorf("tile at %v,%v is %vx%v, its SVG %vx%v", tile.x, tile.y, tile.w, tile.h, w, h)
		}
		if tile.x+tile.w > d.w+0.01 || tile.y+tile.h > d.h+0.01 {
			t.Errorf("tile at %v,%v %vx%v past %vx%v", tile.x, tile.y, tile.w, tile.h, d.w, d.h)
		}
		area += tile.w * tile.h
	}
	if math.Abs(float64(area-d.w*d.h)) > 1 {
		t.Errorf("tiles cover %v of %v", area, d.w*d.h)
	}
	if small := Render("flowchart LR\nA --> B", testStyle()); len(small.tiles) != 1 || small.tiles[0].svg != small.svg {
		t.Errorf("a small diagram in %d tiles", len(small.tiles))
	}
}

// 字体超过上限被整批释放后，按同一缩放再画要重排标签：画已释放字体的排版会崩
func TestDrawAfterFontsReleased(t *testing.T) {
	d := Render("flowchart LR\nA[常驻进程] --> B[终端]", testStyle())
	if d.Err != "" {
		t.Fatal(d.Err)
	}
	w, h := d.Size()
	tt := ui.NewTester(func(c *ui.Context) { ui.Box(c).Width(w).Height(h).Draw(d.Draw) }, 800, 600)
	tt.Frame()
	gen := ui.TextGeneration()
	for i := range 600 {
		ui.ShapeText("x", ui.Font{Size: 8 + float32(i)*0.1}, 0)
	}
	tt.Frame()
	tt.Frame()
	if ui.TextGeneration() == gen {
		t.Fatal("字体没有被释放，测不到")
	}
}
