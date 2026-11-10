package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/locales"
)

// 终端里的常用快捷键，照 macOS 上 Ghostty、iTerm 和 Orca 的默认：
// 终端视图把带 Command 的键都交给 app，Option 也不当 Meta，所以这些键要在 OnKey 里自己处理。

// termEditKeys 是按行、按词移动和删除，发 readline 认的控制字符：zsh、bash 和 Claude、Codex 的输入框都认。
// 键在快捷键表里（keymap.go）。
var termEditKeys = []struct{ id, seq string }{
	{"termLineStart", "\x01"},    // 行首（Ctrl+A）
	{"termLineEnd", "\x05"},      // 行尾（Ctrl+E）
	{"termKillLine", "\x15"},     // 删到行首（Ctrl+U）
	{"termWordLeft", "\x1bb"},    // 上一个词（Meta+B）
	{"termWordRight", "\x1bf"},   // 下一个词（Meta+F）
	{"termKillWord", "\x1b\x7f"}, // 删前一个词（Meta+退格）
}

// termKey 是终端的 OnKey：报告这个键是不是 Posse 处理掉了。
func (a *app) termKey(v *termView, id string, mods ui.Modifiers, key ui.Key) bool {
	t := v.term
	if t == nil {
		return false
	}
	// ⌘A 不在这里：Edit 菜单的"全选"已经交给终端视图
	if a.keyRec != nil {
		return false
	}
	if a.termFindKey(v, mods, key) {
		return true
	}
	// 全屏程序里也能查：查的是它这一屏
	if a.keyIs("termFind", mods, key) && a.live(v) {
		a.openTermFind(v)
		return true
	}
	if a.splitKey(v, mods, key) {
		return true
	}
	if a.keyIs("termClear", mods, key) {
		a.clearTerminal(appL(), v, id)
		return true
	}
	// 全屏程序（vim、less）有自己的按键，下面这些原样交给它
	if t.AltScreen() {
		return false
	}
	switch {
	case a.keyIs("termTop", mods, key):
		t.ScrollToEdge(true)
		return true
	case a.keyIs("termBottom", mods, key):
		t.ScrollToEdge(false)
		return true
	case a.keyIs("termNewline", mods, key):
		// Claude、Codex 的输入框里 Ctrl+J 是换行不发送；Shift+Enter 默认编出来和回车一样，会直接发送
		if _, ti := a.findTerminal(id); ti != nil && (ti.Agent == "claude" || ti.Agent == "codex") {
			t.Send([]byte{'\n'})
			return true
		}
		return false
	}
	for _, e := range termEditKeys {
		if a.keyIs(e.id, mods, key) {
			t.Send([]byte(e.seq))
			return true
		}
	}
	return false
}

// termClearSeq 和常驻进程清屏时用的一样：清屏、清回滚区、光标回左上角。
const termClearSeq = "\x1b[H\x1b[2J\x1b[3J"

// clearTerminal 是 ⌘K：由常驻进程清屏并清空回滚区（之后重新接入也是空的），再给程序发 Ctrl+L 重画提示符。
func (a *app) clearTerminal(m locales.Messages, v *termView, id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "terminal.clear", map[string]any{"terminal": id, "redraw": true}, nil)
		var re *rpc.Error
		switch {
		case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
			// 常驻进程还是没有这个接口的旧版本：只清界面这边，切走再接入时回滚区会回来
			a.update(func() {
				if a.live(v) && v.term != nil {
					v.term.Feed([]byte(termClearSeq))
					v.term.Send([]byte{0x0c})
				}
			})
		case err != nil:
			a.update(func() { a.err = m.TermClearError(errtext.Text(err)) })
		}
	}()
}

// stepTab 是 ⌘⇧] / ⌘⇧[（和 ⌃Tab / ⌃⇧Tab）：按侧栏从上到下的顺序切到下一个 / 上一个标签，侧栏的高亮只往一个方向走。
// 一个工作区里先是不在侧栏里的终端（已退出的、等着恢复的 shell）和文件（侧栏里高亮工作区那一行），再是侧栏里列在它下面的会话（agent 和 shell）。
// 到头了切到下一个 / 上一个有标签的工作区（跨项目），停在它的第一个 / 最后一个标签；只有这一个工作区有标签时在它里面绕回来。
func (a *app) stepTab(w *workspace, dir int) {
	// 侧栏跟着滚到切过去的那一行，收着的项目、worker 展开
	a.revealSel = true
	tabs := a.tabOrder(w)
	// 按画面上显示的标签找：还没选过时显示的是标签栏的第一个终端，不一定是这个顺序的第一个
	cur := 0
	if a.filesActive(w) {
		v := a.files[w.ID]
		cur = max(slices.IndexFunc(tabs, func(t tabStop) bool { return t.doc != nil && t.doc == v.cur }), 0)
	} else if t := a.currentTerminal(w); t != nil {
		cur = max(slices.IndexFunc(tabs, func(s tabStop) bool { return s.term == t.ID }), 0)
	}
	if i := cur + dir; i >= 0 && i < len(tabs) {
		a.selectTab(w, tabs[i])
		return
	}
	if next := a.stepWorkspace(w, dir); next != nil {
		tabs := a.tabOrder(next)
		i := 0
		if dir < 0 {
			i = len(tabs) - 1
		}
		a.selected = next.ID
		a.selectTab(next, tabs[i])
		return
	}
	if len(tabs) > 1 {
		a.selectTab(w, tabs[(cur+dir+len(tabs))%len(tabs)])
	}
}

// tabStop 是标签栏里的一个标签：终端或打开的文件。
type tabStop struct {
	term string
	doc  *fileDoc
}

// tabOrder 是工作区的标签按侧栏的顺序：不在侧栏里的终端和文件按标签栏的先后，再是侧栏里的会话按侧栏的先后
// （worker 跟在派发它的会话后面，和 sessionRows 一样）。
func (a *app) tabOrder(w *workspace) []tabStop {
	var tabs []tabStop
	sessions := sidebarSessions(w.Terminals)
	for _, t := range w.Terminals {
		if !slices.ContainsFunc(sessions, func(s terminalInfo) bool { return s.ID == t.ID }) {
			tabs = append(tabs, tabStop{term: t.ID})
		}
	}
	if v := a.files[w.ID]; v != nil {
		for _, d := range v.docs {
			tabs = append(tabs, tabStop{doc: d})
		}
	}
	top, children := a.sessionTree(sessions)
	// 和 sessionRows 一样多留一层余量，防止数据成环时无限递归
	var walk func(list []sessionNode, depth int)
	walk = func(list []sessionNode, depth int) {
		for _, n := range list {
			tabs = append(tabs, tabStop{term: n.term.ID})
			if depth < 4 {
				walk(children[n.term.ID], depth+1)
			}
		}
	}
	walk(top, 0)
	return tabs
}

func (a *app) selectTab(w *workspace, t tabStop) {
	if t.doc != nil {
		a.showFiles(w).cur = t.doc
		return
	}
	a.activeTerm[w.ID] = t.term
}

// stepWorkspace 按侧栏的顺序（项目，再是项目里的工作区）找 w 之后 / 之前第一个有标签的工作区，到头了绕回来；
// 没有别的就返回 nil。
func (a *app) stepWorkspace(w *workspace, dir int) *workspace {
	var order []*workspace
	at := -1
	for _, p := range a.projects {
		ws := a.byProject[p.ID]
		for i := range ws {
			if ws[i].ID == w.ID {
				at = len(order)
			}
			order = append(order, &ws[i])
		}
	}
	if at < 0 {
		return nil
	}
	n := len(order)
	for k := 1; k < n; k++ {
		if x := order[((at+dir*k)%n+n)%n]; len(a.tabOrder(x)) > 0 {
			return x
		}
	}
	return nil
}
