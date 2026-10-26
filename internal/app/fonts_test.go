package app

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEditorSizeOf(t *testing.T) {
	for _, c := range []struct {
		s    uiState
		want float32
	}{
		{uiState{}, defaultEditorFontSize},
		{uiState{FontSize: 16}, 15},
		{uiState{FontSize: 9}, minFontSize},
		{uiState{FontSize: 16, EditorFontSize: 12}, 12},
	} {
		if got := editorSizeOf(c.s); got != c.want {
			t.Errorf("editorSizeOf(%+v) = %g, want %g", c.s, got, c.want)
		}
	}
}

func TestFontStack(t *testing.T) {
	if got := fontStack(""); got != termFontFamily {
		t.Errorf("fontStack(\"\") = %q", got)
	}
	if got := fontStack("Menlo"); got != "Menlo, "+termFontFamily {
		t.Errorf("fontStack(Menlo) = %q", got)
	}
}

func TestFontRows(t *testing.T) {
	all := []ui.FontFamily{{Name: "Helvetica"}, {Name: "Menlo", Monospaced: true}, {Name: "Monaco", Monospaced: true}, {Name: "PingFang SC"}}
	labels := map[string]string{"PingFang SC": "苹方-简"}
	rows := fontRows(all, labels, "")
	var got []string
	for _, r := range rows {
		got = append(got, r.header+r.label)
	}
	want := []string{"默认（JetBrains Mono）", "等宽字体", "Menlo", "Monaco", "其他字体", "Helvetica", "苹方-简"}
	if !slices.Equal(got, want) {
		t.Fatalf("fontRows = %q", got)
	}
	// 中文名和原名都能搜，搜索时不列默认
	for _, q := range []string{"苹方", "pingfang"} {
		rows = fontRows(all, labels, q)
		if len(rows) != 2 || rows[1].family != "PingFang SC" {
			t.Fatalf("fontRows(%s) = %v", q, rows)
		}
	}
	if got := nextFont(rows, 0, 1); got != 1 {
		t.Errorf("nextFont = %d, want 1", got)
	}
	rows = fontRows(all, labels, "")
	if got := nextFont(rows, 4, -1); got != 3 {
		t.Errorf("nextFont up over header = %d, want 3", got)
	}
}

func TestLocalizedFamily(t *testing.T) {
	if got := localizedFamily("Menlo"); got != "Menlo" {
		t.Errorf("localizedFamily(Menlo) = %q", got)
	}
}

// 在列表里搜索后按回车选中第一个匹配的字体，按钮和终端用上它。
func TestFontPickerSearchEnter(t *testing.T) {
	a := previewApp(t)
	a.home = ""
	systemFonts.once.Do(func() {})
	systemFonts.mu.Lock()
	systemFonts.list, systemFonts.labels, systemFonts.done = []ui.FontFamily{{Name: "Helvetica"}, {Name: "Menlo", Monospaced: true}, {Name: "Monaco", Monospaced: true}}, nil, true
	systemFonts.mu.Unlock()
	a.selected, a.settingsTab = settingsBoardID, "appearance"
	tt := newPreviewTester(a, false)
	if err := tt.Click("终端字体"); err != nil {
		t.Fatal(err)
	}
	tt.Type("menlo")
	tt.Key(0, ui.KeyEnter)
	if a.termFamily != "Menlo" || a.termPick.open {
		t.Fatalf("termFamily = %q, open = %v", a.termFamily, a.termPick.open)
	}
}

func TestWeightsOf(t *testing.T) {
	for _, tc := range []struct {
		s          uiState
		weight, bd int
	}{
		{uiState{}, 400, 600},
		{uiState{TermWeight: 500}, 500, 700},
		{uiState{TermWeight: 700}, 700, 900},
		{uiState{TermWeight: 300}, 300, 600},
		{uiState{TermWeight: 500, TermBoldWeight: 600}, 500, 600},
	} {
		if w, b := weightsOf(tc.s.TermWeight, tc.s.TermBoldWeight); w != tc.weight || b != tc.bd {
			t.Errorf("weightsOf(%+v) = %d, %d; want %d, %d", tc.s, w, b, tc.weight, tc.bd)
		}
	}
}

func TestStepLineHeight(t *testing.T) {
	for _, tc := range []struct {
		h    float32
		d    int
		want float32
	}{
		{1, 1, 1.1}, {1.1, 1, 1.2}, {1, -1, 1}, {1.5, -1, 1.4}, {2, 1, 2}, {1.95, 1, 2},
	} {
		if got := stepLineHeight(tc.h, tc.d, 2); got != tc.want {
			t.Errorf("stepLineHeight(%g, %d) = %g, want %g", tc.h, tc.d, got, tc.want)
		}
	}
}

// 界面文字的字号、字重、图标的尺寸和圆角只用 style.go 里的那几档，写数字的会漏掉设置或对不上档。
// 视频上的播放键是单独的大图标，不算。
func TestNoLiteralType(t *testing.T) {
	text := regexp.MustCompile(`FontSize\([0-9]|FontWeight\([0-9]|\b(Size|Weight)(: | = )[0-9]|\.Radius\([0-9]`)
	icon := regexp.MustCompile(`ui\.Icon\(.*FontSize\((sz\()?[0-9]|(iconButtonSized|quietIconButton)\(.*, [0-9]+, [0-9]+\)|` +
		`(agentTile|agentIcon|statusGlyph|statusIcon|taskGlyphIcon|posseLoader)\(c, .*[ (][0-9]+[,)]`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if text.MatchString(line) && !strings.Contains(line, "ui.Icon(") || tupleType(line) ||
				icon.MatchString(line) && !strings.Contains(line, "iconPlay") {
				t.Errorf("%s:%d 写了数字的字号、字重、图标尺寸或圆角：%s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestSetUIType(t *testing.T) {
	defer setUIType(0, 0, 0)
	setUIType(0, 0, 0)
	if fsSmall != 12 || fsBody != 14 || fsTitle != 16 || fsPage != 18 || fwBody != 400 || fwMedium != 500 || fwBold != 600 {
		t.Fatalf("默认 %v %v %v %v %d %d %d", fsSmall, fsBody, fsTitle, fsPage, fwBody, fwMedium, fwBold)
	}
	setUIType(30, 300, 700)
	if fsBody != maxUIFontSize || fwMedium != 500 {
		t.Fatalf("上限或中间字重不对：%v %d", fsBody, fwMedium)
	}
}

// 间距只用 spaceScale 里的几档；对齐算出来的值写成常量或表达式（treeIndent、groupTextIndent），不写数字。
func TestSpacingScale(t *testing.T) {
	call := regexp.MustCompile(`\.(Gap|Padding|Margin)\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	num := regexp.MustCompile(`(?:^|[^\w.*])(\d+(?:\.\d+)?)\b`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range call.FindAllStringSubmatch(line, -1) {
				for _, n := range num.FindAllStringSubmatch(m[2], -1) {
					v, _ := strconv.ParseFloat(n[1], 32)
					if !slices.Contains(spaceScale, float32(v)) {
						t.Errorf("%s:%d 间距 %s 不在阶梯上：%s", f, i+1, n[1], m[0])
					}
				}
			}
		}
	}
}

// tupleType 认出 a.Font, a.Size = x, 12 这种一次给几个字段赋值、把数字赋给字号或字重的写法（常量声明不算）。
func tupleType(line string) bool {
	lhs, rhs, ok := strings.Cut(line, " = ")
	if !ok || !strings.Contains(lhs, ",") {
		return false
	}
	names, values := strings.Split(lhs, ","), strings.Split(rhs, ",")
	for i, n := range names {
		n = strings.TrimSpace(n)
		if i < len(values) && strings.Contains(n, ".") && (strings.HasSuffix(n, "Size") || strings.HasSuffix(n, "Weight")) {
			if v := strings.TrimSpace(values[i]); v != "" && v[0] >= '0' && v[0] <= '9' {
				return true
			}
		}
	}
	return false
}

// 颜色只在 style.go 里定义（主题、调色板、agent 品牌色），别处用名字；透明度变体只给动画调暗传进来的颜色用。
func TestColorsInPalette(t *testing.T) {
	literal := regexp.MustCompile(`ui\.(Hex|RGBA|RGB)\(`)
	alpha := regexp.MustCompile(`\.Alpha\(`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "style.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if literal.MatchString(line) || alpha.MatchString(line) && !strings.Contains(line, "color.Alpha(") {
				t.Errorf("%s:%d 颜色要用 style.go 里的名字：%s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// 投影、描边只经 style.go 的 elevCard、elevFloat、elevSheet，分隔线用 hline、vline，线宽是 hairline，
// 线的颜色是 line（主题的 Border 比它重，只给 mygo 自己的控件）。
func TestElevationAndLines(t *testing.T) {
	bad := regexp.MustCompile(`\b(Shadow|InsetShadow)\(|\b(Height|Width)\(1\)|\bBorder(Width)?\((1|[^)]*, 1)[,)]|\bt\.Border\b`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "style.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bad.MatchString(line) {
				t.Errorf("%s:%d 投影、描边和分隔线要用 style.go 里的函数：%s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
