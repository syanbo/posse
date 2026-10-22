package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"hive/internal/history"
)

// 一体输入区：拼字过程中、确认候选字都不发送，⌘↩ 发送，空的不发送。
// 拼字时按的回车在 macOS 上由输入法拿去确认候选字（见 mygo 的 Element.Composing），不会到这里。
func TestComposer(t *testing.T) {
	for _, multiline := range []bool{false, true} {
		var value string
		sent := 0
		tt := ui.NewTester(func(c *ui.Context) {
			if composer(c, &value, "说点什么", "发送", multiline) {
				sent++
				value = ""
			}
		}, 600, 200)
		tt.Frame()
		if err := tt.Click("说点什么"); err != nil {
			t.Fatal(err)
		}
		tt.Compose("ni", 2)
		tt.Frame()
		tt.Type("你")
		tt.Type("好")
		tt.Frame()
		if sent != 0 || value != "你好" {
			t.Fatalf("multiline=%v: sent=%d value=%q while typing", multiline, sent, value)
		}
		tt.Key(ui.Cmd, ui.KeyEnter)
		tt.Frame()
		if sent != 1 || value != "" {
			t.Fatalf("multiline=%v: ⌘↩ sent=%d value=%q", multiline, sent, value)
		}
		tt.Key(ui.Cmd, ui.KeyEnter)
		tt.Frame()
		if sent != 1 {
			t.Fatalf("multiline=%v: sent empty", multiline)
		}
	}
}

// 连续的工具调用折成一行，点开后逐条列出；命中搜索的工具消息所在的组自动展开
func TestTranscriptToolRuns(t *testing.T) {
	d := &historyDetail{Messages: []history.Message{
		{Seq: 0, Role: "user", Text: "q"},
		{Seq: 1, Role: "tool", Text: "ls"},
		{Seq: 2, Role: "tool", Text: "cat"},
		{Seq: 3, Role: "assistant", Text: "a"},
		{Seq: 4, Role: "tool", Text: "grep"},
	}}
	kinds := func(items []transcriptItem) string {
		var s string
		for _, it := range items {
			if it.tools != nil {
				s += fmt.Sprintf("[%d]", len(it.tools))
			} else {
				s += it.msg.Role[:1]
			}
		}
		return s
	}
	if got := kinds(d.visible(false, -1)); got != "u[2]a[1]" {
		t.Fatalf("collapsed %s", got)
	}
	if got := kinds(d.visible(false, 4)); got != "u[2]a[1]t" {
		t.Fatalf("hit opens its run: %s", got)
	}
	d.opened, d.openedV = map[int]bool{1: true}, 1
	if got := kinds(d.visible(false, -1)); got != "u[2]tta[1]" {
		t.Fatalf("opened run %s", got)
	}
	if got := kinds(d.visible(true, -1)); got != "uttat" {
		t.Fatalf("all tools %s", got)
	}
}

// 代码块的复制按钮把代码放进剪贴板
func TestCodeBlockCopy(t *testing.T) {
	blocks := parseMarkdown("看这段：\n\n```go\nfunc main() {}\n```\n")
	if len(blocks) != 2 || blocks[1].lang != "go" {
		t.Fatalf("blocks %+v", blocks)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() { mdView{}.blocks(c, blocks) })
	}, 600, 300)
	tt.Frame()
	if !tt.HasText("go") {
		t.Fatal("language bar missing")
	}
	if err := tt.Click("复制代码"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "func main() {}" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
}

// 渐隐只画在还有内容的那一边：在顶部时上边不画，滚到底后下边不画
func TestFadeEdges(t *testing.T) {
	var s ui.ListState
	bg := ui.Hex("#FFFFFF")
	red := ui.Hex("#FF0000")
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Background(bg).Children(func() {
			fadeEdges(ui.List(c, &s, 100, func(i int) {
				ui.Box(c).Height(30).Background(red)
			}).Grow(1), &s, bg)
		})
	}, 200, 300)
	tt.Frame()
	tt.Frame()
	px := func(y int) (r, g uint8) {
		img := tt.Image()
		c := img.RGBAAt(100*img.Rect.Dx()/200, y*img.Rect.Dy()/300)
		return c.R, c.G
	}
	if r, g := px(1); r < 240 || g > 20 {
		t.Fatalf("top faded at the top: r=%d g=%d", r, g)
	}
	if _, g := px(298); g < 200 {
		t.Fatalf("bottom not faded: g=%d", g)
	}
	s.ScrollToEnd()
	tt.Frame()
	tt.Frame()
	if _, g := px(1); g < 200 {
		t.Fatalf("top not faded at the end: g=%d", g)
	}
	if r, g := px(298); r < 240 || g > 20 {
		t.Fatalf("bottom faded at the end: r=%d g=%d", r, g)
	}
}

// 照 Orca：能点的地方是手形指针，对话框里的文字和空白处是箭头。
func TestPointerCursor(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.selected = "w1"
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	at := func(s string) ui.Cursor {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("%q missing", s)
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
		return tt.Cursor()
	}
	if c := at("zsh"); c != ui.CursorPointer {
		t.Errorf("tab cursor = %v", c)
	}
	if c := at("任务"); c != ui.CursorPointer {
		t.Errorf("sidebar row cursor = %v", c)
	}
	// 拖项目排序时是抓握的手
	r, _ := tt.Find("Chat")
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Move(r.X+r.W/2, r.Y+r.H/2+30)
	tt.Frame()
	if c := tt.Cursor(); c != ui.CursorGrabbing {
		t.Errorf("dragging cursor = %v", c)
	}
	tt.Release(r.X+r.W/2, r.Y+r.H/2+30)
	tt.Frame()
	a.confirm("关闭 终端？", "里面正在运行的程序会被结束。", "关闭", nil)
	tt.Frame()
	if c := at("里面正在运行的程序会被结束。"); c != ui.CursorDefault {
		t.Errorf("dialog text cursor = %v", c)
	}
	if c := at("关闭"); c != ui.CursorPointer {
		t.Errorf("dialog button cursor = %v", c)
	}
	tt.Move(5, 700)
	tt.Frame()
	if c := tt.Cursor(); c != ui.CursorDefault {
		t.Errorf("backdrop cursor = %v", c)
	}
}

// 加进来的 worktree 记成 folder，但目录不是项目目录：侧栏照样画分支图标。
func TestIsWorktree(t *testing.T) {
	a := &app{byProject: map[string][]workspace{"p": {
		{ID: "m", ProjectID: "p", Kind: "main", Path: "/r"},
		{ID: "f", ProjectID: "p", Kind: "folder", Path: "/r"},
		{ID: "w", ProjectID: "p", Kind: "worktree", Path: "/h/wt"},
		{ID: "x", ProjectID: "p", Kind: "folder", Path: "/elsewhere/wt"},
	}}}
	want := map[string]bool{"m": false, "f": false, "w": true, "x": true}
	for _, w := range a.byProject["p"] {
		if got := a.isWorktree(&w); got != want[w.ID] {
			t.Errorf("isWorktree(%s) = %v", w.ID, got)
		}
	}
}
