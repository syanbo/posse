package app

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"hive/internal/client"
)

func TestSortTerminals(t *testing.T) {
	ts := []terminalInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	// 不在顺序里的（新开的）按原来的创建顺序排在后面
	sortTerminals(ts, []string{"c", "a"})
	if got := termIDs(ts); !slices.Equal(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("sorted = %v", got)
	}
	if got := moveTerminal(ts, "d", "c"); !slices.Equal(got, []string{"d", "c", "a", "b"}) {
		t.Fatalf("moved = %v", got)
	}
	if got := moveTerminal(ts, "c", "b"); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("moved down = %v", got)
	}
}

func termIDs(ts []terminalInfo) []string {
	var ids []string
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	return ids
}

// drag 按住 from 拖到 to 再松开，中间分几步移动，和真实的拖动一样先越过起拖的距离。
func drag(tt *ui.Tester, fx, fy, tx, ty float32) {
	tt.Press(fx, fy)
	tt.Frame()
	for i := 1; i <= 5; i++ {
		f := float32(i) / 5
		tt.Move(fx+(tx-fx)*f, fy+(ty-fy)*f)
		tt.Frame()
	}
	tt.Release(tx, ty)
	tt.Frame()
}

// 标签栏和侧栏的会话都能拖动排序，两处共用一个顺序，存进 ui.json；拖动不算点击。
func TestReorderTerminals(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.selected = "w1"
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	w := a.workspace("w1")
	if got := termIDs(w.Terminals); !slices.Equal(got, []string{"t1", "t2", "t3", "t6"}) {
		t.Fatalf("fixture = %v", got)
	}

	// 把 zsh 标签拖到最前面（第一个标签上）
	zsh, ok := tt.Find("zsh")
	if !ok {
		t.Fatal("zsh tab missing")
	}
	drag(tt, zsh.X+zsh.W/2, zsh.Y+zsh.H/2, 300, zsh.Y+zsh.H/2)
	want := []string{"t3", "t1", "t2", "t6"}
	if got := termIDs(a.workspace("w1").Terminals); !slices.Equal(got, want) {
		t.Fatalf("after dragging the tab: %v, want %v", got, want)
	}
	if !slices.Equal(a.state.TermOrder["w1"], want) {
		t.Fatalf("saved order = %v", a.state.TermOrder["w1"])
	}
	if a.activeTerm["w1"] != "t1" {
		t.Fatalf("a drag is no click: active = %s", a.activeTerm["w1"])
	}

	// 侧栏里把 Codex 会话拖到 Claude 会话上面
	codex, ok := tt.Find("Codex")
	if !ok {
		t.Fatal("codex session missing")
	}
	claude, ok := tt.Find("重写 hive 界面")
	if !ok {
		t.Fatal("claude session missing")
	}
	drag(tt, codex.X+codex.W/2, codex.Y+codex.H/2, claude.X+claude.W/2, claude.Y+4)
	want = []string{"t3", "t2", "t1", "t6"}
	if got := termIDs(a.workspace("w1").Terminals); !slices.Equal(got, want) {
		t.Fatalf("after dragging the session: %v, want %v", got, want)
	}

	// 下一次轮询带回常驻进程的创建顺序，照样按存下的顺序排
	ws := slices.Clone(a.workspaces)
	polled := []terminalInfo{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}, {ID: "t6"}, {ID: "t7"}}
	ws[0].Terminals = polled
	a.apply(slices.Clone(a.projects), ws)
	if got := termIDs(a.workspace("w1").Terminals); !slices.Equal(got, []string{"t3", "t2", "t1", "t6", "t7"}) {
		t.Fatalf("after a poll: %v", got)
	}
	if got := termIDs(polled); !slices.Equal(got, []string{"t1", "t2", "t3", "t6", "t7"}) {
		t.Fatalf("apply must not sort the poll result kept for comparing: %v", got)
	}
}

// 文件标签之间也能拖动换位置，顺序随打开的文件存下来。
func TestReorderDocTabs(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "README.md"), 0, [2]int{})
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "opened", func() bool { return v.cur != nil && v.cur.kind == docText && tt.HasText("README.md") })
	from, ok := tt.Find("main.go")
	if !ok {
		t.Fatal("main.go tab missing")
	}
	to, ok := tt.Find("README.md")
	if !ok {
		t.Fatal("README.md tab missing")
	}
	drag(tt, from.X+from.W/2, from.Y+from.H/2, to.X+4, to.Y+to.H/2)
	if len(v.docs) != 2 || filepath.Base(v.docs[0].path) != "main.go" {
		t.Fatalf("docs = %v", []string{v.docs[0].path, v.docs[1].path})
	}
	if open := a.state.Files[w.ID].Open; len(open) != 2 || filepath.Base(open[0]) != "main.go" {
		t.Fatalf("saved = %v", open)
	}
}
