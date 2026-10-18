package app

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// termMenu 是终端内容区的右键菜单（照 Orca 的终端菜单），换掉终端插件自带的英文菜单：
// 清屏和 ⌘K 一样由常驻进程清，关闭和 ⌘W 一样先问。
func (a *app) termMenu(m *ui.Menu, v *termView, id string) {
	if v.term == nil {
		return
	}
	sel := v.term.Selection()
	if m.Item("复制").Shortcut(ui.Cmd, ui.KeyC).Disabled(sel == "").Chosen() {
		writeClipboard(sel)
	}
	if m.Item("粘贴").Shortcut(ui.Cmd, ui.KeyV).Chosen() {
		v.term.PasteClipboard()
	}
	if m.Item("全选").Shortcut(ui.Cmd, ui.KeyA).Chosen() {
		v.term.SelectAll()
	}
	m.Separator()
	if m.Item("清屏").Shortcut(ui.Cmd, ui.KeyK).Chosen() {
		a.clearTerminal(v, id)
	}
	_, t := a.findTerminal(id)
	if t != nil && t.AgentState != nil && t.AgentState.SessionID != "" {
		m.Separator()
		if m.Item("复制会话 ID").Chosen() {
			writeClipboard(t.AgentState.SessionID)
		}
	}
	if t != nil {
		m.Separator()
		if m.Item("关闭终端").Shortcut(ui.Cmd, ui.KeyW).Chosen() {
			a.askClose(*t)
		}
	}
}

// writeClipboard 写剪贴板：菜单的回调里拿不到 ui.Context。
func writeClipboard(s string) { mygo.Clipboard.Write(transfer.TextData(s)) }

// pathDrag 是从文件栏拖出来的文件或目录，放到终端上贴入路径。
type pathDrag struct{ path string }

// dragPath 让文件栏里的一行可以拖到终端上贴入路径。文件栏滑进来的途中不能拖：
// 行在不动的指针下面移动，mygo 会把这一下点击当成拖动。
func (a *app) dragPath(row ui.Element, path string) {
	if a.filesPanelShown >= 1 {
		row.Drag(pathDrag{path})
	}
}

// termDrop 让终端区接住从访达和文件栏拖来的文件，把路径贴进终端；拖到上面时画一圈强调色的框。
func (a *app) termDrop(c *ui.Context, area ui.Element) {
	_, inner := ui.DragOver[pathDrag](area)
	if area.FileDragOver() || inner {
		accent := c.Theme().Accent
		area.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Stroke(ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}, accent, 8, 2)
		})
	}
	paths := area.DroppedFiles()
	if d, ok := ui.Drop[pathDrag](area); ok {
		paths = append(paths, d.path)
	}
	if len(paths) == 0 || a.view == nil || a.view.term == nil {
		return
	}
	term := a.view.term
	for _, p := range dropPieces(paths) {
		if p.paste {
			term.Paste(p.text)
		} else {
			term.Send([]byte(p.text))
		}
	}
	a.focusTerm = true
}

// dropPiece 是拖进终端的一段输入：paste 为真时按粘贴发（程序开了括号粘贴就带上标记），否则当作键入。
type dropPiece struct {
	text  string
	paste bool
}

// dropPieces 照 Orca 把拖进来的路径变成输入：图片按粘贴发，Claude、Codex 认得出粘贴的图片路径、当成附件，
// 路径里没有特殊字符时原样发（转义了它们就找不到文件），有空格等字符时转义，shell 里仍是一个参数；
// 其他文件转义后当作键入，后面带一个空格。两张原样的图片之间不加空格，否则空格会留在 agent 的输入框里。
func dropPieces(paths []string) []dropPiece {
	var out []dropPiece
	for i, p := range paths {
		if !isImagePath(p) {
			out = append(out, dropPiece{text: shellQuote(p) + " "})
			continue
		}
		out = append(out, dropPiece{text: shellQuote(p), paste: true})
		if i+1 < len(paths) && !(shellSafe(p) && isImagePath(paths[i+1]) && shellSafe(paths[i+1])) {
			out = append(out, dropPiece{text: " "})
		}
	}
	return out
}

var shellSafeRe = regexp.MustCompile(`^[a-zA-Z0-9_./@:%+=,-]+$`)

func shellSafe(p string) bool { return shellSafeRe.MatchString(p) }

// shellQuote 给路径加单引号，没有特殊字符时原样返回。
func shellQuote(p string) string {
	if shellSafe(p) {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
