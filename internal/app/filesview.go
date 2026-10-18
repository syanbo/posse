package app

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/files"
)

func dot(c *ui.Context, color ui.Color) ui.Element {
	return ui.Box(c).Size(6, 6).Radius(3).Background(color).Shrink(0)
}

// filesPage 是文件标签的内容：打开的文件。文件树、检索和改动在右边的文件栏里。
func (a *app) filesPage(c *ui.Context, w *workspace) {
	ui.Column(c).Grow(1).Shrink(1).Background(c.Theme().Background).Children(func() { a.docArea(c, w, a.filesFor(w)) })
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
		// 尺寸照 WebStorm 的项目树（界面字号 13、行高 24、每层缩进约 18）量出来的
		row := ui.Row(c.Key(r.Path)).Height(24).Margin(0, 6).Padding(0, 8, 0, 6+float32(r.Depth)*18).Gap(6).Radius(6).
			AlignItems(ui.Center).Role(ui.RoleButton).Label(r.Name)
		selectedBg(c, row, r.Path == curPath)
		ignored := v.list.Ignored(r.Path, r.Dir)
		row.Children(func() {
			if r.Dir {
				turn := float32(0)
				if v.tree.Expanded(r.Path) {
					turn = 90
				}
				ui.Icon(c, iconChevron).FontSize(12).TextColor(t.TextMuted).Rotate(turn).Shrink(0)
				ui.Icon(c, iconFolder).FontSize(16).TextColor(t.Accent).Shrink(0)
			} else {
				ui.Box(c).Width(12).Shrink(0)
				ui.Icon(c, iconFile).FontSize(16).TextColor(t.TextMuted).Shrink(0)
			}
			name := ui.Text(c, r.Name).FontSize(13).SingleLine().Shrink(1).Grow(1)
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
			switch {
			case r.Dir:
				a.toggleDir(v, r.Path)
			case row.DoubleClicked():
				a.openFile(w, r.Path, 0, [2]int{})
			default:
				a.previewFile(w, r.Path)
			}
		}
		a.pathMenu(c, row, w, v, r.Path, r.Dir)
		a.dragPath(row, r.Path)
	}).Grow(1).Padding(0, 0, 8, 0)
}

// pathMenu 是文件树和检索结果里一行的右键菜单；在 git 仓库里的文件多了文件历史和 Annotate。
func (a *app) pathMenu(c *ui.Context, e ui.Element, w *workspace, v *filesView, path string, dir bool) {
	e.ContextMenu(func(m *ui.Menu) {
		if m.Item("复制路径").Chosen() {
			c.WriteClipboard(path)
		}
		if rel, err := filepath.Rel(v.root, path); err == nil && m.Item("复制相对路径").Chosen() {
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
		if !dir {
			a.gitFileMenu(m, w, v, path)
		}
	})
}

// --- 检索

func (a *app) searchView(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	s := &v.search
	ui.Column(c).Padding(0, 10, 8, 10).Gap(6).Shrink(0).Children(func() {
		in := fieldInput(c, &s.query.Text, "在文件里检索")
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
			if fieldInput(c.Key("include"), &s.query.Include, "包含，如 *.go, src/").Changed() {
				a.startSearch(v, 300*time.Millisecond)
			}
			if fieldInput(c.Key("exclude"), &s.query.Exclude, "排除，如 vendor, *_test.go").Changed() {
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
				ui.Icon(c, iconFile).FontSize(16).TextColor(t.TextMuted).Shrink(0)
				ui.Text(c, filepath.Base(h.Path)).FontSize(fsBody).FontWeight(500).SingleLine().Shrink(0)
				if dir := filepath.Dir(h.Path); dir != "." {
					ui.Text(c, dir).FontSize(11).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprint(r.count)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			a.pathMenu(c, row, w, v, abs, false)
			return
		}
		row := ui.Row(c.Key(i)).Margin(0, 6).Padding(3, 8, 3, 8).Gap(8).Radius(6).AlignItems(ui.Start).Role(ui.RoleButton)
		selectedBg(c, row, false)
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

func (a *app) docArea(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	if v.cur == nil {
		filesEmpty(c)
		return
	}
	d := v.cur
	// diff 读的时候 diff 栏照样画，连按 F7 换文件时不闪
	if d.kind == docLoading && d.diff == nil {
		ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(16, 18)
		return
	}
	if d.comp != nil {
		if d.kind == docCompare {
			a.compareView(c, w, v, d)
		} else {
			docMessage(c, d, d.err)
		}
		return
	}
	if d.diff != nil {
		a.diffBar(c, w, v, d)
		switch d.kind {
		case docLoading:
			ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(16, 18)
		case docDiff:
			a.diffView(c, v, d)
		case docImageDiff:
			imageDiffView(c, d)
		case docBinary:
			docMessage(c, d, "二进制文件，不显示 diff")
		case docTooLarge:
			docMessage(c, d, tooLargeDiff)
		default:
			docMessage(c, d, d.err)
		}
		diffHint(c, d.diff)
		return
	}
	a.docBar(c, w, v, d)
	a.docBanner(c, d)
	switch d.kind {
	case docText:
		// Annotate 开着时在这个标签里显示逐行作者，关掉回到编辑器
		if d.blame != nil {
			a.blameView(c, w, v, d)
			return
		}
		if d.preview && isMarkdown(d.path) {
			markdownPreview(c, d)
			return
		}
		d.ed.Font = ui.Font{Family: termFontFamily, Size: a.fontSize - editorInset}
		d.ed.OnChangeClick = func(i int, _ float32) { d.change = &changePop{index: i} }
		// 包一层：改动卡片按行的位置浮在编辑器上面
		ui.Box(c).Grow(1).Children(func() {
			el := editor.View(c.Key("ed:"+d.path), &d.ed).Ref(&d.edRef).Fill().AutoFocus()
			if d.focus {
				el.Focus()
				d.focus = false
			}
			a.changePopup(c, w, v, d)
		})
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
		titleText(c, "在右边的文件栏里选一个文件", fsCardTitle)
		ui.Row(c).Gap(14).Children(func() {
			shortcutHint(c, "⌘P", "按名称打开")
			shortcutHint(c, "⇧⌘F", "在文件里检索")
			shortcutHint(c, "⌘L", "文件栏")
		})
	})
}

// docTabs 把工作区打开的文件画成标签栏里的标签，排在终端后面；同名文件后面带上级目录名。
func (a *app) docTabs(c *ui.Context, w *workspace) {
	v := a.files[w.ID]
	if v == nil {
		return
	}
	names := map[string]int{}
	for _, d := range v.docs {
		names[d.name()]++
	}
	// 点 × 会在这次遍历里关掉文件，slices.Delete 把末尾那格清成 nil：遍历副本
	for _, d := range slices.Clone(v.docs) {
		detail := ""
		switch {
		case d.diff != nil:
			detail = d.diff.tabLabel()
		case d.comp != nil:
			detail = "比较"
		case names[d.name()] > 1:
			detail = filepath.Base(filepath.Dir(d.path))
		}
		a.docTab(c, w, v, d, detail)
	}
}

// docIcon 是文件标签的图标：分支比较用分支图标，其余是文件。
func docIcon(d *fileDoc) *ui.SVG {
	switch {
	case d.comp != nil:
		return iconBranch
	}
	return iconFile
}

// docTab 是标签栏里的一个文件，样子和终端标签一样。
func (a *app) docTab(c *ui.Context, w *workspace, v *filesView, d *fileDoc, detail string) {
	t := c.Theme()
	active := a.filesActive(w) && v.cur == d
	// 文件名短，不跟着收缩：放不下时先缩终端标签里的标题
	row := ui.Row(c.Key("doc:"+d.path)).Height(28).Padding(0, 4, 0, 8).Gap(6).Radius(8).Shrink(0).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(d.name()).Tooltip(shortPath(d.path))
	hovered := row.Hovered()
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case hovered:
		bg = colors(t).wash
	}
	a.revealTab(row, "doc:"+d.path, active)
	row.Background(row.AnimateColor("bg", bg, hover))
	modified := d.isModified()
	row.Children(func() {
		ui.Icon(c, docIcon(d)).FontSize(14).TextColor(t.TextMuted).Shrink(0)
		name := ui.Text(c, d.name()).FontSize(12).SingleLine().MaxWidth(180)
		tabText(c, name, active)
		if d.isPreview() {
			name.Italic()
		}
		if d.deleted {
			name.Strikethrough()
		}
		if detail != "" {
			ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).SingleLine().MaxWidth(100)
		}
		// 未保存时显示圆点，指上去换成关闭按钮，和常见编辑器一样
		btn := ui.Box(c.Key("close")).Size(20, 20).Radius(4).Center().Shrink(0).Role(ui.RoleButton).Label("关闭文件").Tooltip("关闭 ⌘W")
		if btn.Hovered() {
			btn.Background(t.SurfacePressed)
		}
		btn.Children(func() {
			switch {
			case modified && !hovered:
				dot(c, t.Accent)
			case active || hovered:
				ui.Icon(c, iconClose).FontSize(12).TextColor(t.TextMuted)
			}
		})
		if (active || hovered) && btn.Clicked() {
			a.closeDoc(v, d)
		}
	})
	// 双击预览标签让它常驻
	if d.transient && row.DoubleClicked() {
		d.transient = false
	}
	if row.MiddleClicked() {
		a.closeDoc(v, d)
	}
	row.ContextMenu(func(m *ui.Menu) { a.docTabMenu(c, m, v, d) })
	a.reorderableDoc(c, row, w.ID, v, d)
	if row.Clicked() && !active {
		a.showFiles(w)
		if v.cur != d {
			v.cur = d
			a.saveFilesState(v)
		}
		d.focus = true
	}
}

// docDrag 是拖动中的文件标签，只在同一个工作区的文件标签之间换位置。
type docDrag struct{ ws, path string }

// reorderableDoc 让文件标签可以拖动排序，顺序随打开的文件存进 ui.json。
func (a *app) reorderableDoc(c *ui.Context, row ui.Element, wsID string, v *filesView, d *fileDoc) {
	row.Drag(docDrag{wsID, d.path})
	if row.Dragging() {
		row.Opacity(0.4)
	}
	index := func(path string) int {
		return slices.IndexFunc(v.docs, func(x *fileDoc) bool { return x.path == path })
	}
	if g, over := ui.DragOver[docDrag](row); over && g.ws == wsID && g.path != d.path {
		dropMark(c, row, index(g.path) < index(d.path), true)
	}
	if g, ok := ui.Drop[docDrag](row); ok && g.ws == wsID && g.path != d.path {
		if from, to := index(g.path), index(d.path); from >= 0 && to >= 0 {
			moved := v.docs[from]
			v.docs = slices.Insert(slices.Delete(v.docs, from, from+1), to, moved)
			a.saveFilesState(v)
			c.AnimationFrame()
		}
	}
}

// docTabMenu 是文件标签的右键菜单（照 Orca）：关闭这个、其他的、右边的、全部文件，复制路径和在访达中显示。
// 有未保存的文件时先问一次。
func (a *app) docTabMenu(c *ui.Context, m *ui.Menu, v *filesView, d *fileDoc) {
	i := slices.Index(v.docs, d)
	if i < 0 {
		return
	}
	if m.Item("关闭").Shortcut(ui.Cmd, ui.KeyW).Chosen() {
		a.closeDoc(v, d)
	}
	others := slices.Concat(v.docs[:i], v.docs[i+1:])
	right := slices.Clone(v.docs[i+1:])
	if m.Item("关闭其他文件").Disabled(len(others) == 0).Chosen() {
		a.closeDocs(v, others)
	}
	if m.Item("关闭右侧文件").Disabled(len(right) == 0).Chosen() {
		a.closeDocs(v, right)
	}
	if m.Item("关闭所有文件").Chosen() {
		a.closeDocs(v, slices.Clone(v.docs))
	}
	if d.virtual() {
		return
	}
	m.Separator()
	if m.Item("复制路径").Chosen() {
		c.WriteClipboard(d.path)
	}
	if rel, err := filepath.Rel(v.root, d.path); err == nil && m.Item("复制相对路径").Chosen() {
		c.WriteClipboard(rel)
	}
	if m.Item("在访达中显示").Chosen() {
		mygo.Shell.ShowItemInFolder(d.path)
	}
}

// docBar 是文件上方的一行：相对路径和操作。
func (a *app) docBar(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
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
		if d.kind == docText && v.git.relPath(d.path) != "" {
			if pressable(iconButtonSized(c, iconHistory, "文件历史", 24, 14)).Clicked() {
				a.openLog(w, "", v.git.relPath(d.path))
			}
			if d.blame != nil {
				if pressable(textButton(c, "关闭 Annotate")).Clicked() {
					d.blame = nil
				}
			} else if pressable(iconButtonSized(c, iconBlame, "Annotate：逐行看是谁改的", 24, 14)).Clicked() {
				a.openBlame(w, v, d.path)
			}
			if e := v.git.entry(d.path); e != nil && !e.Conflict {
				if pressable(iconButtonSized(c, iconRollback, "回滚这个文件…", 24, 14)).Clicked() {
					a.rollbackDoc(v, d)
				}
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
	ui.Column(c).Grow(1).AlignSelf(ui.Stretch).Center().Gap(12).Children(func() {
		ui.Text(c, msg).FontSize(13).TextColor(t.TextMuted)
		// diff、日志这些不是磁盘上的文件（路径是 diff:…、log:…），没有可打开的
		if filepath.IsAbs(d.path) && (d.kind != docError || !d.deleted) {
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

// openQuick 打开快速打开，清单不新鲜就重列；选中文件后才切到文件标签。
func (a *app) openQuick(w *workspace) {
	v := a.filesFor(w)
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
			in := fieldInput(c, &q.query, "按名称查找文件，加 :行号 跳到那一行").AutoFocus()
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
	selectedBg(c, row, selected)
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
