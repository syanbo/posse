package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/gitx"
)

// changesView 是文件标签左栏的"改动"：分支和同步状态、改动列表（含搁置），底部是提交框。
func (a *app) changesView(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	g := &v.git
	switch {
	case !g.checked:
		ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		return
	case g.repo == "":
		ui.Text(c, "这个工作区不是 git 仓库").FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		return
	case g.status == nil:
		msg := "正在读取…"
		if g.err != "" {
			msg = g.err
		}
		ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		return
	}
	a.changesHeader(c, w, v)
	a.operationBanner(c, w, v)
	// 分页：提交看文件改动和提交框，搁置看 stash
	var files, stashes []changeRow
	for _, r := range g.rows {
		switch {
		case r.group != groupStash:
			files = append(files, r)
		case !r.header:
			stashes = append(stashes, r)
		}
	}
	ui.Row(c).Gap(4).Padding(2, 10, 6, 10).AlignItems(ui.Center).Children(func() {
		changesTab(c, &g.tab, 0, "提交", g.changeCount())
		changesTab(c, &g.tab, 1, "搁置", len(stashes))
	})
	rows, list, empty := files, &g.list, "没有改动"
	if g.tab == 1 {
		rows, list, empty = stashes, &g.stashLst, "没有搁置的改动"
	}
	if len(rows) == 0 {
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, empty).FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		})
	} else {
		list.Key = func(i int) any {
			r := rows[i]
			return [3]any{r.group, r.header, r.entry.Path + r.stash.Ref}
		}
		ui.List(c, list, len(rows), func(i int) { a.changeRow(c, w, v, rows[i]) }).Grow(1).Padding(0, 0, 8, 0)
	}
	if g.tab == 0 {
		a.commitBox(c, v)
	}
}

// changesTab 是改动页的一个分页：文字加个数，选中的垫一层填充底。
func changesTab(c *ui.Context, sel *int, i int, label string, n int) {
	t := c.Theme()
	active := *sel == i
	row := ui.Row(c.Key(label)).Height(26).Padding(0, 10).Gap(5).Radius(6).AlignItems(ui.Center).
		Role(ui.RoleTab).Label(label).Checked(active)
	bg := ui.Transparent
	switch {
	case active:
		bg = colors(t).fill
	case row.Hovered():
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		text := ui.Text(c, label).FontSize(12.5)
		if active {
			text.FontWeight(500)
		} else {
			text.TextColor(t.TextMuted)
		}
		if n > 0 {
			ui.Text(c, strconv.Itoa(n)).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted)
		}
	})
	if row.Clicked() {
		*sel = i
	}
}

// changesHeader 是改动列表上方一行：分支、领先和落后的提交数，以及刷新、搁置。
func (a *app) changesHeader(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	g := &v.git
	st := g.status
	ui.Row(c).Height(30).Shrink(0).Padding(0, 6, 0, 16).Gap(4).AlignItems(ui.Center).Children(func() {
		head := st.Head
		if head == "" {
			head = "分离的 HEAD"
		}
		ui.Icon(c, iconBranch).FontSize(13).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, head).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		trackBadge(c, st.Ahead, st.Behind, true)
		ui.Spacer(c)
		if g.busy != "" {
			ui.Text(c, g.busy+"…").FontSize(11).TextColor(t.TextMuted).Shrink(0)
		}
		if pressable(iconButtonSized(c, iconGitLog, "Git 日志", 28, 16)).Clicked() {
			a.openLog(w, "", "")
		}
		if pressable(iconButtonSized(c, iconRefresh, "刷新", 28, 16)).Clicked() {
			a.refreshGit(v)
		}
		if g.changeCount() > 0 && pressable(iconButtonSized(c, iconShelve, "搁置全部改动（git stash，含未跟踪文件）", 28, 16)).Clicked() {
			a.gitDo(v, "搁置", func(ctx context.Context, root string) error { return gitx.StashPush(ctx, root, "") })
		}
	})
}

func statusLetter(e gitx.Entry) byte {
	switch {
	case e.Untracked:
		return '?'
	case e.Conflict:
		return 'U'
	case e.Y != '.':
		return e.Y
	}
	return e.X
}

// changeRow 画改动列表的一行。
func (a *app) changeRow(c *ui.Context, w *workspace, v *filesView, r changeRow) {
	t := c.Theme()
	p := colors(t)
	g := &v.git
	if r.header {
		ui.Row(c).Height(26).Padding(6, 16, 0, 16).Gap(6).AlignItems(ui.Center).Children(func() {
			// 改动和未跟踪的组标题能一键全选、全不选
			if r.group == groupChanged || r.group == groupUntracked {
				var in []gitx.Entry
				on := 0
				for _, e := range g.status.Entries {
					if !e.Conflict && e.Untracked == (r.group == groupUntracked) {
						in = append(in, e)
						if g.picked(e) {
							on++
						}
					}
				}
				state := pickNone
				switch {
				case on == len(in):
					state = pickAll
				case on > 0:
					state = pickSome
				}
				if pickBox(c, fmt.Sprint("group-", r.group), groupTitles[r.group], state) {
					for _, e := range in {
						g.setPicked(e, state != pickAll)
					}
				}
			}
			ui.Text(c, groupTitles[r.group]).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
			ui.Text(c, fmt.Sprint(r.count)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
		})
		return
	}
	if r.group == groupStash {
		a.stashRow(c, v, r.stash)
		return
	}
	e := r.entry
	spec := changeSpec(e)
	key := diffKey(g.absPath(e.Path), spec)
	// 比组标题缩进一层：勾选框和组标题的文字对齐
	row := ui.Row(c.Key(key)).Height(28).Margin(0, 6).Padding(0, 8, 0, 30).Gap(6).Radius(6).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(e.Path)
	selectable(c, row, v.cur != nil && v.cur.path == key)
	letter := statusLetter(e)
	color := t.Accent
	switch letter {
	case 'A', '?':
		color = p.sage
	case 'D':
		color = t.TextMuted
	case 'U':
		color = t.Danger
	}
	row.Children(func() {
		if !e.Conflict {
			state := pickNone
			if g.picked(e) {
				state = pickAll
			}
			if pickBox(c, "pick", e.Path, state) {
				g.setPicked(e, state != pickAll)
			}
		}
		ui.Text(c, string(letter)).FontSize(11).Font(monoFont).FontWeight(500).TextColor(color).Width(10).Shrink(0)
		name := ui.Text(c, filepath.Base(e.Path)).FontSize(fsBody).SingleLine().Shrink(0)
		if letter == 'D' {
			name.Strikethrough().TextColor(t.TextMuted)
		}
		if dir := filepath.Dir(e.Path); dir != "." {
			ui.Text(c, dir).FontSize(11).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		if s, ok := g.head[e.Path]; ok && !s.Binary && r.group != groupUntracked {
			if s.Added > 0 {
				ui.Text(c, fmt.Sprintf("+%d", s.Added)).FontSize(11).FontFeatures("tnum").TextColor(p.sage).Shrink(0)
			}
			if s.Deleted > 0 {
				ui.Text(c, fmt.Sprintf("−%d", s.Deleted)).FontSize(11).FontFeatures("tnum").TextColor(t.Danger).Shrink(0)
			}
		}
	})
	if row.Clicked() {
		if e.Conflict {
			a.openFile(w, g.absPath(e.Path), 0, [2]int{})
		} else {
			a.openDiff(w, spec, nil, true)
		}
	}
	row.ContextMenu(func(m *ui.Menu) {
		if !e.Conflict && m.Item("显示 diff").Chosen() {
			a.openDiff(w, spec, nil, true)
		}
		if letter != 'D' && m.Item("打开文件").Chosen() {
			a.openFile(w, g.absPath(e.Path), 0, [2]int{})
		}
		m.Separator()
		switch {
		case e.Untracked:
			if m.Item("加入 Git").Chosen() {
				a.gitDo(v, "加入 Git", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
			}
		case e.Conflict:
			ours, theirs := sideNames(g.status.Operation)
			if m.Item("采用" + ours).Chosen() {
				a.gitDo(v, "解决冲突", func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, e.Path, true) })
			}
			if m.Item("采用" + theirs).Chosen() {
				a.gitDo(v, "解决冲突", func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, e.Path, false) })
			}
			if m.Item("改完了，标记为已解决").Chosen() {
				a.gitDo(v, "标记为已解决", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
			}
		}
		if !e.Conflict {
			label := "回滚…"
			if e.Untracked {
				label = "删除…"
			}
			if m.Item(label).Shortcut(ui.Cmd|ui.Alt, ui.KeyZ).Chosen() {
				a.askRollback(v, []gitx.Entry{e})
			}
		}
		m.Separator()
		abs := g.absPath(e.Path)
		if letter != 'D' && m.Item("在访达中显示").Chosen() {
			mygo.Shell.ShowItemInFolder(abs)
		}
		if m.Item("复制路径").Chosen() {
			c.WriteClipboard(abs)
		}
	})
}

func (a *app) stashRow(c *ui.Context, v *filesView, s gitx.Stash) {
	t := c.Theme()
	row := ui.Row(c.Key("stash:"+s.Ref)).Height(28).Margin(0, 6).Padding(0, 8, 0, 10).Gap(6).Radius(6).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(s.Message)
	selectable(c, row, false)
	row.Children(func() {
		ui.Icon(c, iconShelve).FontSize(14).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, stashTitle(s.Message)).FontSize(13).SingleLine().Shrink(1).Grow(1).Tooltip(s.Message)
		ui.Text(c, ago(s.When)).FontSize(11).TextColor(t.TextMuted).Shrink(0)
	})
	row.Menu(func(m *ui.Menu) {
		if m.Item("取回改动").Chosen() {
			a.gitDo(v, "取回搁置", func(ctx context.Context, root string) error { return gitx.StashPop(ctx, root, s.Ref) })
		}
		if m.Item("删除…").Chosen() {
			a.confirm("删除这次搁置？", "搁置的改动会丢失："+s.Message, "删除", func() {
				a.update(func() {
					a.gitDo(v, "删除搁置", func(ctx context.Context, root string) error { return gitx.StashDrop(ctx, root, s.Ref) })
				})
			})
		}
	})
}

var opNames = map[string]string{"rebase": "rebase", "merge": "合并", "cherry-pick": "cherry-pick", "revert": "revert"}

// sideNames 是冲突两边在这个操作里各是什么：rebase 时 ours 是要 rebase 到的分支，theirs 是你正在重放的提交。
func sideNames(op string) (ours, theirs string) {
	if op == "rebase" {
		return "目标分支的（ours）", "你的提交的（theirs）"
	}
	return "当前分支的（ours）", "合入的提交的（theirs）"
}

// operationBanner 是 rebase、合并等操作进行到一半时的横幅：冲突数，继续、跳过、中止。
func (a *app) operationBanner(c *ui.Context, w *workspace, v *filesView) {
	g := &v.git
	op := g.status.Operation
	if op == "" {
		return
	}
	t := c.Theme()
	conflicts := 0
	for _, e := range g.status.Entries {
		if e.Conflict {
			conflicts++
		}
	}
	msg := "正在" + opNames[op] + "，没有冲突了，可以继续。"
	if conflicts > 0 {
		msg = fmt.Sprintf("正在%s，有 %d 个文件冲突。解决后点继续：右键冲突文件可以整个采用一方，或者打开文件改完后标记为已解决。", opNames[op], conflicts)
	}
	ui.Column(c).Shrink(0).Margin(2, 10, 8, 10).Padding(10, 12).Gap(8).Radius(8).Background(t.Surface).Children(func() {
		ui.Text(c, msg).FontSize(12).LineHeight(1.45)
		ui.Row(c).Gap(6).Children(func() {
			cont := pressable(accentButton(c, "继续"))
			if conflicts > 0 {
				cont.Disabled(true)
			} else if cont.Clicked() {
				a.gitDo(v, "继续"+opNames[op], func(ctx context.Context, root string) error { return gitx.Continue(ctx, root, op) })
			}
			if (op == "rebase" || op == "cherry-pick") && pressable(textButton(c, "跳过这个提交")).Clicked() {
				a.gitDo(v, "跳过", func(ctx context.Context, root string) error { return gitx.Skip(ctx, root, op) })
			}
			if pressable(textButton(c, "中止")).Clicked() {
				a.confirm("中止"+opNames[op]+"？", "会回到开始"+opNames[op]+"之前的样子，已经解决的冲突也会丢掉。", "中止", func() {
					a.update(func() {
						a.gitDo(v, "中止", func(ctx context.Context, root string) error { return gitx.Abort(ctx, root, op) })
					})
				})
			}
		})
	})
}

// stashTitle 去掉 git 加在搁置说明前面的 "On <分支>: " 或 "WIP on <分支>: "。
func stashTitle(msg string) string {
	for _, p := range []string{"WIP on ", "On "} {
		if rest, ok := strings.CutPrefix(msg, p); ok {
			if _, after, ok := strings.Cut(rest, ": "); ok {
				return after
			}
		}
	}
	return msg
}

// commitBox 是左栏底部的提交框：提交信息、修改上一次提交、提交按钮。
func (a *app) commitBox(c *ui.Context, v *filesView) {
	t := c.Theme()
	p := colors(t)
	g := &v.git
	_, _, picked := g.pickedPaths()
	label := fmt.Sprintf("提交 %d 个文件", picked)
	if picked == 0 && g.amend {
		label = "只改提交信息"
	}
	ui.Box(c).Height(1).Shrink(0).Background(p.line)
	ui.Column(c).Shrink(0).Padding(10).Gap(8).Children(func() {
		msg := ui.TextArea(c, &g.message).Placeholder("提交信息").Label("提交信息").Height(72).FontSize(12.5)
		if g.focusMsg {
			msg.Focus()
			g.focusMsg = false
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			amend := g.amend
			if ui.Checkbox(c, &amend, "修改上一次提交").Changed() {
				a.setAmend(v, amend)
			}
			ui.Spacer(c)
			btn := pressable(accentButton(c, label)).Tooltip("提交勾选的文件，没勾的改动留在工作区 ⌘K")
			if g.busy != "" || picked == 0 && !g.amend {
				btn.Disabled(true)
			} else if btn.Clicked() || msg.Shortcut(ui.Cmd, ui.KeyEnter) {
				a.commit(v)
			}
		})
	})
}

// --- diff 标签页

func (a *app) diffView(c *ui.Context, v *filesView, d *fileDoc) {
	df := d.diff
	df.state.Font = ui.Font{Family: termFontFamily, Size: a.fontSize - editorInset}
	df.state.HunkActions = df.worktree()
	if df.worktree() {
		df.state.RenderHunkActions = func(c *ui.Context, bar ui.Element, act *editor.DiffHunkActions) {
			t := c.Theme()
			bar.Row().Gap(4)
			bar.Children(func() {
				btn := ui.ButtonBase(c).Key("revert").Padding(0, 8).Height(20).Radius(5).Background(t.Surface).Border(1, t.Border).Label("回滚这处改动")
				btn.Children(func() { ui.Text(c, "回滚").FontSize(11) })
				if btn.Clicked() {
					a.revertHunk(v, d, act.Index)
				}
			})
		}
	}
	df.state.RenderSeparator = diffSeparator
	editor.DiffView(c.Key(d.path), &df.state).Grow(1)
	d.shown++
	if !df.jumped {
		// 第一帧还没有尺寸，跳到改动要等排过版
		if d.shown < 2 {
			c.AnimationFrame()
		} else {
			diffJump(df)
		}
	}
}

// diffSeparator 是 diff 里折叠的未改动行：展开按钮和"N 行未改动"，点文字全部展开。
func diffSeparator(c *ui.Context, sep ui.Element, s *editor.DiffSeparator) {
	t := c.Theme()
	sep.Padding(4)
	sep.Children(func() {
		ui.Row(c).Grow(1).AlignSelf(ui.Stretch).AlignItems(ui.Center).Gap(2).Padding(0, 6).Radius(6).Background(colors(t).wash).Children(func() {
			if s.Lines == 0 {
				ui.Text(c, s.Header).Font(monoFont).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
				return
			}
			if !s.First && pressable(iconButtonSized(c.Key("down"), iconDown, "向下展开", 20, 12)).Clicked() {
				s.ExpandDown()
			}
			if !s.Last && pressable(iconButtonSized(c.Key("up"), iconUp, "向上展开", 20, 12)).Clicked() {
				s.ExpandUp()
			}
			b := ui.Box(c.Key("all")).Padding(2, 6).Radius(4).Role(ui.RoleButton).Label("全部展开").Cursor(ui.CursorPointer)
			b.Children(func() { ui.Text(c, fmt.Sprintf("%d 行未改动", s.Lines)).FontSize(11.5).TextColor(t.TextMuted) })
			if b.Clicked() {
				s.ExpandAll()
			}
		})
	})
}

// diffBar 是 diff 上方的一行：路径、第几处改动、上一处 / 下一处、排版和忽略空白、在编辑器里打开。
func (a *app) diffBar(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	df := d.diff
	ui.Row(c).Height(30).Shrink(0).Padding(0, 8, 0, 14).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, df.rel).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1).Selectable()
		if label := df.tabLabel(); label != "改动" {
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).Padding(1, 6).Radius(4).Background(t.Surface).Shrink(0)
		}
		if d.kind == docDiff {
			n := len(df.state.Hunks())
			added, deleted := df.state.Stats()
			msg := "没有差异"
			if n > 0 {
				msg = fmt.Sprintf("第 %d / %d 处  +%d −%d", min(df.hunk+1, n), n, added, deleted)
			}
			ui.Text(c, msg).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0).Padding(0, 0, 0, 8)
		}
		ui.Spacer(c)
		if pressable(iconButtonSized(c, iconUp, "上一处改动 ⇧F7", 24, 14)).Clicked() {
			a.stepDiff(w, v, d, -1)
		}
		if pressable(iconButtonSized(c, iconDown, "下一处改动 F7", 24, 14)).Clicked() {
			a.stepDiff(w, v, d, 1)
		}
		// 排版和忽略空白只对文字的 diff 有用
		if d.kind != docImageDiff {
			split := df.state.Layout == editor.DiffSplit
			if toggleChip(c, "左右", "左右对照，关掉是上下合并", split) {
				if split {
					df.state.Layout = editor.DiffUnified
				} else {
					df.state.Layout = editor.DiffSplit
				}
			}
			if toggleChip(c, "忽略空白", "只差空白的行不算改动", df.state.IgnoreWhitespace) {
				df.state.IgnoreWhitespace = !df.state.IgnoreWhitespace
			}
		}
		if pressable(iconButtonSized(c, iconOpen, "在编辑器中打开 F4", 24, 14)).Clicked() {
			a.diffToEditor(w, v, d)
		}
	})
}

// diffToEditor 是 F4：在编辑器里打开文件，停在当前改动的那一行。
func (a *app) diffToEditor(w *workspace, v *filesView, d *fileDoc) {
	df := d.diff
	line := 1
	if hunks := df.state.Hunks(); df.hunk >= 0 && df.hunk < len(hunks) {
		line = hunks[df.hunk].NewStart + 1
	}
	a.openFile(w, v.git.absPath(df.rel), line, [2]int{})
}

// gitShortcuts 是文件标签里的 Git 快捷键，照 WebStorm：F7 / ⇧F7 跳改动，F4 打开文件，⌘K 提交，⌥⌘Z 回滚。
func (a *app) gitShortcuts(c *ui.Context, w *workspace, v *filesView) {
	d := v.cur
	if d != nil && d.diff != nil {
		switch {
		case c.Shortcut(0, ui.KeyF7):
			a.stepDiff(w, v, d, 1)
		case c.Shortcut(ui.Shift, ui.KeyF7):
			a.stepDiff(w, v, d, -1)
		case c.Shortcut(0, ui.KeyF4):
			a.diffToEditor(w, v, d)
		case c.Shortcut(ui.Cmd|ui.Alt, ui.KeyZ) && d.diff.worktree() && d.kind == docDiff:
			if hunks := d.diff.state.Hunks(); d.diff.hunk < len(hunks) {
				a.revertHunk(v, d, d.diff.hunk)
			}
		}
	}
}

const (
	pickNone = iota
	pickAll
	pickSome
)

// pickBox 是改动列表里的勾选框：空框、勾上（强调色加对勾）、部分勾上（强调色加横线）。返回这一帧是否点了它。
func pickBox(c *ui.Context, key, name string, state int) bool {
	t := c.Theme()
	on := state == pickAll
	box := ui.CheckboxBase(c.Key("pick:"+key), &on).Size(14, 14).Radius(4).Label("勾选 " + name)
	if state == pickSome {
		box.Mixed()
	}
	accent, line := t.Accent, t.TextMuted.Alpha(0.6)
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if state == pickNone {
			p.Stroke(r, line, 4, 1.2)
			return
		}
		p.Fill(r, accent, 4)
		white := ui.RGBA(255, 255, 255, 1)
		if state == pickSome {
			p.Fill(ui.Rect{X: r.X + 3.5, Y: r.Y + r.H/2 - 0.8, W: r.W - 7, H: 1.6}, white, 1)
			return
		}
		var path ui.Path
		path.MoveTo(r.X+3.5, r.Y+7.2).LineTo(r.X+6, r.Y+9.6).LineTo(r.X+10.5, r.Y+4.6)
		p.StrokePath(&path, 1.6, white)
	})
	return box.Changed()
}

// imageDiffView 是图片的 diff：左右两张图对照（照 WebStorm），各自标出版本、尺寸和文件大小；
// 透明的地方垫棋盘格，看得出哪里是透明的。
func imageDiffView(c *ui.Context, d *fileDoc) {
	t := c.Theme()
	df := d.diff
	newLabel := "现在（工作区）"
	switch {
	case !df.worktree():
		newLabel = "现在（" + shortRev(df.newRev) + "）"
	}
	oldLabel := "原来"
	if df.oldRev != "" {
		oldLabel += "（" + shortRev(df.oldRev) + "）"
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		for i, side := range df.images {
			if i == 1 {
				ui.Box(c).Width(1).Shrink(0).Background(colors(t).line)
			}
			label := oldLabel
			if i == 1 {
				label = newLabel
			}
			ui.Column(c.Key(fmt.Sprint("img-", i))).Grow(1).Shrink(1).Basis(0).Padding(12, 16).Gap(10).Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).FontSize(12.5).FontWeight(500)
					switch {
					case side.size < 0 && i == 0:
						ui.Text(c, "新文件").FontSize(11.5).TextColor(colors(t).sage)
					case side.size < 0:
						ui.Text(c, "已删除").FontSize(11.5).TextColor(t.Danger)
					case side.img != nil:
						ui.Text(c, fmt.Sprintf("%d×%d", side.w, side.h)).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted)
						ui.Text(c, byteSize(side.size)).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted)
					}
				})
				ui.Box(c).Grow(1).Center().Children(func() {
					switch {
					case side.err != "":
						ui.Text(c, side.err).FontSize(12).TextColor(t.TextMuted)
					case side.img != nil:
						// 位图按 2 倍屏解码，按一半的尺寸显示才是原大
						w, h := side.img.Size()
						checker := ui.RGBA(128, 128, 128, 0.18)
						ui.Box(c).Shrink(1).Draw(func(p *ui.Painter, r ui.Rect) {
							const sq = 8
							p.Clip(r, 0, func() {
								for y := float32(0); y < r.H; y += sq {
									for x := float32(0); x < r.W; x += sq {
										if (int(x/sq)+int(y/sq))%2 == 0 {
											p.Fill(ui.Rect{X: r.X + x, Y: r.Y + y, W: sq, H: sq}, checker, 0)
										}
									}
								}
							})
						}).Children(func() {
							ui.Image(c, side.img).Fit(ui.Contain).MaxWidth(float32(w) / 2).MaxHeight(float32(h) / 2).Shrink(1)
						})
					}
				})
			})
		}
	})
}

// byteSize 把字节数写成 B、KB、MB。
func byteSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
