package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/gitx"
	"posse/locales"
)

// changesView 是文件栏的"改动"：分支和同步状态、改动列表（含储藏），底部是提交框。
func (a *app) changesView(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	g := v.git
	switch {
	case !v.repos.scanned:
		ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	case g.repo == "":
		ui.Text(c, L(c).GitNotRepo()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	case g.status == nil:
		msg := L(c).CommonLoading()
		if g.err != "" {
			msg = g.err
		}
		ui.Text(c, msg).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	a.changesHeader(c, w, v)
	if v.repos.partial {
		ui.Text(c, L(c).GitReposPartial()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, 16, 6, 16)
	}
	a.baseRow(c, w, v)
	a.operationBanner(c, w, v)
	a.autostashBanner(c, v)
	// 分页：提交看文件改动和提交框，储藏看 stash（照 WebStorm 的"储藏"标签）
	var files, stashes []changeRow
	// 收起的组只留标题；F7 等仍按全部的 g.rows 走
	for _, r := range g.rows {
		switch {
		case r.group != groupStash && (r.header || *g.groupOpen(r.group)):
			files = append(files, r)
		case !r.header:
			stashes = append(stashes, r)
		}
	}
	if a.state.ChangesByDir {
		files = byDirRows(files, g.dirFolds)
	}
	ui.Row(c).Gap(4).Padding(2, 12, 6, 12).AlignItems(ui.Center).Children(func() {
		changesTab(c, &g.tab, 0, L(c).GitCommitButton(), g.changeCount())
		changesTab(c, &g.tab, 1, L(c).GitStash(), len(stashes))
		if g.tab == 0 {
			ui.Spacer(c)
			if toggleIconButton(c, "by-dir", iconFolder, L(c).GitGroupByDirectory(), a.state.ChangesByDir) {
				a.state.ChangesByDir = !a.state.ChangesByDir
				a.saveState()
			}
		}
	})
	rows, list, empty := files, &g.list, L(c).GitNoChanges()
	if g.tab == 1 {
		rows, list, empty = stashes, &g.stashLst, L(c).GitNoStashes()
	}
	if len(rows) == 0 {
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, empty).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		})
	} else {
		list.Key = func(i int) any {
			r := rows[i]
			return [4]any{r.group, r.header, r.dir, r.entry.Path + r.stash.Ref + r.path}
		}
		ui.List(c, list, len(rows), func(i int) { a.changeRow(c, w, v, rows[i]) }).Grow(1).Padding(0, 0, 8, 0)
		if g.tab == 0 {
			a.changesKeys(c, w, v, rows)
		}
	}
	// ⌘K 时在储藏分页上：切回提交分页，提交框才在
	if g.focusMsg {
		g.tab = 0
	}
	if g.tab == 0 {
		a.commitBox(c, v)
	}
}

// changesKeys 是改动列表的键盘操作，照 WebStorm 的 Commit 工具窗口：焦点在列表里（点过一行）时
// ↑↓ 换文件并在 diff 标签里预览，空格勾选、取消勾选，回车打开文件。冲突文件点开是编辑器，不在 ↑↓ 之列。
func (a *app) changesKeys(c *ui.Context, w *workspace, v *filesView, rows []changeRow) {
	g := v.git
	var files []int
	cur := -1
	for i, r := range rows {
		if r.header || r.dir || r.entry.Conflict {
			continue
		}
		if v.cur != nil && v.cur.path == diffKey(g.absPath(r.entry.Path), changeSpec(g.repo, r.entry)) {
			cur = len(files)
		}
		files = append(files, i)
	}
	if len(files) == 0 {
		return
	}
	step := 0
	switch {
	case g.list.Shortcut(c, 0, ui.KeyDown):
		step = 1
	case g.list.Shortcut(c, 0, ui.KeyUp):
		step = -1
	}
	if step != 0 {
		j := cur + step
		if cur < 0 {
			j = 0
		}
		if j >= 0 && j < len(files) {
			a.openDiff(w, changeSpec(g.repo, rows[files[j]].entry), nil, true)
			g.list.ScrollIntoView(files[j])
			// 打开 diff 不该把焦点从列表拿走，接着按 ↑↓
			g.list.Focus(c)
		}
		return
	}
	if cur < 0 {
		return
	}
	e := rows[files[cur]].entry
	switch {
	case g.list.Shortcut(c, 0, ui.KeySpace):
		g.setPicked(e, !g.picked(e))
	case g.list.Shortcut(c, 0, ui.KeyEnter):
		if statusLetter(e) != 'D' {
			a.openFile(w, g.absPath(e.Path), 0, [2]int{})
		}
	}
}

// changesTab 是改动页的一个分页：文字加个数，选中的垫一层填充底。
func changesTab(c *ui.Context, sel *int, i int, label string, n int) {
	t := c.Theme()
	active := *sel == i
	row := ui.Row(c.Key(label)).Height(sz(26)).Padding(0, 12).Gap(4).Radius(rRow).AlignItems(ui.Center).
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
		text := ui.Text(c, label).FontSize(fsBody)
		if active {
			text.FontWeight(fwMedium)
		} else {
			text.TextColor(t.TextMuted)
		}
		if n > 0 {
			ui.Text(c, strconv.Itoa(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
		}
	})
	if row.Clicked() {
		*sel = i
	}
}

// changesHeader 是改动列表上方一行：分支、领先和落后的提交数，以及刷新、储藏。
func (a *app) changesHeader(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	g := v.git
	st := g.status
	ui.Row(c).Height(sz(30)).Shrink(0).Padding(0, 6, 0, 16).Gap(4).AlignItems(ui.Center).Children(func() {
		head := st.Head
		if head == "" {
			head = L(c).GitDetachedHead()
		}
		ui.Icon(c, iconBranch).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, head).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		trackBadge(c, st.Ahead, st.Behind, true)
		ui.Spacer(c)
		if g.busy != "" {
			busyLine(c, g)
		}
		if pressable(iconButtonSized(c, iconGitLog, L(c).GitLogTitle(), buttonBox(), iconSize)).Clicked() {
			a.openLog(w, "", "")
		}
		if pressable(iconButtonSized(c, iconRefresh, L(c).CommonRefresh(), buttonBox(), iconSize)).Clicked() {
			a.rescanRepos(v)
		}
		if g.changeCount() > 0 && pressable(iconButtonSized(c, iconRollback, a.withKey(L(c).GitRollbackEllipsis(), "rollback"), buttonBox(), iconSize)).Clicked() {
			a.openRollback(v)
		}
		if g.changeCount() > 0 && pressable(iconButtonSized(c, iconShelve, L(c).GitStashTip(), buttonBox(), iconSize)).Clicked() {
			m := L(c)
			a.prompt(promptReq{title: m.GitStashTitle(), body: m.GitStashBody(),
				placeholder: m.GitStashMessage(), verb: m.GitStash(), optional: true, do: func(msg string, _ bool) {
					a.gitDo(m, v.git, m.GitStash(), func(ctx context.Context, root string) error { return gitx.StashPush(ctx, root, msg) })
				}})
		}
	})
}

// letterColor 是按状态字母给文件名的颜色，照 WebStorm：修改（含重命名）蓝、新增绿、删除灰，未跟踪和冲突红
// （WebStorm 两者也是相近的红，靠所在分组和悬停说明区分；琥珀色只给待处理）。文件树、改动、提交里的文件都用它。
func letterColor(c *ui.Context, letter byte) ui.Color {
	t := c.Theme()
	switch letter {
	case 'A':
		return colors(t).sage
	case 'D':
		return t.TextMuted
	case '?', 'U':
		return t.Danger
	}
	return colors(t).sky
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
	g := v.git
	if r.header {
		open := g.groupOpen(r.group)
		head := ui.Row(c).Height(sz(26)).Padding(6, 16, 0, groupHeadPad).Gap(6).AlignItems(ui.Center)
		head.Children(func() {
			// 箭头在最左边，点它或组名展开收起
			title := groupTitles[r.group](L(c))
			disclosure(c, open, L(c).GitExpandGroup(title), L(c).GitCollapseGroup(title))
			// 改动和未跟踪的组标题能一键全选、全不选
			if r.group == groupChanged || r.group == groupUntracked {
				var in []gitx.Entry
				for _, e := range g.status.Entries {
					if !e.Conflict && e.Untracked == (r.group == groupUntracked) {
						in = append(in, e)
					}
				}
				state := g.pickState(in)
				if pickBox(c, fmt.Sprint("group-", r.group), title, state) {
					g.setPickedAll(in, state != pickAll)
				}
			}
			row := ui.Row(c.Key("title")).Gap(6).AlignItems(ui.Center).Role(ui.RoleButton).Label(title)
			row.Children(func() {
				ui.Text(c, title).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
				ui.Text(c, fmt.Sprint(r.count)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			})
			if row.Clicked() {
				*open = !*open
			}
		})
		return
	}
	if r.group == groupStash {
		a.stashRow(c, v, r.stash)
		return
	}
	if r.dir {
		a.changeDirRow(c, v, r)
		return
	}
	e := r.entry
	spec := changeSpec(g.repo, e)
	key := diffKey(g.absPath(e.Path), spec)
	// 比组标题缩进一层：勾选框和组名的文字对齐；按目录分组时再按层缩进
	indent := groupTextIndent()
	if r.tree {
		indent += float32(r.depth) * treeIndent
	}
	row := ui.Row(c.Key(key)).Height(sz(28)).Margin(2, 6, 0, 6).Padding(0, 8, 0, indent).Gap(6).Radius(rRow).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(e.Path)
	selectedBg(c, row, v.cur != nil && v.cur.path == key)
	letter := statusLetter(e)
	color := letterColor(c, letter)
	row.Children(func() {
		if r.tree {
			// 文件没有展开箭头，留出同样的宽度，勾选框和同一层的目录对齐
			ui.Box(c).Width(miniBox()).Shrink(0)
		}
		if !e.Conflict {
			state := pickNone
			if g.picked(e) {
				state = pickAll
			}
			if pickBox(c, "pick", e.Path, state) {
				g.setPicked(e, state != pickAll)
			}
		}
		changeGlyph(c, e.Path, e.IsDir(), letter == 'D')
		name := ui.Text(c, filepath.Base(e.Path)).FontSize(fsBody).SingleLine().Shrink(0).TextColor(color).Tooltip(gitStatusText(L(c), letter))
		if letter == 'D' {
			name.Strikethrough().TextColor(t.TextMuted)
		}
		if dir := filepath.Dir(strings.TrimSuffix(e.Path, "/")); dir != "." && !r.tree {
			ui.Text(c, dir).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		if s, ok := g.head[e.Path]; ok && !s.Binary && r.group != groupUntracked {
			if s.Added > 0 {
				ui.Text(c, fmt.Sprintf("+%d", s.Added)).FontSize(fsSmall).FontFeatures("tnum").TextColor(p.sage).Shrink(0)
			}
			if s.Deleted > 0 {
				ui.Text(c, fmt.Sprintf("−%d", s.Deleted)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.Danger).Shrink(0)
			}
		}
	})
	if letter != 'D' {
		a.dragPath(row, g.absPath(e.Path))
	}
	if row.Clicked() {
		if e.Conflict {
			a.openFile(w, g.absPath(e.Path), 0, [2]int{})
		} else {
			a.openDiff(w, spec, nil, true)
			// 行是按钮、不拿焦点；焦点给列表，↑↓ 才能接着换
			g.list.Focus(c)
		}
	}
	contextMenu(c, row, func(m *popMenu) {
		if letter != 'D' && m.Item(L(c).GitOpenFile()).Chosen() {
			a.openFile(w, g.absPath(e.Path), 0, [2]int{})
		}
		m.Separator()
		switch {
		case e.Untracked:
			if m.Item(L(c).GitAdd()).Chosen() {
				a.gitDo(L(c), v.git, L(c).GitAdd(), func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
			}
		case e.Conflict:
			ours, theirs := sideNames(L(c), g.status.Operation, g.autostash != "" && g.autostashConflicts)
			if m.Item(L(c).GitAcceptSide(ours)).Chosen() {
				a.gitDo(L(c), v.git, L(c).GitResolve(), func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, e.Path, true) })
			}
			if m.Item(L(c).GitAcceptSide(theirs)).Chosen() {
				a.gitDo(L(c), v.git, L(c).GitResolve(), func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, e.Path, false) })
			}
			if m.Item(L(c).GitMarkResolvedLabel()).Chosen() {
				a.gitDo(L(c), v.git, L(c).GitMarkResolved(), func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, e.Path) })
			}
		}
		if !e.Conflict {
			label := L(c).GitRollbackEllipsis()
			if e.Untracked {
				label = L(c).CommonDeleteEllipsis()
			}
			if m.Item(label).Keys(a.keyText("rollback")).Chosen() {
				a.openRollbackFor(v.git, []string{e.Path}, nil)
			}
		}
		m.Separator()
		abs := g.absPath(e.Path)
		if letter != 'D' && m.Item(L(c).CommonRevealInFinder()).Icon(iconFolder).Chosen() {
			mygo.Shell.ShowItemInFolder(abs)
		}
		if m.Item(L(c).CommonCopyPath()).Chosen() {
			c.WriteClipboard(abs)
		}
	})
}

// changeDirRow 画按目录分组时的目录行：展开箭头、整个目录的勾选、目录名和文件数；点行展开收起。
func (a *app) changeDirRow(c *ui.Context, v *filesView, r changeRow) {
	g := v.git
	folds := g.dirFolds(r.group)
	row := ui.Row(c.Key("dir:"+r.path)).Height(sz(28)).Margin(2, 6, 0, 6).Padding(0, 8, 0, groupTextIndent()+float32(r.depth)*treeIndent).
		Gap(6).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(r.path)
	selectedBg(c, row, false)
	row.Children(func() {
		open := !folds[r.path]
		disclosure(c, &open, L(c).GitExpandPath(r.path), L(c).GitCollapsePath(r.path))
		if open == folds[r.path] {
			folds[r.path] = !open
		}
		if r.group != groupConflict {
			state := g.pickState(r.files)
			if pickBox(c, "dir:"+r.path, r.path, state) {
				g.setPickedAll(r.files, state != pickAll)
			}
		}
		treeDirLabel(c, r.name, len(r.files), "")
	})
	if row.Clicked() {
		folds[r.path] = !folds[r.path]
	}
	contextMenu(c, row, func(m *popMenu) {
		if r.group != groupConflict {
			label := L(c).GitRollbackEllipsis()
			if r.group == groupUntracked {
				label = L(c).CommonDeleteEllipsis()
			}
			if m.Item(label).Chosen() {
				paths := make([]string, len(r.files))
				for i, e := range r.files {
					paths[i] = e.Path
				}
				a.openRollbackFor(v.git, paths, nil)
			}
			m.Separator()
		}
		abs := g.absPath(r.path)
		// 里面的文件全删了时目录可能已经不在
		exists := slices.ContainsFunc(r.files, func(e gitx.Entry) bool { return statusLetter(e) != 'D' })
		if exists && m.Item(L(c).CommonRevealInFinder()).Icon(iconFolder).Chosen() {
			mygo.Shell.ShowItemInFolder(abs)
		}
		if m.Item(L(c).CommonCopyPath()).Chosen() {
			c.WriteClipboard(abs)
		}
	})
}

func (a *app) stashRow(c *ui.Context, v *filesView, s gitx.Stash) {
	t := c.Theme()
	lm := L(c)
	// 储藏是整个仓库共用的：别的 worktree 里存的也列在这里，标出分支，取回前先确认
	other := ""
	if head := v.git.status.Head; s.Branch != "" && head != "" && s.Branch != head {
		other = s.Branch
	}
	row := ui.Row(c.Key("stash:"+s.Ref)).Height(sz(28)).Margin(2, 6, 0, 6).Padding(0, 8, 0, 12).Gap(6).Radius(rRow).
		AlignItems(ui.Center).Role(ui.RoleButton).Label(s.Message)
	selectedBg(c, row, false)
	row.Children(func() {
		ui.Icon(c, iconShelve).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, stashTitle(s.Message)).FontSize(fsBody).SingleLine().Shrink(1).Grow(1).Tooltip(s.Message)
		if other != "" {
			ui.Text(c, other).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(2).Tooltip(lm.GitStashOnBranch(other))
		}
		ui.Text(c, ago(L(c), s.When)).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
	})
	owner := a.stashOwner(v, other)
	take := func(what string, fn func(ctx context.Context, root string) error) {
		if other == "" {
			a.gitDo(lm, v.git, what, fn)
			return
		}
		where := lm.GitStashWhereBranch(other)
		if owner != nil {
			where = lm.GitStashWhereWorkspace(displayName(owner), other)
		}
		a.confirm(lm.GitStashOtherConfirm(what), lm.GitStashOtherBody(where, what), lm.GitOperationAnyway(what), func() {
			a.update(func() { a.gitDo(lm, v.git, what, fn) })
		})
	}
	menuButton(c, row, func(m *popMenu) {
		if owner != nil && m.Item(lm.GitSwitchToWorkspace(displayName(owner))).Chosen() {
			a.selected, a.revealSel = owner.ID, true
			a.openFilesPanel(owner, sideChanges).git.tab = 1
		}
		// 照 WebStorm 储藏标签的右键：弹出（取回并删掉）、应用（储藏留着）、删除
		if m.Item(lm.GitStashPop()).Chosen() {
			take(lm.GitStashPopOperation(), func(ctx context.Context, root string) error { return gitx.StashPop(ctx, root, s.Ref) })
		}
		if m.Item(lm.GitStashApply()).Chosen() {
			take(lm.GitStashApplyOperation(), func(ctx context.Context, root string) error { return gitx.StashApply(ctx, root, s.Ref) })
		}
		m.Separator()
		if m.Item(lm.CommonDeleteEllipsis()).Chosen() {
			a.confirm(lm.GitStashDeleteConfirm(), lm.GitStashDeleteBody(s.Message), lm.CommonDelete(), func() {
				a.update(func() {
					a.gitDo(lm, v.git, lm.GitStashDelete(), func(ctx context.Context, root string) error { return gitx.StashDrop(ctx, root, s.Ref) })
				})
			})
		}
	})
}

// stashOwner 找同一项目里当前检出 branch 的工作区；没有（分支没人检出、不是同一项目）为 nil。
func (a *app) stashOwner(v *filesView, branch string) *workspace {
	cur := a.workspace(v.wsID)
	if branch == "" || cur == nil {
		return nil
	}
	for _, w := range a.byProject[cur.ProjectID] {
		if w.ID != cur.ID && w.Branch == branch {
			return a.workspace(w.ID)
		}
	}
	return nil
}

// opName 是进行到一半的操作在横幅、确认里的名字。
func opName(m locales.Messages, op string) string {
	switch op {
	case "rebase":
		return m.GitOpRebase()
	case "merge":
		return m.GitMerge()
	case "cherry-pick":
		return m.GitOpCherryPick()
	case "revert":
		return m.GitOpRevert()
	}
	return op
}

// sideNames 是冲突两边在这个操作里各是什么：rebase 时 ours 是要 rebase 到的分支，theirs 是你正在重放的提交。
func sideNames(m locales.Messages, op string, autostash bool) (ours, theirs string) {
	// 这个工作区的更新取回本地改动时冲突：ours 是更新后的 HEAD，theirs 是储藏里的本地改动
	if op == "" && autostash {
		return m.GitUpdatedVersion(), m.GitLocalChanges()
	}
	if op == "rebase" {
		return m.GitRebaseOurs(), m.GitRebaseTheirs()
	}
	return m.GitMergeOurs(), m.GitMergeTheirs()
}

// operationBanner 是 rebase、合并等操作进行到一半时的横幅：冲突数，继续、跳过、中止。
func (a *app) operationBanner(c *ui.Context, w *workspace, v *filesView) {
	g := v.git
	op := g.status.Operation
	if op == "" {
		return
	}
	t := c.Theme()
	m := L(c)
	name := opName(m, op)
	conflicts := 0
	for _, e := range g.status.Entries {
		if e.Conflict {
			conflicts++
		}
	}
	msg := m.GitOperationReady(name)
	if conflicts > 0 {
		msg = m.GitOperationConflicts(name, conflicts)
	}
	ui.Column(c).Shrink(0).Margin(2, 12, 8, 12).Padding(12, 12).Gap(8).Radius(rControl).Background(t.Surface).Children(func() {
		ui.Text(c, msg).FontSize(fsSmall).LineHeight(1.45)
		ui.Row(c).Gap(6).Children(func() {
			cont := pressable(accentButton(c, m.CommonContinue()))
			if conflicts > 0 {
				cont.Disabled(true)
			} else if cont.Clicked() {
				a.gitDo(m, v.git, m.GitContinueOperation(name), func(ctx context.Context, root string) error { return gitx.Continue(ctx, root, op) })
			}
			if (op == "rebase" || op == "cherry-pick") && pressable(textButton(c, m.GitSkipCommit())).Clicked() {
				a.gitDo(m, v.git, m.GitSkip(), func(ctx context.Context, root string) error { return gitx.Skip(ctx, root, op) })
			}
			if pressable(textButton(c, m.GitAbort())).Clicked() {
				a.confirm(m.GitAbortConfirm(name), m.GitAbortBody(name), m.GitAbort(), func() {
					a.update(func() {
						a.gitDo(m, v.git, m.GitAbort(), func(ctx context.Context, root string) error { return gitx.Abort(ctx, root, op) })
					})
				})
			}
		})
	})
}

// autostashBanner 照 WebStorm 的 "Local changes were restored with conflicts"：这个工作区的更新、改写提交前
// 自动储藏的本地改动没干净地取回，git 把它们留在储藏里。有冲突时引去解决；解决完了 WebStorm 让你自己去删
// 那条储藏，这里给一个按钮；根本没取回时引去储藏页手动应用。
func (a *app) autostashBanner(c *ui.Context, v *filesView) {
	g := v.git
	hash := g.autostash
	if hash == "" || g.status.Operation != "" {
		return
	}
	// 储藏在别处被删、被弹出了。刚结束时列表还是操作前读的，读到过一次才算数
	if slices.ContainsFunc(g.stashes, func(s gitx.Stash) bool { return s.Hash == hash }) {
		g.autostashSeen = true
	} else if g.autostashSeen {
		g.autostash = ""
		return
	}
	t := c.Theme()
	m := L(c)
	conflicts := 0
	for _, e := range g.status.Entries {
		if e.Conflict {
			conflicts++
		}
	}
	var msg string
	switch {
	case !g.autostashConflicts:
		msg = m.GitAutostashUnrestored()
	case conflicts > 0:
		msg = m.GitAutostashConflicts(conflicts)
	default:
		msg = m.GitAutostashResolved()
	}
	ui.Column(c).Shrink(0).Margin(2, 12, 8, 12).Padding(12, 6, 12, 12).Gap(8).Radius(rControl).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Text(c, msg).FontSize(fsSmall).LineHeight(1.45).Grow(1).Shrink(1)
			if iconButtonSized(c, iconClose, L(c).GitDismissHint(), compactBox(), smallIcon()).Clicked() {
				g.autostash = ""
			}
		})
		ui.Row(c).Gap(6).Children(func() {
			if g.autostashConflicts && conflicts == 0 {
				drop := pressable(accentButton(c, L(c).GitStashDeleteEllipsis()))
				if g.busy != "" {
					drop.Disabled(true)
				}
				if drop.Clicked() {
					a.confirm(m.GitStashDeleteConfirm(), m.GitAutostashDeleteBody(), m.CommonDelete(), func() {
						a.update(func() {
							a.gitDo(m, v.git, m.GitStashDelete(), func(ctx context.Context, root string) error { return gitx.StashDropHash(ctx, root, hash) })
						})
					})
				}
			}
			if pressable(textButton(c, L(c).GitViewStash())).Clicked() {
				g.tab = 1
			}
		})
	})
}

// stashTitle 去掉 git 加在储藏说明前面的 "On <分支>: " 或 "WIP on <分支>: "。
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
	g := v.git
	m := L(c)
	_, _, picked := g.pickedPaths()
	label := m.GitCommitFiles(picked)
	tip := a.withKey(m.GitCommitSelectedTip(), "commit")
	whole := g.wholeCommit()
	switch whole {
	case "merge":
		label = m.GitCommitMergeResult()
		tip = a.withKey(m.GitWholeCommitTip(m.GitMerge()), "commit")
	case "cherry-pick":
		label = m.GitCommitPickResult()
		tip = a.withKey(m.GitWholeCommitTip("Cherry-pick"), "commit")
	case "revert":
		label = m.GitCommitRevertResult()
		tip = a.withKey(m.GitWholeCommitTip(m.GitRevert()), "commit")
	}
	if whole == "" && picked == 0 && g.amend {
		label = m.GitRewordOnly()
	}
	hline(c)
	ui.Column(c).Shrink(0).Padding(12).Gap(8).Children(func() {
		msg := fieldArea(c, &g.message, L(c).GitCommitMessage(), 58).FontSize(fsBody)
		if g.focusMsg {
			msg.Focus()
			g.focusMsg = false
		}
		// 英文的勾选项和按钮都长，一行放不下时按钮换到下一行、靠右
		ui.Row(c).Gap(8).Wrap().Justify(ui.End).AlignItems(ui.Center).Children(func() {
			amend := g.amend
			if ui.Checkbox(c, &amend, L(c).GitAmend()).Changed() {
				a.setAmend(v, amend)
			}
			ui.Spacer(c)
			btn := pressable(accentButton(c, label)).Tooltip(tip)
			// 自动 Fetch 期间也能点，提交排在它后面
			if strings.TrimSpace(g.message) == "" {
				btn.Tooltip(L(c).GitMessageRequired())
			}
			if g.busy != "" && g.busyKind != gitFetch || g.pending(gitCommit) || strings.TrimSpace(g.message) == "" || picked == 0 && !g.amend && whole == "" {
				btn.Disabled(true)
			} else if btn.Clicked() || msg.Shortcut(ui.Cmd, ui.KeyEnter) {
				a.commit(m, v)
			}
		})
	})
}

// --- diff 标签页

func (a *app) diffView(c *ui.Context, v *filesView, d *fileDoc) {
	df := d.diff
	df.state.Font, df.state.LineHeight, df.state.BoldWeight = a.editorFont(), a.state.EditorLineHeight, a.editorBoldWeight()
	df.state.Bindings = a.editorBindings()
	// 左右对照时照 WebStorm 在中间分隔线旁给每处改动放 >>，上下合并时只有悬停的那处有"回滚"
	split := df.state.Layout == editor.DiffSplit
	df.state.HunkActions = df.worktree() && !split
	df.state.RenderHunkMark = nil
	df.state.OldNumbersRight = true
	if df.worktree() && split {
		df.state.RenderHunkMark = func(c *ui.Context, btn ui.Element, act *editor.DiffHunkActions) {
			t := c.Theme()
			btn.Width(24).Radius(rSmall).AlignItems(ui.Center).Justify(ui.Center).Label(L(c).GitRollbackChange()).Tooltip(L(c).GitRollbackChange())
			if btn.Hovered() {
				btn.Background(colors(t).wash)
			}
			btn.Children(func() { ui.Icon(c, iconRevertHunk).FontSize(iconSize).TextColor(t.TextMuted) })
			if btn.Clicked() {
				a.revertHunk(L(c), v, d, act.Index)
			}
		}
	}
	if df.worktree() {
		df.state.RenderHunkActions = func(c *ui.Context, bar ui.Element, act *editor.DiffHunkActions) {
			t := c.Theme()
			bar.Row().Gap(4)
			bar.Children(func() {
				btn := ui.ButtonBase(c).Key("revert").Padding(0, 8).Height(sz(20)).Radius(rSmall).Background(t.Surface).Label(L(c).GitRollbackChange())
				btn.Children(func() { ui.Text(c, L(c).GitRollback()).FontSize(fsSmall) })
				if btn.Clicked() {
					a.revertHunk(L(c), v, d, act.Index)
				}
			})
		}
	}
	df.state.RenderSeparator = diffSeparator
	// 切换忽略空白也会让改动数变
	clampHunk(df)
	// 左右对照的分界线位置所有 diff 共用，拖完松手才写 ui.json
	if df.state.Split == 0 {
		df.state.Split = a.state.DiffSplit
	}
	editor.DiffView(c.Key(d.path), &df.state).Grow(1)
	if s := df.state.Split; !df.state.SplitDragging && s != 0 && s != a.state.DiffSplit {
		a.state.DiffSplit = s
		a.saveState()
	}
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
// 组标题左边留 groupHeadPad，依次是展开箭头（miniBox）、勾选框（14）和组名，间隔 6；
// groupTextIndent 让文件行（外边距 6）的勾选框和组名的文字对齐。
const groupHeadPad = 12

func groupTextIndent() float32 { return groupHeadPad + miniBox() + 6 + 14 + 6 - 6 }

// 折叠行高 32（mygo 的 diffSeparatorHeight），减去内边距只剩 24：展开按钮用 miniBox，图标调大也放得下。
func diffSeparator(c *ui.Context, sep ui.Element, s *editor.DiffSeparator) {
	t := c.Theme()
	sep.Padding(4)
	sep.Children(func() {
		ui.Row(c).Grow(1).AlignSelf(ui.Stretch).AlignItems(ui.Center).Gap(2).Padding(0, 6).Radius(rRow).Background(colors(t).wash).Children(func() {
			if s.Lines == 0 {
				ui.Text(c, s.Header).Font(monoFont).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
				return
			}
			if !s.First && pressable(iconButtonSized(c.Key("down"), iconDown, L(c).GitExpandDown(), miniBox(), fsSmall)).Clicked() {
				s.ExpandDown()
			}
			if !s.Last && pressable(iconButtonSized(c.Key("up"), iconUp, L(c).GitExpandUp(), miniBox(), fsSmall)).Clicked() {
				s.ExpandUp()
			}
			b := ui.Box(c.Key("all")).Padding(2, 6).Radius(rSmall).Role(ui.RoleButton).Label(L(c).GitExpandAll())
			b.Children(func() { ui.Text(c, L(c).GitUnchangedLines(s.Lines)).FontSize(fsSmall).TextColor(t.TextMuted) })
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
	ui.Row(c).Height(sz(36)).Shrink(0).Padding(0, 8, 0, 8).Gap(4).AlignItems(ui.Center).Children(func() {
		// 上一处、下一处放在最左边（照 WebStorm 的 diff 工具栏），到头了换到同一组的上一个、下一个文件
		if pressable(iconButtonSized(c, iconArrowUp, a.withKey(L(c).GitPreviousChange(), "prevChange"), buttonBox(), iconSize)).Clicked() {
			a.stepDiff(L(c), w, v, d, -1)
		}
		if pressable(iconButtonSized(c, iconArrowDown, a.withKey(L(c).GitNextChange(), "nextChange"), buttonBox(), iconSize)).Clicked() {
			a.stepDiff(L(c), w, v, d, 1)
		}
		// 点路径弹出同一组的文件，在这个标签里换着看
		group := diffGroup(v, df)
		path := ui.Row(c.Key("diff-file")).Gap(4).Padding(2, 6).Radius(rSmall).AlignItems(ui.Center).Shrink(1).
			Role(ui.RoleButton).Label(L(c).GitSwitchFile()).Tooltip(L(c).GitSwitchFileTip(a.keyText("nextChange"), a.keyText("prevChange")))
		if path.Hovered() && len(group) > 1 {
			path.Background(t.SurfaceHover)
		}
		path.Children(func() {
			ui.Text(c, df.rel).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
			if len(group) > 1 {
				ui.Icon(c, iconDown).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			}
		})
		if len(group) > 1 {
			menuButton(c, path, func(m *popMenu) {
				for _, s := range group {
					label := s.rel
					if s.rel == df.rel {
						label = "✓ " + label
					}
					if m.Item(label).Chosen() {
						a.openDiff(w, s, df.siblings, true)
					}
				}
			})
		}
		if !df.plainChanges() {
			ui.Text(c, df.tabLabel(L(c))).FontSize(fsSmall).TextColor(t.TextMuted).Padding(2, 6).Radius(rSmall).Background(t.Surface).Shrink(0)
		}
		if d.kind == docDiff {
			n := len(df.state.Hunks())
			added, deleted := df.state.Stats()
			msg := L(c).GitNoDifferences()
			if n == 0 && df.state.IgnoreWhitespace && df.state.Old.String() != df.state.New.String() {
				msg = L(c).GitWhitespaceOnly()
			}
			if n > 0 {
				msg = L(c).GitChangeStats(min(df.hunk+1, n), n, added, deleted)
			}
			ui.Text(c, msg).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine().Shrink(1).Padding(0, 0, 0, 8)
		}
		ui.Spacer(c)
		// 排版和忽略空白只对文字的 diff 有用
		if d.kind != docImageDiff {
			split := df.state.Layout == editor.DiffSplit
			if toggleChip(c, L(c).GitSideBySide(), L(c).GitSideBySideTip(), split) {
				if split {
					df.state.Layout = editor.DiffUnified
				} else {
					df.state.Layout = editor.DiffSplit
				}
			}
			if toggleChip(c, L(c).GitIgnoreWhitespace(), L(c).GitIgnoreWhitespaceTip(), df.state.IgnoreWhitespace) {
				a.toggleIgnoreSpace(d)
			}
		}
		if df.worktree() && pressable(iconButtonSized(c, iconRollback, L(c).GitRollbackFile(), compactBox(), smallIcon())).Clicked() {
			a.rollbackDiffFile(w, v, d)
		}
		if pressable(iconButtonSized(c, iconOpen, a.withKey(L(c).GitOpenInEditor(), "diffOpenFile"), compactBox(), smallIcon())).Clicked() {
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
	a.openFile(w, df.abs(), line, [2]int{})
}

// gitShortcuts 是文件标签里的 Git 快捷键，照 WebStorm：F7 / ⇧F7 跳改动，F4 打开文件，⌘K 提交，⌥⌘Z 回滚。
func (a *app) gitShortcuts(c *ui.Context, w *workspace, v *filesView) {
	d := v.cur
	if d != nil && d.diff != nil {
		switch {
		case a.pressed(c, "nextChange"):
			a.stepDiff(L(c), w, v, d, 1)
		case a.pressed(c, "prevChange"):
			a.stepDiff(L(c), w, v, d, -1)
		case a.pressed(c, "diffOpenFile"):
			a.diffToEditor(w, v, d)
		case a.pressed(c, "rollback") && d.diff.worktree() && d.kind == docDiff:
			if hunks := d.diff.state.Hunks(); d.diff.hunk < len(hunks) {
				a.revertHunk(L(c), v, d, d.diff.hunk)
			}
		}
	}
}

const (
	pickNone = iota
	pickAll
	pickSome
)

// pickStateOf 是 n 个文件里勾了 on 个时的勾选状态。
func pickStateOf(on, n int) int {
	switch on {
	case 0:
		return pickNone
	case n:
		return pickAll
	}
	return pickSome
}

// treeDirLabel 是改动文件树目录行的文件夹图标、目录名和文件数，回滚对话框、改动页、提交和比较的文件列表共用；tip 非空时悬停目录名显示它。
func treeDirLabel(c *ui.Context, name string, n int, tip string) {
	t := c.Theme()
	dirGlyph(c, name, iconSize)
	label := ui.Text(c, name).FontSize(fsBody).SingleLine().Shrink(1)
	if tip != "" {
		label.Tooltip(tip)
	}
	ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
}

// pickBox 是改动列表里的勾选框：空框、勾上（强调色加对勾）、部分勾上（强调色加横线）。返回这一帧是否点了它。
func pickBox(c *ui.Context, key, name string, state int) bool {
	t := c.Theme()
	on := state == pickAll
	box := ui.CheckboxBase(c.Key("pick:"+key), &on).Size(14, 14).Radius(rSmall).Label(L(c).GitPickLabel(name))
	if state == pickSome {
		box.Mixed()
	}
	accent, line := t.Accent, colors(t).faint
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if state == pickNone {
			p.Stroke(r, line, 4, 1.2)
			return
		}
		p.Fill(r, accent, 4)
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
	m := L(c)
	newLabel := m.GitImageCurrent()
	if !df.worktree() {
		newLabel = m.GitImageNow(shortRev(m, df.newRev))
	}
	oldLabel := m.GitImageBefore()
	if df.oldRev != "" {
		oldLabel = m.GitImageBeforeRev(shortRev(m, df.oldRev))
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		for i, side := range df.images {
			if i == 1 {
				vline(c)
			}
			label := oldLabel
			if i == 1 {
				label = newLabel
			}
			ui.Column(c.Key(fmt.Sprint("img-", i))).Grow(1).Shrink(1).Basis(0).Padding(12, 16).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).FontSize(fsBody).FontWeight(fwMedium)
					switch {
					case side.size < 0 && i == 0:
						ui.Text(c, L(c).GitNewFile()).FontSize(fsSmall).TextColor(colors(t).sage)
					case side.size < 0:
						ui.Text(c, L(c).GitDeleted()).FontSize(fsSmall).TextColor(t.Danger)
					case side.img != nil:
						ui.Text(c, fmt.Sprintf("%d×%d", side.w, side.h)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
						ui.Text(c, byteSize(side.size)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
						if side.anim != nil {
							ui.Text(c, m.GitFrameCount(len(side.anim.delays))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
						}
					}
				})
				ui.Box(c).Grow(1).Center().Children(func() {
					switch {
					case side.err != "":
						ui.Text(c, m.GitImageUnreadable(side.err)).FontSize(fsSmall).TextColor(t.TextMuted)
					case side.img != nil:
						// 位图按 2 倍屏解码，按一半的尺寸显示才是原大
						w, h := side.img.Size()
						on := colors(t).fillHi
						anim, ck := side.anim, &df.checkers[i]
						ui.Box(c).Shrink(1).Draw(func(p *ui.Painter, r ui.Rect) {
							// 先换帧，下面的图片元素画的就是这一帧
							animate(p, anim)
							cols, rows := int(r.W/checkerTile)+1, int(r.H/checkerTile)+1
							p.Clip(r, 0, func() {
								p.ImageRegion(ck.get(cols, rows, on), ui.Rect{}, ui.Rect{X: r.X, Y: r.Y, W: float32(cols * checkerTile), H: float32(rows * checkerTile)}, ui.ImageOptions{})
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
