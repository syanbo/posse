package app

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/files"
)

// 跳到定义：编辑器里 ⌘点击、F12 或右键"跳到定义"。照 Zed 的 hover_links 先看指针下是什么（files.LinkAt）：
// 网址交给浏览器；引号里的路径按 import 的规则解析成文件（files.ResolveImport）打开；标识符在工作区里找定义
// （files.FindDefinitions，按声明写法的正则，没有语法树），这一行 import 的文件优先。只有一处明确的定义就直接跳过去，
// 几处同名的弹出列表选；跳走之前记下位置，⌃- 回来。光标本来就在那处定义上时照 Zed 退化为找用法：文件栏的检索页按整词搜它。
// 按住 ⌘ 时指针下的这一段加下划线、指针变手形（mygo 编辑器的 LinkAt、link）。

const maxJumps = 50

// here 是当前文件和光标的位置，给跳转历史记的。
func (v *filesView) here() (jumpLoc, bool) {
	d := v.cur
	if d == nil || d.virtual() {
		return jumpLoc{}, false
	}
	loc := jumpLoc{path: d.path}
	if d.kind == docText && d.ed.Buffer != nil {
		pos := d.ed.CursorPos()
		loc.line, loc.col = pos.Line+1, pos.Col
	}
	return loc, true
}

// pushJump 记下跳走之前的位置，⌃- 回去；和栈顶相同的不重复记。
func (v *filesView) pushJump(loc jumpLoc) {
	if n := len(v.jumps); n > 0 && v.jumps[n-1] == loc {
		return
	}
	v.jumps = append(v.jumps, loc)
	if len(v.jumps) > maxJumps {
		v.jumps = v.jumps[len(v.jumps)-maxJumps:]
	}
}

// defSearch 是正在进行的一次查找，每次加一的 gen 让旧查找的结果作废。
type defSearch struct {
	gen     int
	cancel  context.CancelFunc
	running bool
	word    string
	doc     *fileDoc // 发起查找的文档，"正在查找"的提示画在它上面
	origin  jumpLoc  // 发起查找时的位置，从选择对话框跳走时记下它
}

// jumpLoc 是跳走之前的位置，line 从 1 起，col 是行内的字节偏移。
type jumpLoc struct {
	path      string
	line, col int
}

// defPick 是几处同名定义的选择对话框。
type defPick struct {
	open  bool
	ws    string
	word  string
	defs  []files.Definition
	sel   int
	moved bool // 这一帧用方向键换了选中项，要滚到它
}

// goToDefinition 找编辑器 off 处标识符的定义。
func (a *app) goToDefinition(w *workspace, v *filesView, d *fileDoc, off int) {
	snap := d.ed.Buffer.Snapshot()
	ln := snap.LineOf(off)
	col := off - snap.LineStart(ln)
	lineText := snap.Line(ln)
	lk := files.LinkAt(lineText, col)
	origin := jumpLoc{path: d.path, line: ln + 1, col: col}
	if lk.Kind == files.LinkWord && !files.IsCode(d.path) {
		lk.Kind = files.LinkNone
	}
	switch lk.Kind {
	case files.LinkNone:
		return
	case files.LinkURL:
		if err := mygo.Shell.OpenExternal(lk.Text); err != nil {
			a.err = err.Error()
		}
		return
	case files.LinkPath:
		p, line, ok := files.ResolveImport(v.root, d.path, lk.Text)
		if !ok {
			d.hint, d.hintAt = fmt.Sprintf("找不到 %s", lk.Text), time.Now()
			return
		}
		v.jumpFrom = &origin
		a.openFile(w, p, line, [2]int{})
		return
	}
	word, qual := lk.Text, lk.Qual
	// picker.openAlbum() 里 picker 是变量：在这个文件里往上找它的类型，按类型去找方法
	if qual != "" {
		if t := files.TypeOf(d.path, snap.String(), qual, ln); t != "" {
			qual = t
		}
	}
	from := ""
	if rel, err := filepath.Rel(v.root, d.path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		from = rel
	}
	s := &v.def
	s.gen++
	gen := s.gen
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	s.cancel, s.running, s.word, s.doc = cancel, true, word, d
	q := files.DefinitionQuery{Word: word, Qual: qual, From: from, Prefer: files.PreferFor(v.root, d.path, lineText)}
	a.refreshList(v, false, func() {
		if s.gen != gen {
			return
		}
		overlay := editorOverlay(v)
		l := v.list
		// 这个文件 import 的东西：import 进来的名字（或限定符是 import 的别名）优先去它来自的文件里找
		named, all := files.Imports(l, v.root, d.path, snap.String())
		q.Imported = all
		if p := named[word]; p != "" {
			q.Prefer = p
		} else if p := named[qual]; qual != "" && p != "" {
			q.Prefer = p
		}
		go func() {
			defs, err := files.FindDefinitions(ctx, l, overlay, q)
			cancel()
			a.update(func() {
				if s.gen != gen {
					return
				}
				s.running = false
				switch {
				case err != nil && len(defs) == 0:
					a.err = "查找定义失败：" + err.Error()
				case len(defs) == 0:
					d.hint, d.hintAt = fmt.Sprintf("没找到 %s 的定义", word), time.Now()
				default:
					// 光标所在的那一行本身就是定义：照 Zed 把它排除，别的都没有就找用法
					here := func(x files.Definition) bool { return from != "" && x.Path == from && x.Line == ln }
					if slices.ContainsFunc(defs, here) {
						if defs = slices.DeleteFunc(defs, here); len(defs) == 0 {
							a.searchUsages(w, v, word)
							return
						}
					}
					if u, ok := files.Unique(defs); ok {
						a.jumpTo(w, v, origin, u)
						return
					}
					a.defPick = defPick{open: true, ws: w.ID, word: word, defs: defs}
					v.def.origin = origin
				}
			})
		}()
	})
}

// searchUsages 在文件栏的检索页按整词搜 word，光标已经在定义上时用。
func (a *app) searchUsages(w *workspace, v *filesView, word string) {
	a.openFilesPanel(w, sideSearch)
	v.search.query = files.Query{Text: `\b` + regexp.QuoteMeta(word) + `\b`, MatchCase: true, Regex: true}
	a.startSearch(v, 0)
}

// editorOverlay 返回有未保存改动的文件（相对工作区的路径 → 编辑器里的文本），查找时用它们代替磁盘上的内容。
func editorOverlay(v *filesView) map[string]string {
	overlay := map[string]string{}
	for _, d := range v.docs {
		if d.isModified() {
			if rel, err := filepath.Rel(v.root, d.path); err == nil {
				overlay[rel] = d.ed.Buffer.String()
			}
		}
	}
	return overlay
}

// jumpTo 打开定义所在的文件并选中名字，出发点（⌘点击的位置）由 openDoc 记进历史。
func (a *app) jumpTo(w *workspace, v *filesView, origin jumpLoc, d files.Definition) {
	v.jumpFrom = &origin
	a.openFile(w, filepath.Join(v.root, d.Path), d.Line+1, d.Span)
}

// linkRange 是按住 ⌘ 时编辑器要在 off 处画下划线的范围：网址、引号里的路径或标识符。
func linkRange(d *fileDoc, off int) (int, int) {
	snap := d.ed.Buffer.Snapshot()
	ln := snap.LineOf(off)
	start := snap.LineStart(ln)
	lk := files.LinkAt(snap.Line(ln), off-start)
	if lk.Kind == files.LinkNone || lk.Kind == files.LinkWord && !files.IsCode(d.path) {
		return 0, 0
	}
	return start + lk.Start, start + lk.End
}

// jumpBack 回到上一次跳走之前的位置（⌃-），当前位置记到前进栈。
func (a *app) jumpBack(w *workspace) {
	v := a.files[w.ID]
	if v == nil || len(v.jumps) == 0 {
		return
	}
	loc := v.jumps[len(v.jumps)-1]
	v.jumps = v.jumps[:len(v.jumps)-1]
	if cur, ok := v.here(); ok {
		v.forward = append(v.forward, cur)
	}
	a.goTo(w, v, loc)
}

// jumpForward 是 ⌃- 的反向（⌃⇧-）。
func (a *app) jumpForward(w *workspace) {
	v := a.files[w.ID]
	if v == nil || len(v.forward) == 0 {
		return
	}
	loc := v.forward[len(v.forward)-1]
	v.forward = v.forward[:len(v.forward)-1]
	if cur, ok := v.here(); ok {
		v.pushJump(cur)
	}
	a.goTo(w, v, loc)
}

// goTo 打开历史里的位置，不再记历史。
func (a *app) goTo(w *workspace, v *filesView, loc jumpLoc) {
	v.navigating = true
	a.openFile(w, loc.path, loc.line, [2]int{loc.col, loc.col})
	v.navigating = false
}

// defDialog 是几处同名定义的选择列表：↑↓ 选、回车或点击跳过去。
func (a *app) defDialog(c *ui.Context) {
	p := &a.defPick
	if !p.open {
		return
	}
	w := a.workspace(p.ws)
	if w == nil {
		p.open = false
		return
	}
	v := a.filesFor(w)
	t := c.Theme()
	n := len(p.defs)
	chosen := false
	ui.Modal(c, &p.open, func() {
		// 窗口级快捷键要在对话框里面问才算数（对话框外面的在它打开时一律收不到）
		switch {
		case c.Shortcut(0, ui.KeyDown):
			p.sel, p.moved = (p.sel+1)%n, true
		case c.Shortcut(0, ui.KeyUp):
			p.sel, p.moved = (p.sel+n-1)%n, true
		}
		chosen = c.Shortcut(0, ui.KeyEnter)
		ui.Column(c).Width(620).Gap(8).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(2, 2, 0, 2).Children(func() {
				ui.Text(c, p.word).FontSize(fsCode).FontWeight(fwBold).Font(monoFont)
				ui.Text(c, fmt.Sprintf("有 %d 处定义", n)).FontSize(fsBody).TextColor(t.TextMuted)
			})
			ui.Scroll(c).MaxHeight(400).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					for i, d := range p.defs {
						if defRow(c, d, i == p.sel, p.moved) {
							p.sel, chosen = i, true
						}
					}
				})
			})
		})
	})
	p.moved = false
	if chosen && n > 0 {
		p.open = false
		a.jumpTo(w, v, v.def.origin, p.defs[p.sel])
	}
}

// defRow 是选择列表里的一处定义：路径和行号在上，那一行的代码在下、名字铺底色。返回是否被点了。
func defRow(c *ui.Context, d files.Definition, selected, reveal bool) bool {
	t := c.Theme()
	key := d.Path + ":" + strconv.Itoa(d.Line)
	row := ui.Column(c.Key(key)).Padding(6, 12).Gap(4).Radius(rRow).Role(ui.RoleButton).Label(key)
	selectedBg(c, row, selected)
	if selected && reveal {
		row.ScrollIntoView()
	}
	mark := colors(t).mark
	row.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconFile).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, d.Path+":"+strconv.Itoa(d.Line+1)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
			ui.Text(c, d.Kind).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		})
		text := trimIndent(d.Text)
		shift := len(d.Text) - len(text)
		var marks [][2]int
		if d.Span[1] > d.Span[0] && d.Span[0] >= shift {
			marks = [][2]int{{d.Span[0] - shift, d.Span[1] - shift}}
		}
		ui.RichText(c, markSpans(text, marks, ui.Span{Size: fsCode, Font: monoFont}, mark)...).SingleLine().Shrink(1)
	})
	return row.Clicked()
}

// trimIndent 去掉行首的缩进。
func trimIndent(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}
