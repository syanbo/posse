package mermaid

import (
	"math"
	"strings"
	"testing"
	"unicode"
)

func testStyle() Style {
	return Style{
		Bg: hex("#141413"), Fg: hex("#ECEAE4"), Muted: hex("#9A978F"), Line: hex("#9A978F"),
		Surface: hex("#222220"), SurfaceAlt: hex("#2C2C29"), Border: hex("#5A5853"), Accent: hex("#D97757"),
		Danger: hex("#EF4444"),
	}
}

// testMeasurer 按字数量：汉字一个字号宽，别的 0.6 个
var testMeasurer = Measurer{Key: "test", Width: func(text string, size float32, style int) float32 {
	w := float32(0)
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			w += size
		} else {
			w += size * 0.6
		}
	}
	return w
}}

func TestRender(t *testing.T) {
	st := testStyle()
	for name, src := range map[string]string{
		"flowchart": "flowchart LR\nA[Start] --> B{Ok?}\nB -- yes --> C[Done]\nB -- no --> A",
		"sequence":  "sequenceDiagram\nAlice->>Bob: Hello\nBob-->>Alice: Hi",
		"class":     "classDiagram\nclass Animal{\n+String name\n}\nAnimal <|-- Duck",
		"gantt":     "gantt\ntitle Plan\ndateFormat YYYY-MM-DD\nsection A\nTask :a1, 2024-01-01, 30d",
		"pie":       "pie title Pets\n\"Dogs\" : 386\n\"Cats\" : 85",
		"git":       "gitGraph\ncommit\nbranch dev\ncommit\ncheckout main\nmerge dev",
		"chinese":   "flowchart LR\nA[常驻进程] --> B[终端]",
	} {
		l, err := Render(src, st, testMeasurer)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(l.Shapes) == 0 || l.W <= 0 || l.H <= 0 || len(l.Labels) == 0 {
			t.Errorf("%s: %d bytes %vx%v, %d labels", name, len(l.Shapes), l.W, l.H, len(l.Labels))
		}
		for _, lb := range l.Labels {
			if lb.X < -l.W || lb.X > 2*l.W || lb.Y < -l.H || lb.Y > 2*l.H {
				t.Errorf("%s: a label at %v,%v outside %vx%v", name, lb.X, lb.Y, l.W, l.H)
			}
		}
		if name == "chinese" {
			var got []string
			for _, lb := range l.Labels {
				got = append(got, lb.Text)
			}
			if s := strings.Join(got, "|"); !strings.Contains(s, "常驻进程") || !strings.Contains(s, "终端") {
				t.Errorf("labels %q", s)
			}
		}
	}
}

func TestRenderError(t *testing.T) {
	_, err := Render("sequenceDiagram\nA->>B hi", testStyle(), testMeasurer)
	if err == nil || strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v", err)
	}
	if _, err := Render("not a diagram", testStyle(), testMeasurer); err == nil {
		t.Error("no error for text that is no diagram")
	}
}

// 标签按量出来的宽度对齐：居中的标签左边是基线起点减去一半宽度
func TestLabelAnchor(t *testing.T) {
	c := chunk{m: identity, x: 100, y: 50, anchor: "middle", runs: []run{{text: "ab", size: 10}, {text: "中", size: 10, style: Bold}}}
	l := c.shape(testMeasurer)
	if len(l) != 2 || l[0].X != 100-11 || l[1].X != 100-11+12 || l[0].Y != 50 || l[1].Style != Bold {
		t.Errorf("labels %+v", l)
	}
}

func TestSVGSize(t *testing.T) {
	if w, h := svgSize([]byte(`<svg viewBox="0 0 160 64" width="160" height="64"><g/></svg>`)); w != 160 || h != 64 {
		t.Errorf("size %vx%v", w, h)
	}
	if w, h := svgSize([]byte(`<svg viewBox="0 0 30 20"><rect width="5" height="5"/></svg>`)); w != 30 || h != 20 {
		t.Errorf("viewBox size %vx%v", w, h)
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
	if len(texts) != 4 {
		t.Fatalf("%d chunks: %+v", len(texts), texts)
	}
	if r := texts[0].runs; len(r) != 1 || r[0].text != "A & B" || r[0].size != 16 || texts[0].anchor != "middle" {
		t.Errorf("first chunk %+v", texts[0])
	}
	if x, y := texts[0].m.apply(texts[0].x, texts[0].y); x != 15 || y != 26 {
		t.Errorf("first chunk at %v,%v", x, y)
	}
	if r := texts[1].runs; len(r) != 2 || r[0].style != Bold || r[1].style != Mono || r[1].text != " two" || texts[1].y != -4 {
		t.Errorf("second chunk %+v", texts[1])
	}
	if r := texts[2].runs; len(r) != 1 || r[0].size != 12 || texts[2].y != 16 {
		t.Errorf("third chunk %+v", texts[2])
	}
	if l := texts[3].shape(testMeasurer); len(l) != 1 || l[0].Angle != 90 {
		t.Errorf("turned %+v", l)
	}
}

func TestStyledFillLabels(t *testing.T) {
	st := testStyle()
	source := "flowchart LR\n subgraph S[\"sub\"]\n A[aa]\n end\n A --> B[bb] --> C[cc] --> D[dd]\n" +
		"style S fill:#ffe\n style B fill:#fdd,stroke:#c00\n style C fill:#fdd,color:#f00\n style D fill:#333\n"
	svg, err := peleRender(source, testMeasurer)
	if err != nil {
		t.Fatal(err)
	}
	_, texts, err := toSVG(svg, st)
	if err != nil {
		t.Fatal(err)
	}
	colors := map[string]Color{}
	for _, x := range texts {
		for _, r := range x.runs {
			colors[r.text] = r.color
		}
	}
	for text, want := range map[string]Color{"sub": st.Bg, "aa": st.Fg, "bb": st.Bg, "cc": hex("#ff0000"), "dd": st.Fg} {
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
