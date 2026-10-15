package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/files"
)

const filesSideWidth = 260

// filesTab 是标签栏最前面的"文件"标签；有未保存的文件时名字后面带一个点。
func (a *app) filesTab(c *ui.Context, w *workspace, active bool) {
	t := c.Theme()
	row := ui.Row(c.Key(filesTabID)).Height(28).Padding(0, 10, 0, 8).Gap(6).Radius(8).Shrink(0).
		Role(ui.RoleButton).Label("文件").Tooltip("文件 ⇧⌘E")
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case row.Hovered():
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	dirty := false
	if v := a.files[w.ID]; v != nil {
		for _, d := range v.docs {
			dirty = dirty || d.isModified()
		}
	}
	row.Children(func() {
		ui.Icon(c, iconFile).FontSize(14).TextColor(t.TextMuted).Shrink(0)
		name := ui.Text(c, "文件").FontSize(12).Shrink(0)
		if active {
			name.FontWeight(500)
		} else {
			name.TextColor(t.TextMuted)
		}
		if dirty {
			dot(c, t.Accent).Tooltip("有未保存的修改")
		}
	})
	if row.Clicked() {
		a.showFiles(w)
	}
}

func dot(c *ui.Context, color ui.Color) ui.Element {
	return ui.Box(c).Size(6, 6).Radius(3).Background(color).Shrink(0)
}

// filesPage 是文件标签的内容：左栏文件树或检索，右边打开的文件。
func (a *app) filesPage(c *ui.Context, w *workspace) {
	t := c.Theme()
	p := colors(t)
	v := a.filesFor(w)
	if v.poll.due(c, filesPollEvery) {
		a.pollFiles(v)
	}
	a.refreshList(v, false, nil)
	if d := v.cur; d != nil && d.kind == docLoading && !d.started {
		d.started = true
		a.loadDoc(d)
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(filesSideWidth).Shrink(0).Background(p.band).Children(func() { a.filesSide(c, w, v) })
		ui.Box(c).Width(1).Shrink(0).Background(p.line)
		ui.Column(c).Grow(1).Shrink(1).Background(t.Background).Children(func() { a.docArea(c, v) })
	})
}

func (a *app) filesSide(c *ui.Context, w *workspace, v *filesView) {
	ui.Row(c).Height(40).Shrink(0).Padding(0, 10).AlignItems(ui.Center).Children(func() {
		if segmented(c, &v.side, "左栏", "文件", "检索") && v.side == 1 {
			v.search.focus = true
		}
	})
	if v.side == 0 {
		a.treeView(c, w, v)
	} else {
		a.searchView(c, w, v)
	}
}

// --- 文件树

func (a *app) treeView(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	rows := v.tree.Rows()
	if len(rows) == 0 {
		msg := "目录是空的"
		if !v.tree.Loaded(v.root) {
			msg = "正在读取…"
		}
		ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		return
	}
	curPath := ""
	if v.cur != nil {
		curPath = v.cur.path
	}
	modified := map[string]bool{}
	for _, d := range v.docs {
		if d.isModified() {
			modified[d.path] = true
		}
	}
	v.treeList.Key = func(i int) any { return rows[i].Path }
	ui.List(c, &v.treeList, len(rows), func(i int) {
		r := rows[i]
		row := ui.Row(c.Key(r.Path)).Height(26).Margin(0, 6).Padding(0, 8, 0, 6+float32(r.Depth)*14).Gap(5).Radius(6).
			AlignItems(ui.Center).Role(ui.RoleButton).Label(r.Name)
		selectable(c, row, r.Path == curPath)
		ignored := v.list.Ignored(r.Path, r.Dir)
		row.Children(func() {
			if r.Dir {
				turn := float32(0)
				if v.tree.Expanded(r.Path) {
					turn = 90
				}
				ui.Icon(c, iconChevron).FontSize(11).TextColor(t.TextMuted).Rotate(turn).Shrink(0)
				ui.Icon(c, iconFolder).FontSize(14).TextColor(t.Accent).Shrink(0)
			} else {
				ui.Box(c).Width(11).Shrink(0)
				ui.Icon(c, iconFile).FontSize(14).TextColor(t.TextMuted).Shrink(0)
			}
			name := ui.Text(c, r.Name).FontSize(12.5).SingleLine().Shrink(1).Grow(1)
			if ignored {
				name.TextColor(t.TextMuted)
			}
			if modified[r.Path] {
				dot(c, t.Accent).Tooltip("有未保存的修改")
			}
		})
		if ignored {
			row.Opacity(0.7)
		}
		if row.Clicked() {
			if r.Dir {
				a.toggleDir(v, r.Path)
			} else {
				a.openFile(w, r.Path, 0, [2]int{})
			}
		}
		pathMenu(c, row, r.Path, v.root, a)
	}).Grow(1).Padding(0, 0, 8, 0)
}

// pathMenu 是文件树和检索结果里一行的右键菜单。
func pathMenu(c *ui.Context, e ui.Element, path, root string, a *app) {
	e.ContextMenu(func(m *ui.Menu) {
		if m.Item("复制路径").Chosen() {
			c.WriteClipboard(path)
		}
		if rel, err := filepath.Rel(root, path); err == nil && m.Item("复制相对路径").Chosen() {
			c.WriteClipboard(rel)
		}
		m.Separator()
		if m.Item("在访达中显示").Chosen() {
			mygo.Shell.ShowItemInFolder(path)
		}
		if m.Item("用默认应用打开").Chosen() {
			if err := mygo.Shell.OpenPath(path); err != nil {
				a.err = err.Error()
			}
		}
	})
}

// --- 检索

func (a *app) searchView(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	s := &v.search
	ui.Column(c).Padding(0, 10, 8, 10).Gap(6).Shrink(0).Children(func() {
		in := ui.TextInput(c, &s.query.Text).Placeholder("在文件里检索").Label("检索")
		if s.focus {
			in.Focus()
			s.focus = false
		}
		if s.query.Text != "" && in.Shortcut(0, ui.KeyEscape) {
			s.query.Text = ""
			a.startSearch(v, 0)
		} else if in.Changed() {
			a.startSearch(v, 200*time.Millisecond)
		} else if in.Submitted() {
			a.startSearch(v, 0)
		}
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if toggleChip(c, "Aa", "区分大小写", s.query.MatchCase) {
				s.query.MatchCase = !s.query.MatchCase
				a.startSearch(v, 0)
			}
			if toggleChip(c, ".*", "正则表达式", s.query.Regex) {
				s.query.Regex = !s.query.Regex
				a.startSearch(v, 0)
			}
			filtered := s.query.Include != "" || s.query.Exclude != ""
			if toggleChip(c, "筛选", "只搜或不搜某些文件", s.showFilter || filtered) {
				s.showFilter = !s.showFilter
			}
		})
		if s.showFilter {
			if ui.TextInput(c.Key("include"), &s.query.Include).Placeholder("包含，如 *.go, src/").Label("包含的文件").Changed() {
				a.startSearch(v, 300*time.Millisecond)
			}
			if ui.TextInput(c.Key("exclude"), &s.query.Exclude).Placeholder("排除，如 vendor, *_test.go").Label("排除的文件").Changed() {
				a.startSearch(v, 300*time.Millisecond)
			}
		}
		if msg := searchStatus(s); msg != "" {
			color := t.TextMuted
			if s.err != "" {
				color = t.Danger
			}
			ui.Text(c, msg).FontSize(11).TextColor(color).LineHeight(1.4).Padding(2, 2, 0, 2)
		}
	})
	if len(s.rows) == 0 {
		return
	}
	rows, hits := s.rows, s.hits
	s.list.Key = func(i int) any { return [2]any{rows[i].hit, rows[i].header} }
	ui.List(c, &s.list, len(rows), func(i int) {
		r := rows[i]
		h := hits[r.hit]
		abs := filepath.Join(v.root, h.Path)
		if r.header {
			row := ui.Row(c).Padding(8, 12, 3, 14).Gap(6).AlignItems(ui.Center)
			row.Children(func() {
				ui.Icon(c, iconFile).FontSize(13).TextColor(t.TextMuted).Shrink(0)
				ui.Text(c, filepath.Base(h.Path)).FontSize(12).FontWeight(600).SingleLine().Shrink(0)
				if dir := filepath.Dir(h.Path); dir != "." {
					ui.Text(c, dir).FontSize(11).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprint(r.count)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			pathMenu(c, row, abs, v.root, a)
			return
		}
		row := ui.Row(c.Key(i)).Margin(0, 6).Padding(3, 8, 3, 8).Gap(8).Radius(6).AlignItems(ui.Start).Role(ui.RoleButton)
		selectable(c, row, false)
		text, spans := trimHit(h)
		row.Children(func() {
			ui.Text(c, fmt.Sprint(h.Line+1)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Width(30).Shrink(0).TextAlign(ui.End)
			ui.RichText(c, markSpans(text, spans, ui.Span{Font: monoFont, Size: 11.5}, colors(t).honey.Alpha(0.28))...).SingleLine().Shrink(1)
		})
		if row.Clicked() {
			var span [2]int
			if len(h.Spans) > 0 {
				span = h.Spans[0]
			}
			a.openFile(w, abs, h.Line+1, span)
		}
	}).Grow(1).Padding(0, 0, 8, 0)
}

// trimHit 去掉命中行开头的缩进，命中范围跟着挪。
func trimHit(h files.Hit) (string, [][2]int) {
	text := strings.TrimLeft(h.Text, " \t")
	shift := len(h.Text) - len(text)
	spans := make([][2]int, 0, len(h.Spans))
	for _, s := range h.Spans {
		if s[0] >= shift {
			spans = append(spans, [2]int{s[0] - shift, s[1] - shift})
		}
	}
	return text, spans
}

func searchStatus(s *fileSearch) string {
	switch {
	case s.err != "":
		return s.err
	case strings.TrimSpace(s.query.Text) == "":
		return ""
	case s.running && len(s.hits) == 0:
		return "正在检索…"
	case !s.running && len(s.hits) == 0:
		return "没有找到"
	}
	files := 0
	for _, r := range s.rows {
		if r.header {
			files++
		}
	}
	msg := fmt.Sprintf("%d 个文件里有 %d 行", files, len(s.hits))
	switch {
	case s.truncated:
		msg += "，结果太多，只列出前面这些"
	case s.running:
		msg += "，还在检索"
	}
	return msg
}

// toggleChip 是检索选项的小开关，开着时垫强调色淡底；返回这一帧是否被点了。
func toggleChip(c *ui.Context, label, tip string, on bool) bool {
	t := c.Theme()
	b := ui.Box(c.Key("chip-"+label)).Height(22).Padding(0, 7).Radius(5).Center().Shrink(0).
		Role(ui.RoleButton).Label(tip).Tooltip(tip).Checked(on)
	bg, fg := ui.Transparent, t.TextMuted
	switch {
	case on:
		bg, fg = colors(t).tint, t.Accent
	case b.Hovered():
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() { ui.Text(c, label).FontSize(11.5).TextColor(fg) })
	return b.Clicked()
}

// --- 打开的文件

func (a *app) docArea(c *ui.Context, v *filesView) {
	t := c.Theme()
	if v.cur == nil {
		filesEmpty(c)
		return
	}
	a.docStrip(c, v)
	d := v.cur
	if d.kind == docLoading {
		ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(16, 18)
		return
	}
	a.docBar(c, v, d)
	a.docBanner(c, d)
	switch d.kind {
	case docText:
		if d.preview && isMarkdown(d.path) {
			markdownPreview(c, d)
			return
		}
		d.ed.Font = ui.Font{Family: termFontFamily, Size: a.fontSize - editorInset}
		el := editor.View(c.Key("ed:"+d.path), &d.ed).Grow(1).AutoFocus()
		if d.focus {
			el.Focus()
			d.focus = false
		}
		d.shown++
		if d.gotoLine > 0 {
			// 编辑器第一帧还没有尺寸，跳行要等它排过版
			if d.shown < 2 {
				c.AnimationFrame()
			} else {
				gotoLine(d)
			}
		}
	case docImage:
		ui.Box(c).Grow(1).Padding(16).Center().Children(func() {
			// 位图按 2 倍屏解码，按一半的尺寸显示才是原大
			w, h := d.img.Size()
			ui.Image(c, d.img).Fit(ui.Contain).MaxWidth(float32(w) / 2).MaxHeight(float32(h) / 2).Shrink(1)
		})
	case docBinary:
		docMessage(c, d, "二进制文件，不显示内容")
	case docTooLarge:
		docMessage(c, d, fmt.Sprintf("文件超过 %d MB，不在这里打开", files.MaxSize>>20))
	case docError:
		docMessage(c, d, d.err)
	}
}

// gotoLine 跳到文档要去的行，并选中命中的文字。
func gotoLine(d *fileDoc) {
	snap := d.ed.Buffer.Snapshot()
	line := min(d.gotoLine-1, snap.LineCount()-1)
	d.ed.GoTo(editor.Pos{Line: line})
	if s := d.gotoSpan; s[1] > s[0] {
		start := snap.LineStart(line)
		end := min(start+s[1], snap.LineEnd(line))
		d.ed.Select(min(start+s[0], end), end)
	}
	d.gotoLine, d.gotoSpan = 0, [2]int{}
}

func filesEmpty(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(14).Children(func() {
		tile(c, iconFile, 48, colors(t).tint, t.Accent)
		ui.Text(c, "在左边的文件树里选一个文件").FontSize(14).FontWeight(600)
		ui.Row(c).Gap(14).Children(func() {
			shortcutHint(c, "⌘P", "按名称打开")
			shortcutHint(c, "⇧⌘F", "在文件里检索")
			shortcutHint(c, "⇧⌘E", "回到终端")
		})
	})
}

// docStrip 是打开的文件的一排小标签；同名文件后面带上级目录名。
func (a *app) docStrip(c *ui.Context, v *filesView) {
	t := c.Theme()
	p := colors(t)
	names := map[string]int{}
	for _, d := range v.docs {
		names[d.name()]++
	}
	ui.ScrollHorizontal(c).Height(36).Shrink(0).Background(p.band).BorderWidth(0, 0, 1, 0).BorderColor(p.line).Children(func() {
		ui.Row(c).Height(36).Padding(0, 8).Gap(2).AlignItems(ui.Center).Children(func() {
			for _, d := range v.docs {
				detail := ""
				if names[d.name()] > 1 {
					detail = filepath.Base(filepath.Dir(d.path))
				}
				a.docTab(c, v, d, detail)
			}
		})
	})
}

func (a *app) docTab(c *ui.Context, v *filesView, d *fileDoc, detail string) {
	t := c.Theme()
	active := v.cur == d
	row := ui.Row(c.Key(d.path)).Height(26).Padding(0, 4, 0, 10).Gap(6).Radius(6).Shrink(0).MaxWidth(240).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(d.name()).Tooltip(shortPath(d.path))
	hovered := row.Hovered()
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case hovered:
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	modified := d.isModified()
	row.Children(func() {
		name := ui.Text(c, d.name()).FontSize(12).SingleLine().Shrink(1)
		if !active {
			name.TextColor(t.TextMuted)
		}
		if d.deleted {
			name.Strikethrough()
		}
		if detail != "" {
			ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
		// 未保存时显示圆点，指上去换成关闭按钮，和常见编辑器一样
		btn := ui.Box(c.Key("close")).Size(18, 18).Radius(4).Center().Shrink(0).Role(ui.RoleButton).Label("关闭文件").Tooltip("关闭 ⌘W")
		if btn.Hovered() {
			btn.Background(t.SurfacePressed)
		}
		btn.Children(func() {
			switch {
			case modified && !hovered:
				dot(c, t.Accent)
			case active || hovered:
				ui.Icon(c, iconClose).FontSize(11).TextColor(t.TextMuted)
			}
		})
		if (active || hovered) && btn.Clicked() {
			a.closeDoc(v, d)
		}
	})
	if row.Clicked() && v.cur != d {
		v.cur = d
		d.focus = true
		a.saveFilesState(v)
	}
}

// docBar 是文件上方的一行：相对路径和操作。
func (a *app) docBar(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	rel, err := filepath.Rel(v.root, d.path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = shortPath(d.path)
	}
	ui.Row(c).Height(30).Shrink(0).Padding(0, 8, 0, 14).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, rel).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1).Selectable()
		if d.readOnly {
			ui.Text(c, "只读").FontSize(11).TextColor(t.TextMuted).Padding(1, 6).Radius(4).Background(t.Surface).Shrink(0).
				Tooltip(fmt.Sprintf("文件超过 %d MB，只读打开", files.ReadOnlySize>>20))
		}
		ui.Spacer(c)
		if d.kind == docText && isMarkdown(d.path) {
			label := "预览"
			if d.preview {
				label = "源码"
			}
			if toggleChip(c, label, "切换 Markdown 预览", d.preview) {
				d.preview = !d.preview
			}
		}
		if d.kind == docText && !d.readOnly && d.isModified() {
			if pressable(iconButtonSized(c, iconSave, "保存 ⌘S", 24, 14)).Clicked() {
				a.saveDoc(d, false)
			}
		}
		if pressable(iconButtonSized(c, iconFolder, "在访达中显示", 24, 14)).Clicked() {
			mygo.Shell.ShowItemInFolder(d.path)
		}
		if pressable(iconButtonSized(c, iconCopy, "复制路径", 24, 14)).Clicked() {
			c.WriteClipboard(d.path)
		}
	})
}

// docBanner 说明文件和磁盘上的不一致：被别人改过、被删掉了。
func (a *app) docBanner(c *ui.Context, d *fileDoc) {
	var msg string
	var actions func()
	switch {
	case d.conflict:
		msg = "磁盘上的文件被改过了（可能是 agent），你这里也有没保存的修改。"
		actions = func() {
			if pressable(textButton(c, "保留我的")).Clicked() {
				d.conflict = false
			}
			if pressable(accentButton(c, "用磁盘上的")).Clicked() {
				a.reloadDoc(d)
			}
		}
	case d.deleted:
		msg = "文件已经被删除。"
		if d.kind == docText && !d.readOnly {
			msg += "保存会重新创建它。"
		}
	default:
		return
	}
	t := c.Theme()
	ui.Row(c).Shrink(0).Margin(0, 12, 8, 12).Padding(8, 8, 8, 12).Gap(8).Radius(8).Background(t.Surface).AlignItems(ui.Center).Children(func() {
		ui.Text(c, msg).FontSize(12).LineHeight(1.4).Grow(1).Shrink(1)
		if actions != nil {
			actions()
		}
	})
}

func docMessage(c *ui.Context, d *fileDoc, msg string) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
		ui.Text(c, msg).FontSize(13).TextColor(t.TextMuted)
		if d.kind != docError || !d.deleted {
			if pressable(textButton(c, "用默认应用打开")).Clicked() {
				mygo.Shell.OpenPath(d.path)
			}
		}
	})
}

func isMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}

// markdownPreview 用历史页的 Markdown 渲染显示文档，只在内容变了之后重新解析。
func markdownPreview(c *ui.Context, d *fileDoc) {
	if v := d.ed.Buffer.Version(); d.md == nil || v != d.mdVer {
		d.md, d.mdVer = parseMarkdown(d.ed.Buffer.String()), v
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Row(c).Padding(20, 28, 40, 28).Justify(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MaxWidth(820).Gap(12).Children(func() { mdView{}.blocks(c, d.md) })
		})
	})
}

// --- 快速打开

type quickOpen struct {
	open    bool
	ws      string
	query   string
	sel     int
	matches []files.Match
	key     string      // matches 对应的查询
	list    *files.List // matches 对应的清单
	moved   bool        // 这一帧用方向键换了选中项，要滚到它
}

// openQuick 打开快速打开：切到文件标签，清单不新鲜就重列。
func (a *app) openQuick(w *workspace) {
	v := a.showFiles(w)
	a.quick = quickOpen{open: true, ws: w.ID}
	a.refreshList(v, false, nil)
}

func (a *app) quickDialog(c *ui.Context) {
	q := &a.quick
	if !q.open {
		return
	}
	w := a.workspace(q.ws)
	if w == nil {
		q.open = false
		return
	}
	v := a.filesFor(w)
	t := c.Theme()
	ui.Modal(c, &q.open, func() {
		ui.Column(c).Width(560).Gap(10).Children(func() {
			in := ui.TextInput(c, &q.query).Placeholder("按名称查找文件，加 :行号 跳到那一行").Label("快速打开").AutoFocus()
			path, line := files.SplitLine(q.query)
			if v.list != nil && (q.list != v.list || q.key != path) {
				q.matches, q.key, q.list, q.sel = files.Fuzzy(v.list.Files, path, 50), path, v.list, 0
			}
			n := len(q.matches)
			switch {
			case n > 0 && in.Shortcut(0, ui.KeyDown):
				q.sel, q.moved = (q.sel+1)%n, true
			case n > 0 && in.Shortcut(0, ui.KeyUp):
				q.sel, q.moved = (q.sel+n-1)%n, true
			}
			submitted := in.Submitted()
			switch {
			case v.list == nil:
				ui.Text(c, "正在列出文件…").FontSize(12).TextColor(t.TextMuted).Padding(4, 2)
			case n == 0:
				ui.Text(c, "没有匹配的文件").FontSize(12).TextColor(t.TextMuted).Padding(4, 2)
			default:
				ui.Scroll(c).MaxHeight(380).Children(func() {
					ui.Column(c).Gap(1).Children(func() {
						for i, m := range q.matches {
							if quickRow(c, m, i == q.sel, q.moved) {
								q.sel, submitted = i, true
							}
						}
					})
				})
			}
			q.moved = false
			if submitted && n > 0 {
				q.open = false
				a.openFile(w, filepath.Join(v.root, q.matches[q.sel].Path), line, [2]int{})
			}
		})
	})
}

// quickRow 是快速打开的一条结果：文件名在前，目录在后，命中的字母铺底色。返回是否被点了。
func quickRow(c *ui.Context, m files.Match, selected, reveal bool) bool {
	t := c.Theme()
	row := ui.Row(c.Key(m.Path)).Height(30).Padding(0, 10).Gap(8).Radius(6).AlignItems(ui.Center).Role(ui.RoleButton).Label(m.Path)
	selectable(c, row, selected)
	if selected && reveal {
		row.ScrollIntoView()
	}
	base := strings.LastIndexByte(m.Path, filepath.Separator) + 1
	var nameMarks, dirMarks [][2]int
	for _, r := range posRanges(m.Pos) {
		if r[0] >= base {
			nameMarks = append(nameMarks, [2]int{r[0] - base, r[1] - base})
		} else {
			dirMarks = append(dirMarks, [2]int{r[0], min(r[1], max(base-1, 0))})
		}
	}
	mark := colors(t).honey.Alpha(0.28)
	row.Children(func() {
		ui.Icon(c, iconFile).FontSize(14).TextColor(t.TextMuted).Shrink(0)
		ui.RichText(c, markSpans(m.Path[base:], nameMarks, ui.Span{Size: 13}, mark)...).SingleLine().Shrink(0)
		if base > 0 {
			dir := m.Path[:base-1]
			ui.RichText(c, markSpans(dir, clampMarks(dirMarks, len(dir)), ui.Span{Size: 11.5, Font: monoFont, Color: t.TextMuted}, mark)...).SingleLine().Shrink(1)
		}
	})
	return row.Clicked()
}

// posRanges 把逐字节的命中位置合成连续的区间。
func posRanges(pos []int) [][2]int {
	var out [][2]int
	for _, p := range pos {
		if n := len(out); n > 0 && out[n-1][1] == p {
			out[n-1][1]++
		} else {
			out = append(out, [2]int{p, p + 1})
		}
	}
	return out
}

func clampMarks(ms [][2]int, n int) [][2]int {
	out := ms[:0:0]
	for _, m := range ms {
		if m[0] < n && m[1] > m[0] {
			out = append(out, [2]int{m[0], min(m[1], n)})
		}
	}
	return out
}
