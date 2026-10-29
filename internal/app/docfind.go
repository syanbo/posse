package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// docFind 是不用编辑器显示文件内容的视图（Markdown 预览、Annotate）的查找栏，样子和按键照编辑器自带的：
// ⌘F 打开，回车、⌘G 下一个，⇧回车、⇧⌘G 上一个，Esc 关闭；不分大小写。
// 命中由视图自己数：Markdown 预览在搭界面时按顺序数（total），Annotate 的行是按需搭的，预先算好。
type docFind struct {
	open   bool
	query  string
	cur    int  // 当前是第几个命中
	n      int  // 上一帧数到的命中数，跳转按它算
	total  int  // 这一帧正在数的命中数
	focus  bool // 下一帧把焦点给输入框
	scroll bool // 把当前命中滚进视野
}

// words 是交给 markRanges 的搜索词；栏关着或没输入时为空。
func (f *docFind) words() []string {
	if !f.open || f.query == "" {
		return nil
	}
	return []string{strings.ToLower(f.query)}
}

// shortcuts 在视图搭内容之前调用：⌘F 打开，栏开着时 ⌘G、⇧⌘G 跳。按键跟着编辑器的查找（快捷键表里的 editor.find 等）。
func (f *docFind) shortcuts(c *ui.Context, a *app) {
	if a.pressed(c, "editor.find") {
		f.open, f.focus, f.scroll = true, true, true
	}
	if !f.open {
		return
	}
	switch {
	case a.pressed(c, "editor.findNext"):
		f.step(1)
	case a.pressed(c, "editor.findPrevious"):
		f.step(-1)
	}
}

func (f *docFind) step(d int) {
	if f.n == 0 {
		return
	}
	f.cur = ((f.cur+d)%f.n + f.n) % f.n
	f.scroll = true
}

// begin、end 包住一帧的计数；内容改了命中变少时当前项退到最后一个。
func (f *docFind) begin() { f.total = 0 }

func (f *docFind) end() {
	f.n = f.total
	if f.cur >= f.n {
		f.cur = max(f.n-1, 0)
	}
	if f.n == 0 {
		f.scroll = false
	}
}

// reveal 在 el 里含有当前命中（编号从 from 到 total）且要滚动时把它滚进视野，返回 el。
func (f *docFind) reveal(el ui.Element, from int) ui.Element {
	if f.scroll && from <= f.cur && f.cur < f.total {
		el.ScrollIntoView()
		f.scroll = false
	}
	return el
}

// bar 画浮在视图右上角的查找栏，放在视图最外层的容器里、内容之后。
func (f *docFind) bar(c *ui.Context, a *app) {
	if !f.open {
		return
	}
	t := c.Theme()
	panel := floating(c, ui.Row(c.Key("doc-find")).Absolute().Top(8).Right(20).Padding(4).Gap(2).AlignItems(ui.Center)).
		Role(ui.RoleToolbar).Label("查找").Cursor(ui.CursorDefault)
	panel.Children(func() {
		in := fieldInputWidth(c, &f.query, "查找", 220)
		if f.focus {
			in.Focus()
			f.focus = false
		}
		switch {
		case in.Changed():
			f.cur, f.scroll = 0, true
		case in.Shortcut(ui.Shift, ui.KeyEnter):
			f.step(-1)
		case in.Submitted():
			f.step(1)
		case in.Shortcut(0, ui.KeyEscape):
			f.open = false
		}
		status := ""
		switch {
		case f.query == "":
		case f.n == 0:
			status = "无结果"
		default:
			status = fmt.Sprintf("%d/%d", f.cur+1, f.n)
		}
		ui.Text(c, status).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).MinWidth(sz(56)).TextAlign(ui.Center).Role(ui.RoleStatus)
		if iconButtonSized(c, iconArrowUp, a.withKey("上一个", "editor.findPrevious"), compactBox(), smallIcon()).Clicked() {
			f.step(-1)
		}
		if iconButtonSized(c, iconArrowDown, a.withKey("下一个", "editor.findNext"), compactBox(), smallIcon()).Clicked() {
			f.step(1)
		}
		if iconButtonSized(c, iconClose, "关闭 Esc", compactBox(), smallIcon()).Clicked() {
			f.open = false
		}
	})
}
