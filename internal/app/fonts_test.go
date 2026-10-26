package app

import (
	"slices"
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
