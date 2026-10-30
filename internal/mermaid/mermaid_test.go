package mermaid

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

// 中文标签按窗口的字体量宽度，节点要比同样字数的英文宽
func TestRenderChinese(t *testing.T) {
	d := Render("flowchart LR\nA[常驻进程] --> B[终端]", testStyle())
	if d.Err != "" {
		t.Fatal(d.Err)
	}
	var got []string
	for _, l := range d.labels {
		got = append(got, l.text)
	}
	if s := strings.Join(got, "|"); !strings.Contains(s, "常驻进程") || !strings.Contains(s, "终端") {
		t.Errorf("labels %q", s)
	}
}

func TestRender(t *testing.T) {
	st := testStyle()
	for name, src := range map[string]string{
		"flowchart": "flowchart LR\nA[Start] --> B{Ok?}\nB -- yes --> C[Done]\nB -- no --> A",
		"sequence":  "sequenceDiagram\nAlice->>Bob: Hello\nBob-->>Alice: Hi",
		"class":     "classDiagram\nclass Animal{\n+String name\n}\nAnimal <|-- Duck",
		"gantt":     "gantt\ntitle Plan\ndateFormat YYYY-MM-DD\nsection A\nTask :a1, 2024-01-01, 30d",
		"pie":       "pie title Pets\n\"Dogs\" : 386\n\"Cats\" : 85",
		"git":       "gitGraph\ncommit\nbranch dev\ncommit\ncheckout main\nmerge dev",
	} {
		d := Render(src, st)
		if d.Err != "" {
			t.Errorf("%s: %s", name, d.Err)
			continue
		}
		if d.svg == nil || d.w <= 0 || d.h <= 0 || len(d.labels) == 0 {
			t.Errorf("%s: svg %v %vx%v, %d labels", name, d.svg != nil, d.w, d.h, len(d.labels))
		}
		for _, l := range d.labels {
			if l.x < -d.w || l.x > 2*d.w || l.y < -d.h || l.y > 2*d.h {
				t.Errorf("%s: a label at %v,%v outside %vx%v", name, l.x, l.y, d.w, d.h)
			}
		}
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

func TestRenderError(t *testing.T) {
	d := Render("sequenceDiagram\nA->>B hi", testStyle())
	if d.Err == "" || strings.Contains(d.Err, "\n") || !strings.Contains(d.Err, "line 2") {
		t.Errorf("err = %q", d.Err)
	}
	if d := Render("not a diagram", testStyle()); d.Err == "" {
		t.Error("no error for text that is no diagram")
	}
}

func TestToSVG(t *testing.T) {
	st := testStyle()
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 50" width="100" height="50" style="--_bg:var(--pele-bg,#fff);--_s:var(--pele-surface,#f3f3f3);--_a:var(--pele-nope,#808080);--_r:var(--pele-radius,4px);font:16px sans-serif" fill="var(--_fg,#123456)">` +
		`<g transform="translate(10,20)"><rect x="0" y="0" width="10" height="10" fill="var(--_s)" style="rx:var(--_r)"/>` +
		`<rect fill="color-mix(in srgb,var(--_a) 50%,#000000)"/>` +
		`<text x="5" y="6" text-anchor="middle">A &amp; B</text>` +
		`<text text-anchor="middle" xml:space="preserve"><tspan x="0" y="-4" font-weight="var(--_w)">one</tspan><tspan font-family="var(--_fm,monospace)"> two</tspan><tspan x="0" y="16" font-size="12">three</tspan></text></g>` +
		`<g transform="rotate(90 0 0)"><text x="1" y="2">turned</text></g></svg>`
	out, texts, err := toSVG(svg, st)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`fill="` + hexColor(st.Surface) + `"`, ` rx="4"`, `fill="#404040"`, `fill="#123456"`} {
		if !strings.Contains(s, want) {
			t.Errorf("no %s in %s", want, s)
		}
	}
	for _, gone := range []string{"<text", "tspan", "var(", "--_"} {
		if strings.Contains(s, gone) {
			t.Errorf("%s left in %s", gone, s)
		}
	}
	if _, err := ui.ParseSVG(out); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 4 {
		t.Fatalf("%d chunks: %+v", len(texts), texts)
	}
	if r := texts[0].runs; len(r) != 1 || r[0].text != "A & B" || r[0].size != 16 || texts[0].anchor != "middle" {
		t.Errorf("first chunk %+v", texts[0])
	}
	if x, y := texts[0].m.apply(texts[0].x, texts[0].y); x != 15 || y != 26 {
		t.Errorf("first chunk at %v,%v", x, y)
	}
	if r := texts[1].runs; len(r) != 2 || r[0].style != peleBold || r[1].style != peleMono || r[1].text != " two" || texts[1].y != -4 {
		t.Errorf("second chunk %+v", texts[1])
	}
	if r := texts[2].runs; len(r) != 1 || r[0].size != 12 || texts[2].y != 16 {
		t.Errorf("third chunk %+v", texts[2])
	}
	if l := texts[3].shape(st); len(l) != 1 || l[0].angle != 90 {
		t.Errorf("turned %+v", l)
	}
}

func TestStyledFillLabels(t *testing.T) {
	st := testStyle()
	source := "flowchart LR\n subgraph S[\"sub\"]\n A[aa]\n end\n A --> B[bb] --> C[cc] --> D[dd]\n" +
		"style S fill:#ffe\n style B fill:#fdd,stroke:#c00\n style C fill:#fdd,color:#f00\n style D fill:#333\n"
	svg, err := peleRender(source, st)
	if err != nil {
		t.Fatal(err)
	}
	_, texts, err := toSVG(svg, st)
	if err != nil {
		t.Fatal(err)
	}
	colors := map[string]ui.Color{}
	for _, x := range texts {
		for _, r := range x.runs {
			colors[r.text] = r.color
		}
	}
	for text, want := range map[string]ui.Color{"sub": st.Bg, "aa": st.Fg, "bb": st.Bg, "cc": ui.Hex("#ff0000"), "dd": st.Fg} {
		if got := colors[text]; got != want {
			t.Errorf("%s in %+v, want %+v", text, got, want)
		}
	}
}

func TestCSSResolve(t *testing.T) {
	vars := map[string]string{"--a": "var(--b)", "--b": "#112233"}
	for in, want := range map[string]string{
		"var(--a)":                               "#112233",
		"var(--x,#fff)":                          "#fff",
		"var(--x, var(--a))":                     "#112233",
		"1px solid var(--b)":                     "1px solid #112233",
		"color-mix(in srgb,#ffffff 25%,#000000)": "#404040",
		"none":                                   "none",
	} {
		if got := cssResolve(in, vars); got != want {
			t.Errorf("cssResolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTransform(t *testing.T) {
	m := parseTransform("translate(10, 5) scale(2)")
	if x, y := m.apply(1, 1); x != 12 || y != 7 {
		t.Errorf("translate scale: %v,%v", x, y)
	}
	m = parseTransform("rotate(90 10 10)")
	if x, y := m.apply(20, 10); math.Abs(float64(x-10)) > 1e-4 || math.Abs(float64(y-20)) > 1e-4 {
		t.Errorf("rotate: %v,%v", x, y)
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
