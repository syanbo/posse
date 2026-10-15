package app

import (
	"context"
	"fmt"
	"path/filepath"
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
	if len(g.rows) == 0 {
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, "没有改动").FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		})
	} else {
		rows := g.rows
		g.list.Key = func(i int) any {
			r := rows[i]
			return [3]any{r.group, r.header, r.entry.Path + r.stash.Ref}
		}
		ui.List(c, &g.list, len(rows), func(i int) { a.changeRow(c, w, v, rows[i]) }).Grow(1).Padding(0, 0, 8, 0)
	}
	a.commitBox(c, v)
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
		if st.Ahead > 0 {
			ui.Text(c, fmt.Sprintf("↑%d", st.Ahead)).FontSize(11).FontFeatures("tnum").TextColor(colors(t).sage).Tooltip(fmt.Sprintf("有 %d 个提交还没推送", st.Ahead)).Shrink(0)
		}
		if st.Behind > 0 {
			ui.Text(c, fmt.Sprintf("↓%d", st.Behind)).FontSize(11).FontFeatures("tnum").TextColor(t.Accent).Tooltip(fmt.Sprintf("远端有 %d 个提交还没拉取", st.Behind)).Shrink(0)
		}
		ui.Spacer(c)
		if g.busy != "" {
			ui.Text(c, g.busy+"…").FontSize(11).TextColor(t.TextMuted).Shrink(0)
		}
		if pressable(iconButtonSized(c, iconHistory, "Git 日志", 24, 14)).Clicked() {
			a.openLog(w, "", "")
		}
		if pressable(iconButtonSized(c, iconRefresh, "刷新", 24, 14)).Clicked() {
			a.refreshGit(v)
		}
		if g.changeCount() > 0 && pressable(iconButtonSized(c, iconShelve, "搁置全部改动（git stash，含未跟踪文件）", 24, 14)).Clicked() {
			a.gitDo(v, "搁置", func(ctx context.Context, root string) error { return gitx.StashPush(ctx, root, "") })
		}
	})
}

func statusLetter(e gitx.Entry, group changeGroup) byte {
	switch {
	case e.Untracked:
		return '?'
	case e.Conflict:
		return 'U'
	case group == groupStaged:
		return e.X
	}
	return e.Y
}

// changeRow 画改动列表的一行。
func (a *app) changeRow(c *ui.Context, w *workspace, v *filesView, r changeRow) {
	t := c.Theme()
	p := colors(t)
	g := &v.git
	if r.header {
		ui.Row(c).Height(26).Padding(6, 16, 0, 16).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, groupTitles[r.group]).FontSize(11).FontWeight(600).TextColor(t.TextMuted)
			ui.Text(c, fmt.Sprint(r.count)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
		})
		return
	}
	if r.group == groupStash {
		a.stashRow(c, v, r.stash)
		return
	}
	e := r.entry
	staged := r.group == groupStaged
	spec := changeSpec(e, staged)
	key := diffKey(g.absPath(e.Path), spec)
	row := ui.Row(c.Key(key)).Height(26).Margin(0, 6).Padding(0, 8, 0, 10).Gap(6).Radius(6).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(e.Path)
	selectable(c, row, v.cur != nil && v.cur.path == key)
	letter := statusLetter(e, r.group)
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
		ui.Text(c, string(letter)).FontSize(11).Font(monoFont).FontWeight(600).TextColor(color).Width(10).Shrink(0)
		name := ui.Text(c, filepath.Base(e.Path)).FontSize(12.5).SingleLine().Shrink(0)
		if letter == 'D' {
			name.Strikethrough().TextColor(t.TextMuted)
		}
		if dir := filepath.Dir(e.Path); dir != "." {
			ui.Text(c, dir).FontSize(11).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		stats := g.unstaged
		if staged {
			stats = g.staged
		}
		if s, ok := stats[e.Path]; ok && !s.Binary && r.group != groupUntracked {
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
		case staged:
			if m.Item("取消暂存").Chosen() {
				a.gitDo(v, "取消暂存", func(ctx context.Context, root string) error { return gitx.Unstage(ctx, root, e.Path) })
			}
		case e.Untracked:
			if m.Item("加入 Git").Chosen() {
				a.gitDo(v, "加入 Git", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
			}
		case e.Conflict:
			if m.Item("标记为已解决").Chosen() {
				a.gitDo(v, "标记为已解决", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
			}
		default:
			if m.Item("暂存").Chosen() {
				a.gitDo(v, "暂存", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
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
	row := ui.Row(c.Key("stash:"+s.Ref)).Height(26).Margin(0, 6).Padding(0, 8, 0, 10).Gap(6).Radius(6).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(s.Message)
	selectable(c, row, false)
	row.Children(func() {
		ui.Icon(c, iconShelve).FontSize(13).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, stashTitle(s.Message)).FontSize(12).SingleLine().Shrink(1).Grow(1).Tooltip(s.Message)
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
	label, all := g.commitMode()
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
			btn := pressable(accentButton(c, label)).Tooltip(commitTip(all) + " ⌘K")
			if g.busy != "" || g.changeCount() == 0 && !g.amend {
				btn.Disabled(true)
			} else if btn.Clicked() || msg.Shortcut(ui.Cmd, ui.KeyEnter) {
				a.commit(v)
			}
		})
	})
}

func commitTip(all bool) string {
	if all {
		return "提交所有已跟踪文件的改动；未跟踪的文件要先加入 Git"
	}
	return "只提交已暂存的文件"
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
	if c.Shortcut(ui.Cmd, ui.KeyK) && v.git.repo != "" {
		v.side, v.git.focusMsg = sideChanges, true
	}
}
