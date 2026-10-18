package app

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"hive/internal/client"
)

func TestDropPieces(t *testing.T) {
	for _, tc := range []struct {
		paths []string
		want  []dropPiece
	}{
		{[]string{"/a/main.go"}, []dropPiece{{"/a/main.go ", false}}},
		{[]string{"/a/my notes.md"}, []dropPiece{{"'/a/my notes.md' ", false}}},
		{[]string{"/a/it's.txt"}, []dropPiece{{`'/a/it'\''s.txt' `, false}}},
		{[]string{"/a/x.png"}, []dropPiece{{"/a/x.png", true}}},
		// 两张原样的图片之间不加空格
		{[]string{"/a/x.png", "/a/y.jpg"}, []dropPiece{{"/a/x.png", true}, {"/a/y.jpg", true}}},
		// 转义过的图片后面要有空格，shell 里才是两个参数
		{[]string{"/a/x (1).png", "/a/y.png"}, []dropPiece{{"'/a/x (1).png'", true}, {" ", false}, {"/a/y.png", true}}},
		{[]string{"/a/x.png", "/a/b.go"}, []dropPiece{{"/a/x.png", true}, {" ", false}, {"/a/b.go ", false}}},
	} {
		if got := dropPieces(tc.paths); !slices.Equal(got, tc.want) {
			t.Errorf("dropPieces(%q) = %+v, want %+v", tc.paths, got, tc.want)
		}
	}
}

// 终端标签：中键关闭（运行中的先问），右键菜单关其他、右侧的终端。
func TestTerminalTabMouse(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.selected = "w1"
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	r, ok := tt.Find("zsh")
	if !ok {
		t.Fatal("zsh tab missing")
	}
	tt.MiddleClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if !a.confirming || a.confirmTitle != "关闭 终端？" {
		t.Fatalf("middle click: confirming %v %q", a.confirming, a.confirmTitle)
	}
	a.confirming = false
	tt.Frame()

	if err := tt.RightClick("zsh"); err != nil {
		t.Fatal(err)
	}
	if m := tt.Menu(); !slices.Contains(m, "关闭其他终端") || !slices.Contains(m, "关闭右侧终端") {
		t.Fatalf("menu = %q", m)
	}
	if err := tt.ChooseMenuItem("关闭右侧终端"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.confirming || a.confirmTitle != "关闭 1 个终端？" {
		t.Fatalf("close right: confirming %v %q", a.confirming, a.confirmTitle)
	}
}

// 文件标签：中键关闭，右键菜单关闭全部。
func TestDocTabMouse(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "README.md"), 0, [2]int{})
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	a.openFile(w, filepath.Join(root, "docs/plan.md"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "opened", func() bool { return v.cur != nil && v.cur.kind == docText })
	r, ok := tt.Find("README.md")
	if !ok {
		t.Fatal("README.md tab missing")
	}
	tt.MiddleClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if len(v.docs) != 2 || v.doc(filepath.Join(root, "README.md")) != nil {
		t.Fatalf("middle click left %d docs", len(v.docs))
	}
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if m := tt.Menu(); !slices.Contains(m, "复制相对路径") {
		t.Fatalf("menu = %q", m)
	}
	if err := tt.ChooseMenuItem("关闭所有文件"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(v.docs) != 0 || a.activeTerm[w.ID] != "t1" {
		t.Fatalf("close all: %d docs, active %q", len(v.docs), a.activeTerm[w.ID])
	}
}
