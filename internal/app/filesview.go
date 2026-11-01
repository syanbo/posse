package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/errtext"
	"posse/internal/files"
)

func dot(c *ui.Context, color ui.Color) ui.Element {
	return ui.Box(c).Size(6, 6).Radius(full(6)).Background(color).Shrink(0)
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
		ui.Text(c, msg).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	curPath := ""
	if v.cur != nil {
		curPath = v.cur.path
	}
	if v.reveal != "" && v.cur != v.revealCur {
		v.reveal = ""
	}
	if v.reveal != "" {
		curPath = v.reveal
		if v.revealScroll {
			if i := slices.IndexFunc(rows, func(r files.Row) bool { return r.Path == v.reveal }); i >= 0 {
				v.treeList.ScrollTo(i, ui.Center)
				v.revealScroll = false
			}
		}
	}
	modified := map[string]bool{}
	for _, d := range v.docs {
		if d.isModified() {
			modified[d.path] = true
		}
	}
	marks := v.git.treeMarks()
	v.treeList.Key = func(i int) any { return rows[i].Path }
	ui.List(c, &v.treeList, len(rows), func(i int) {
		r := rows[i]
		// 尺寸照 WebStorm 的项目树（界面字号 13、行高 24、每层缩进约 18）量出来的
		row := ui.Row(c.Key(r.Path)).Height(sz(24)).Margin(2, 6, 0, 6).Padding(0, 8, 0, 6+float32(r.Depth)*treeIndent).Gap(6).Radius(rRow).
			AlignItems(ui.Center).Role(ui.RoleButton).Label(r.Name)
		selectedBg(c, row, r.Path == curPath)
		ignored := v.list.Ignored(r.Path, r.Dir)
		row.Children(func() {
			if r.Dir {
				turn := float32(0)
				if v.tree.Expanded(r.Path) {
					turn = 90
				}
				ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(t.TextMuted).Rotate(turn).Shrink(0)
				icon, color := dirIcon(t, r.Name)
				if r.Link != "" {
					icon, color = iconFolderLink, t.Accent
				}
				ui.Icon(c, icon).FontSize(iconSize).TextColor(color).Shrink(0)
			} else {
				ui.Box(c).Width(12).Shrink(0)
				if r.Link != "" {
					ui.Icon(c, iconFileLink).FontSize(iconSize).TextColor(t.TextMuted).Shrink(0)
				} else {
					fileGlyph(c, r.Name, iconSize)
				}
			}
			name := ui.Text(c, r.Name).FontSize(fsBody).SingleLine().Shrink(1).Grow(1)
			if mark, ok := marks[r.Path]; ok && !ignored {
				name.TextColor(letterColor(c, mark))
				if !r.Dir {
					name.Tooltip(treeMarkText[mark])
				}
			}
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
		if r.Link != "" {
			row.Tooltip("符号链接 → " + r.Link)
		}
		if row.Clicked() {
			v.reveal = ""
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
	contextMenu(c, e, func(m *popMenu) {
		if m.Item("复制路径").Chosen() {
			c.WriteClipboard(path)
		}
		if rel, ok := relInside(v.root, path); ok && m.Item("复制相对路径").Chosen() {
			c.WriteClipboard(rel)
		}
		m.Separator()
		if m.Item("在访达中显示").Icon(iconFolder).Chosen() {
			mygo.Shell.ShowItemInFolder(path)
		}
		if m.Item("用默认应用打开").Chosen() {
			if err := mygo.Shell.OpenPath(path); err != nil {
				a.err = errtext.Text(err)
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
	ui.Column(c).Padding(0, 12, 8, 12).Gap(6).Shrink(0).Children(func() {
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
			ui.Text(c, msg).FontSize(fsSmall).TextColor(color).LineHeight(1.4).Padding(2, 2, 0, 2)
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
			row := ui.Row(c).Padding(8, 12, 4, 16).Gap(6).AlignItems(ui.Center)
			row.Children(func() {
				fileGlyph(c, h.Path, iconSize)
				ui.Text(c, filepath.Base(h.Path)).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Shrink(0)
				if dir := filepath.Dir(h.Path); dir != "." {
					ui.Text(c, dir).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprint(r.count)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			a.pathMenu(c, row, w, v, abs, false)
			return
		}
		row := ui.Row(c.Key(i)).Margin(2, 6, 0, 6).Padding(4, 8, 4, 8).Gap(8).Radius(rRow).AlignItems(ui.Start).Role(ui.RoleButton)
		selectedBg(c, row, false)
		text, spans := trimHit(h)
		row.Children(func() {
			ui.Text(c, fmt.Sprint(h.Line+1)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Width(sz(30)).Shrink(0).TextAlign(ui.End)
			ui.RichText(c, markSpans(text, spans, ui.Span{Font: monoFont, Size: fsSmall}, colors(t).mark)...).SingleLine().Shrink(1)
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
	b := ui.Box(c.Key("chip-"+label)).Height(sz(22)).Padding(0, 8).Radius(rSmall).Center().Shrink(0).
		Role(ui.RoleButton).Label(tip).Tooltip(tip).Checked(on)
	bg, fg := ui.Transparent, t.TextMuted
	switch {
	case on:
		bg, fg = colors(t).tint, t.Accent
	case b.Hovered():
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() { ui.Text(c, label).FontSize(fsSmall).TextColor(fg) })
	return b.Clicked()
}

// --- 打开的文件

// docArea 画一栏里打开的文件 d。文件树、检索和改动在右边的文件栏里。
func (a *app) docArea(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	if d.web != nil {
		if d.web.view == nil {
			docMessage(c, d, d.err)
		} else {
			a.webView(c, d)
		}
		return
	}
	// diff 读的时候 diff 栏照样画，连按 F7 换文件时不闪
	if d.kind == docLoading && d.diff == nil {
		ui.Text(c, "正在读取…").FontSize(fsSmall).TextColor(t.TextMuted).Padding(16, 16)
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
			ui.Text(c, diffLoadingText(d.diff)).FontSize(fsSmall).TextColor(t.TextMuted).Padding(16, 16)
		case docDiff:
			a.diffView(c, v, d)
		case docImageDiff:
			imageDiffView(c, d)
		case docBinary:
			docMessage(c, d, "二进制文件，不显示 diff")
		case docTooLarge:
			a.tooLargeDiffView(c, v, d)
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
		if d.mdPreview() {
			a.markdownPreview(c, w, d)
			return
		}
		d.ed.Font, d.ed.LineHeight, d.ed.BoldWeight = a.editorFont(), a.state.EditorLineHeight, a.editorBoldWeight()
		d.ed.Bindings = a.editorBindings()
		d.ed.OnChangeClick = func(i int, _ float32) { d.change = &changePop{index: i} }
		d.ed.OnDefinition = func(off int) { a.goToDefinition(w, v, d, off) }
		d.ed.LinkAt = func(off int) (int, int) { return linkRange(d, off) }
		// 包一层：改动卡片按行的位置浮在编辑器上面
		ui.Box(c).Grow(1).Children(func() {
			el := editor.View(c.Key("ed:"+d.path), &d.ed).Ref(&d.edRef).Fill()
			// 分栏时只有焦点栏自动拿焦点：别的栏的编辑器一出现就拿走焦点，焦点栏会跟着换过去
			if a.docFocusPane {
				el.AutoFocus()
			}
			if d.focus {
				el.Focus()
				d.focus = false
			}
			a.changePopup(c, w, v, d)
			if v.def.running && v.def.doc == d {
				fadeHint(c, "doc-hint", "正在查找 "+v.def.word+" 的定义…", time.Time{}, 12)
			} else {
				fadeHint(c, "doc-hint", d.hint, d.hintAt, 12)
			}
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
		a.imageDocView(c, d)
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
	} else if s[0] > 0 {
		// 只给了列（跳回定义跳转之前的位置）：光标放到那一列
		off := min(snap.LineStart(line)+s[0], snap.LineEnd(line))
		d.ed.Select(off, off)
	}
	d.gotoLine, d.gotoSpan = 0, [2]int{}
}

func (a *app) filesEmpty(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
		tile(c, iconFile, 48, colors(t).tint, t.Accent)
		titleText(c, "在右边的文件栏里选一个文件", fsTitle)
		ui.Row(c).Gap(12).Children(func() {
			shortcutHint(c, a.keyText("quickOpen"), "按名称打开")
			shortcutHint(c, a.keyText("searchFiles"), "在文件里检索")
			shortcutHint(c, a.keyText("filesPanel"), "文件栏")
		})
	})
}

// docDetail 是文件标签名字后面的小字：diff 的类型、分支比较，同名文件（names 是各名字打开了几个）带上级目录名。
func docDetail(d *fileDoc, names map[string]int) string {
	switch {
	case d.diff != nil:
		return d.diff.tabLabel()
	case d.comp != nil:
		return "比较"
	case names[d.name()] > 1:
		return filepath.Base(filepath.Dir(d.path))
	}
	return ""
}

// docIcon 是文件标签的图标：分支比较用分支图标，网页用地球，其余（包括 diff）按文件类型。
func docIcon(t *ui.Theme, d *fileDoc) (*ui.SVG, ui.Color) {
	switch {
	case d.comp != nil:
		return iconBranch, t.TextMuted
	case d.web != nil:
		return iconGlobe, t.TextMuted
	}
	return fileIcon(t, d.path)
}

// docTab 是第 i 栏标签栏里的一个文件，样子和终端标签一样；active 是这栏选中的，focused 表示这栏是焦点栏。
func (a *app) docTab(c *ui.Context, w *workspace, v *filesView, d *fileDoc, detail string, i int, active, focused bool) {
	t := c.Theme()
	tip := shortPath(d.path)
	if d.web != nil {
		tip = d.web.url
	}
	// 文件名短，不跟着收缩：放不下时先缩终端标签里的标题
	row := ui.Row(c.Key(docKey(d))).Height(sz(28)).Padding(0, 4, 0, 8).Gap(6).Radius(rControl).Shrink(0).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(d.name()).Tooltip(tip)
	hovered := row.Hovered()
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case hovered:
		bg = colors(t).wash
	}
	a.revealTab(row, i, docKey(d), active)
	if active && focused && a.splitShown() {
		tabMark(c, row)
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	modified := d.isModified()
	row.Children(func() {
		svg, color := docIcon(t, d)
		ui.Icon(c, svg).FontSize(smallIcon()).TextColor(color).Shrink(0)
		name := ui.Text(c, d.name()).FontSize(fsSmall).SingleLine().MaxWidth(180)
		tabText(c, name, active)
		if d.isPreview() {
			name.Italic()
		}
		if d.deleted {
			name.Strikethrough()
		}
		if detail != "" {
			ui.Text(c, detail).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().MaxWidth(100)
		}
		// 未保存时显示圆点，指上去换成关闭按钮，和常见编辑器一样
		btn := ui.Box(c.Key("close")).Size(miniBox(), miniBox()).Radius(rSmall).Center().Shrink(0).Role(ui.RoleButton).Label("关闭文件").Tooltip(a.withKey("关闭", "closeTab"))
		if btn.Hovered() {
			btn.Background(t.SurfacePressed)
		}
		btn.Children(func() {
			switch {
			case modified && !hovered:
				dot(c, t.Accent)
			case active || hovered:
				ui.Icon(c, iconClose).FontSize(fsSmall).TextColor(t.TextMuted)
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
	contextMenu(c, row, func(m *popMenu) { a.docTabMenu(c, m, v, d) })
	a.reorderableTab(c, row, w, docKey(d), i)
	if row.Clicked() && !(active && focused) {
		a.selectKey(w, docKey(d), true)
	}
}

// docTabMenu 是文件标签的右键菜单（照 Orca）：关闭这个、其他的、右边的、全部文件，复制路径和在访达中显示。
// 有未保存的文件时先问一次。
func (a *app) docTabMenu(c *ui.Context, m *popMenu, v *filesView, d *fileDoc) {
	i := slices.Index(v.docs, d)
	if i < 0 {
		return
	}
	if m.Item("关闭").Keys(a.keyText("closeTab")).Chosen() {
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
	if w := a.workspace(v.wsID); w != nil {
		m.Separator()
		a.columnMenu(m, w, docKey(d))
	}
	if d.virtual() {
		return
	}
	m.Separator()
	if m.Item("复制路径").Chosen() {
		c.WriteClipboard(d.path)
	}
	// 别的工作区里的文件只给绝对路径：相对这里的 ../.. 路径贴给 agent 只会让它找错
	if rel, ok := relInside(v.root, d.path); ok && m.Item("复制相对路径").Chosen() {
		c.WriteClipboard(rel)
	}
	if m.Item("在访达中显示").Icon(iconFolder).Chosen() {
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
	ui.Row(c).Height(sz(30)).Shrink(0).Padding(0, 8, 0, 16).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, rel).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1).Selectable()
		if d.readOnly {
			ui.Text(c, "只读").FontSize(fsSmall).TextColor(t.TextMuted).Padding(2, 6).Radius(rSmall).Background(t.Surface).Shrink(0).
				Tooltip(fmt.Sprintf("文件超过 %d MB，只读打开", files.ReadOnlySize>>20))
		}
		ui.Spacer(c)
		if d.kind == docImage {
			a.imageBar(c, d)
		}
		if d.kind == docText && isMarkdown(d.path) {
			// 图标是点了之后去的那一边：预览时显示源码，源码时显示预览
			icon, label := iconEye, "预览"
			if d.preview {
				icon, label = iconCode, "查看源码"
			}
			if pressable(iconButtonSized(c, icon, label, compactBox(), smallIcon())).Clicked() {
				d.preview = !d.preview
			}
		}
		if d.kind == docText && !d.readOnly && d.blame == nil && isJSON(d.path) {
			if pressable(iconButtonSized(c, iconBraces, a.withKey("格式化 JSON", "formatJSON"), compactBox(), smallIcon())).Clicked() {
				a.formatJSON(d)
			}
		}
		if d.kind == docText && !d.readOnly && d.isModified() {
			if pressable(iconButtonSized(c, iconSave, a.withKey("保存", "save"), compactBox(), smallIcon())).Clicked() {
				a.saveDoc(d, false)
			}
		}
		if d.kind == docText && v.git.relPath(d.path) != "" {
			if pressable(iconButtonSized(c, iconHistory, "文件历史", compactBox(), smallIcon())).Clicked() {
				a.openLog(w, "", v.git.relPath(d.path))
			}
			if d.blame != nil {
				if pressable(textButton(c, "关闭 Annotate")).Clicked() {
					d.blame = nil
				}
			} else if pressable(iconButtonSized(c, iconBlame, "Annotate：逐行看是谁改的", compactBox(), smallIcon())).Clicked() {
				a.openBlame(w, v, d.path)
			}
			if e := v.git.entry(d.path); e != nil && !e.Conflict {
				if pressable(iconButtonSized(c, iconRollback, "回滚这个文件…", compactBox(), smallIcon())).Clicked() {
					a.rollbackDoc(v, d)
				}
			}
		}
		if revealable(v, d) {
			if pressable(iconButtonSized(c, iconLocate, a.withKey("在文件树中定位", "revealFile"), compactBox(), smallIcon())).Clicked() {
				a.revealCurrent(w)
			}
		}
		if pressable(iconButtonSized(c, iconFolder, "在访达中显示", compactBox(), smallIcon())).Clicked() {
			mygo.Shell.ShowItemInFolder(d.path)
		}
		if pressable(iconButtonSized(c, iconCopy, "复制路径", compactBox(), smallIcon())).Clicked() {
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
	ui.Row(c).Shrink(0).Margin(0, 12, 8, 12).Padding(8, 8, 8, 12).Gap(8).Radius(rControl).Background(t.Surface).AlignItems(ui.Center).Children(func() {
		ui.Text(c, msg).FontSize(fsSmall).LineHeight(1.4).Grow(1).Shrink(1)
		if actions != nil {
			actions()
		}
	})
}

func docMessage(c *ui.Context, d *fileDoc, msg string) {
	t := c.Theme()
	ui.Column(c).Grow(1).AlignSelf(ui.Stretch).Center().Gap(12).Children(func() {
		ui.Text(c, msg).FontSize(fsBody).TextColor(t.TextMuted)
		// diff、日志这些不是磁盘上的文件（路径是 diff:…、log:…），没有可打开的
		if filepath.IsAbs(d.path) && (d.kind != docError || !d.deleted) {
			if pressable(textButton(c, "用默认应用打开")).Clicked() {
				mygo.Shell.OpenPath(d.path)
			}
		}
	})
}

// finding 报告 docFind 的查找栏开着、正显示在 Markdown 预览或 Annotate 上。
func (d *fileDoc) finding() bool {
	return d.find.open && (d.mdPreview() || d.diff == nil && d.comp == nil && d.kind == docText && d.blame != nil)
}

// mdPreview 报告这个文件显示成 Markdown 预览：搭界面和 ⌘= 调哪个字号都按它。
func (d *fileDoc) mdPreview() bool {
	return d.kind == docText && d.diff == nil && d.comp == nil && d.blame == nil && d.preview && isMarkdown(d.path)
}

// frontMarkdown 报告文件标签在前台、显示的是 Markdown 预览。
func (a *app) frontMarkdown(w *workspace) bool {
	if !a.filesActive(w) {
		return false
	}
	d := a.filesFor(w).cur
	return d != nil && d.mdPreview()
}

func isMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}

// markdownPreview 用历史页的 Markdown 渲染显示文档，只在内容变了之后重新解析。
// 版心随字号等比放宽，放大后一行的字数不变。
func (a *app) markdownPreview(c *ui.Context, w *workspace, d *fileDoc) {
	if v := d.ed.Buffer.Version(); d.md == nil || v != d.mdVer {
		d.md, d.mdVer = parseMarkdown(d.ed.Buffer.String()), v
	}
	f := &d.find
	f.shortcuts(c, a)
	ui.Box(c).Grow(1).Children(func() {
		f.begin()
		ui.Scroll(c.Key("md:" + d.path)).Fill().TrackScroll(&d.mdScroll).Children(func() {
			ui.Row(c).Padding(20, 24, 32, 24).Justify(ui.Center).Children(func() {
				size := a.mdSize()
				ui.Column(c).Grow(1).Shrink(1).MaxWidth(820 * size / defaultUIFontSize).Selectable().Children(func() {
					mdView{find: f, size: size, doc: true, wiki: func(target string) { a.openWiki(w, d.path, target) }}.page(c, d.md)
				})
			})
		})
		f.end()
		f.bar(c, a)
	})
}

// openWiki 打开 Markdown 里 [[名字]] 指向的文件：相对当前文件所在的目录，没写扩展名的补 .md，#后面的标题不管。
func (a *app) openWiki(w *workspace, from, target string) {
	name, _, _ := strings.Cut(target, "#")
	p := filepath.Join(filepath.Dir(from), name)
	if filepath.Ext(name) != ".md" {
		if _, err := os.Stat(p); err != nil {
			p += ".md"
		}
	}
	if _, err := os.Stat(p); err != nil {
		a.err = "找不到 [[" + target + "]] 指向的文件：" + p
		return
	}
	a.openFile(w, p, 0, [2]int{})
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
		ui.Column(c).Width(560).Gap(8).Children(func() {
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
				ui.Text(c, "正在列出文件…").FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			case n == 0:
				ui.Text(c, "没有匹配的文件").FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			default:
				ui.Scroll(c).MaxHeight(380).Children(func() {
					ui.Column(c).Gap(2).Children(func() {
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
	row := ui.Row(c.Key(m.Path)).Height(sz(30)).Margin(2, 0, 0, 0).Padding(0, 12).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(m.Path)
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
	mark := colors(t).mark
	row.Children(func() {
		fileGlyph(c, m.Path, fsBody)
		ui.RichText(c, markSpans(m.Path[base:], nameMarks, ui.Span{Size: fsBody}, mark)...).SingleLine().Shrink(0)
		if base > 0 {
			dir := m.Path[:base-1]
			ui.RichText(c, markSpans(dir, clampMarks(dirMarks, len(dir)), ui.Span{Size: fsSmall, Font: monoFont, Color: t.TextMuted}, mark)...).SingleLine().Shrink(1)
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
