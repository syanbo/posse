package app

import (
	"bytes"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"posse/locales"
)

// termMenu 是终端内容区的右键菜单（照 Orca 的终端菜单），换掉终端插件自带的英文菜单：
// 清屏和 ⌘K 一样由常驻进程清，关闭和 ⌘W 一样先问；分栏和快捷键一样从右键的这一格分出去。
func (a *app) termMenu(lm locales.Messages, m *popMenu, v *termView, id string) {
	if v.term == nil {
		return
	}
	sel := v.term.Selection()
	if m.Item(lm.CommonCopy()).Icon(iconCopy).Shortcut(ui.Cmd, ui.KeyC).Disabled(sel == "").Chosen() {
		writeClipboard(sel)
	}
	if m.Item(lm.CommonPaste()).Icon(iconPaste).Shortcut(ui.Cmd, ui.KeyV).Chosen() {
		v.term.PasteClipboard()
	}
	if m.Item(lm.CommonSelectAll()).Icon(iconSelectAll).Shortcut(ui.Cmd, ui.KeyA).Chosen() {
		v.term.SelectAll()
	}
	w, t := a.findTerminal(id)
	if w != nil {
		m.Separator()
		if m.Item(lm.KeysSplitRightLabel()).Icon(iconSplitRight).Keys(a.keyText("splitRight")).Chosen() {
			a.splitFrom(w, v, sideRight)
		}
		if m.Item(lm.KeysSplitDownLabel()).Icon(iconSplitDown).Keys(a.keyText("splitDown")).Chosen() {
			a.splitFrom(w, v, sideBottom)
		}
		if a.splitShown() && m.Item(lm.TabMergePanes()).Icon(iconPane).Keys(a.keyText("closeSplit")).Chosen() {
			a.mergeColumns(w)
		}
	}
	if t != nil {
		m.Separator()
		if m.Item(lm.TabRename()).Icon(iconPencil).Chosen() {
			a.startRename(lm, *t)
		}
		if m.Item(lm.TabCopyTerminalId()).Icon(iconCopy).Chosen() {
			writeClipboard(t.ID)
		}
		if sid := sessionID(*t); sid != "" && m.Item(lm.TermCopySessionId()).Icon(iconCopy).Chosen() {
			writeClipboard(sid)
		}
	}
	m.Separator()
	if m.Item(lm.TermClear()).Icon(iconEraser).Keys(a.keyText("termClear")).Chosen() {
		a.clearTerminal(lm, v, id)
	}
	if t != nil {
		m.Separator()
		if m.Item(lm.TermClose()).Icon(iconClose).Danger().Keys(a.keyText("closeTab")).Chosen() {
			a.askClose(lm, *t)
		}
	}
}

// writeClipboard 写剪贴板：菜单的回调里拿不到 ui.Context。
func writeClipboard(s string) { mygo.Clipboard.Write(transfer.TextData(s)) }

// pathDrag 是拖到终端上贴入路径的东西：文件栏里的文件或目录（path），或图片面板里的一张图（image，
// 工具调用里的截图要放下时先写成临时文件，所以带整项）。只用这一种类型：mygo 的一个元素只认一种拖放类型，
// 后登记的会盖掉先登记的。
type pathDrag struct {
	path  string
	image *imageItem
}

// dragPath 让文件栏里的一行可以拖到终端上贴入路径。文件栏滑进来的途中不能拖：
// 行在不动的指针下面移动，mygo 会把这一下点击当成拖动。
func (a *app) dragPath(row ui.Element, path string) {
	if a.filesPanelShown >= 1 {
		row.Drag(pathDrag{path: path})
	}
}

// termDrop 让终端区接住从访达和文件栏拖来的文件，把路径贴进终端；拖到上面时画一圈强调色的框。
func (a *app) termDrop(c *ui.Context, area ui.Element, v *termView, agent string) {
	_, inner := ui.DragOver[pathDrag](area)
	if area.FileDragOver() || inner {
		accent := c.Theme().Accent
		area.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Stroke(ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}, accent, 8, 2)
		})
	}
	paths := area.DroppedFiles()
	if d, ok := ui.Drop[pathDrag](area); ok {
		if d.image != nil {
			a.withImagePath(*d.image, func(path string) error {
				if a.live(v) && v.term != nil {
					sendPieces(v.term, dropPieces([]string{path}, agent))
					a.focusTerm = true
				}
				return nil
			})
		} else {
			paths = append(paths, d.path)
		}
	}
	if len(paths) == 0 || !a.live(v) || v.term == nil {
		return
	}
	sendPieces(v.term, dropPieces(paths, agent))
	a.focusTerm = true
}

// copiedFiles 是剪贴板里复制的文件：只有文件、或者文本只是这些文件的名字（访达复制文件时的样子）。
// 文本是别的内容（编辑器、邮件里连着附件复制的一段字）时返回 nil，照常粘贴文本。
func copiedFiles(d transfer.Data) []string {
	paths, _ := d.Files()
	if len(paths) == 0 {
		return nil
	}
	text, _ := d.Read(transfer.Text)
	if len(bytes.TrimSpace(text)) == 0 {
		return paths
	}
	names := strings.FieldsFunc(string(text), func(r rune) bool { return r == '\n' || r == '\r' })
	if len(names) != len(paths) {
		return nil
	}
	for i, n := range names {
		if strings.TrimSpace(n) != filepath.Base(paths[i]) {
			return nil
		}
	}
	return paths
}

// sendPieces 把 dropPieces 的结果送进终端：拖进来的文件和粘贴的访达文件共用。
func sendPieces(term *terminal.Terminal, pieces []dropPiece) {
	for _, p := range pieces {
		if p.paste {
			term.Paste(p.text)
		} else {
			term.Send([]byte(p.text))
		}
	}
}

// dropPiece 是拖进终端的一段输入：paste 为真时按粘贴发（程序开了括号粘贴就带上标记），否则当作键入。
type dropPiece struct {
	text  string
	paste bool
}

// dropPieces 照 Orca 把拖进来的路径变成输入：agent 认得出的图片按粘贴发，当成附件（pastedImage），
// 路径里没有特殊字符时原样发，有空格等字符时用反斜杠转义（路径里有单引号时，Claude、Grok 认不出单引号括起来的写法），
// shell 里仍是一个参数；Cursor 连反斜杠转义的单引号也认不出，改发 file:// 链接。
// 其他文件加单引号后当作键入，后面带一个空格。两张原样的图片之间不加空格，否则空格会留在 agent 的输入框里。
func dropPieces(paths []string, agent string) []dropPiece {
	var out []dropPiece
	for i, p := range paths {
		if !pastedImage(agent, p) {
			out = append(out, dropPiece{text: shellQuote(p) + " "})
			continue
		}
		text := shellEscape(p)
		if agent == "cursor" && !shellSafe(p) {
			text = (&url.URL{Scheme: "file", Path: p}).String()
		}
		out = append(out, dropPiece{text: text, paste: true})
		if i+1 < len(paths) && !(shellSafe(p) && pastedImage(agent, paths[i+1]) && shellSafe(paths[i+1])) {
			out = append(out, dropPiece{text: " "})
		}
	}
	return out
}

// imageExts 是粘贴路径时 agent 除了 png、jpg、gif、webp 以外还当成图片附件的扩展名，Claude 只认这四种
// （实测 Claude Code 2.1.303、Codex 0.163.0、Grok 1.0.63、Cursor 2026.10.16）。
var imageExts = map[string][]string{
	"codex":  {".tif", ".tiff"},
	"grok":   {".tif", ".tiff", ".bmp"},
	"cursor": {".tif", ".tiff", ".bmp", ".heic"},
}

// pastedImage 判断路径按图片粘贴。
func pastedImage(agent, p string) bool {
	return isImagePath(p) || slices.Contains(imageExts[agent], strings.ToLower(filepath.Ext(p)))
}

var shellSafeRe = regexp.MustCompile(`^[a-zA-Z0-9_./@:%+=,-]+$`)

func shellSafe(p string) bool { return shellSafeRe.MatchString(p) }

// shellEscape 给 ASCII 特殊字符前加反斜杠，中文等非 ASCII 字符在 shell 里不特殊，原样留着。
func shellEscape(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r < 0x80 && !shellSafe(string(r)) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// shellQuote 给路径加单引号，没有特殊字符时原样返回。
func shellQuote(p string) string {
	if shellSafe(p) {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
