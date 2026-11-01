package app

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
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
		a.clearTerminal(v, id)
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
func (a *app) clearTerminal(v *termView, id string) {
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
			a.update(func() { a.err = "清屏失败：" + errtext.Text(err) })
		}
	}()
}

// stepTab 是 ⌘⇧] / ⌘⇧[（和 ⌃Tab / ⌃⇧Tab）：按标签栏的顺序切到下一个 / 上一个标签，到头了绕回来。
// 标签栏里先是终端，再是打开的文件。
func (a *app) stepTab(w *workspace, dir int) {
	type tab struct {
		term string
		doc  *fileDoc
	}
	var tabs []tab
	for _, t := range w.Terminals {
		tabs = append(tabs, tab{term: t.ID})
	}
	var v *filesView
	if v = a.files[w.ID]; v != nil {
		for _, d := range v.docs {
			tabs = append(tabs, tab{doc: d})
		}
	}
	if len(tabs) < 2 {
		return
	}
	cur := 0
	for i, t := range tabs {
		if a.filesActive(w) && t.doc != nil && t.doc == v.cur || !a.filesActive(w) && t.term == a.activeTerm[w.ID] {
			cur = i
		}
	}
	next := tabs[(cur+dir+len(tabs))%len(tabs)]
	if next.doc != nil {
		a.showFiles(w).cur = next.doc
		return
	}
	a.activeTerm[w.ID] = next.term
}
