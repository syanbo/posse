package app

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/gitx"
)

// rollbackDlg 是改动页的批量回滚，照 WebStorm 提交窗口的 Rollback：列出改动的文件，默认勾上提交时勾着的
// （已跟踪的改动），新文件默认不勾。新文件（未跟踪的、刚加入 Git 的）只有勾了"删除新文件"才删磁盘上的，
// 否则刚加入 Git 的移出暂存区、未跟踪的不动。
// 点文件名在上面另开一个 diff 浮窗（照 WebStorm 在回滚对话框里 ⌘D 看 diff），可以只回滚其中几处。
// 单个文件的回滚（编辑器顶栏、diff 栏、右键）也打开它，只勾这个文件，照 WebStorm。
type rollbackDlg struct {
	open      bool
	v         *filesView
	entries   []gitx.Entry
	on        map[string]bool
	delNew    bool
	list      ui.ListState
	folded    map[string]bool                  // 收起的目录，按 treeRow.path
	statusFor *gitx.Status                     // entries 取自哪一次状态：状态变了（逐处回滚之后）跟着更新
	detail    *fileDoc                         // diff 浮窗里看着的文件，不在文档区里
	then      func(restored, removed []string) // 整个文件回滚成功后在界面线程上调用，参数是恢复和删掉的路径
}

// treeRow 是改动文件树的一行：目录行的 files 是它下面所有文件在 paths 里的下标。
type treeRow struct {
	dir   bool
	path  string // 目录是相对仓库根目录的路径，文件是它自己的路径
	name  string // 只有一个子目录、没有文件的中间层并进来，如 internal/app
	depth int
	entry int
	files []int
}

type treeNode struct {
	dirs  map[string]*treeNode
	files []int
}

func rollbackTree(entries []gitx.Entry, folded map[string]bool) []treeRow {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return changeTree(paths, folded)
}

// changeTree 照 WebStorm 的改动树按目录排：目录在前、文件在后，各按名字排；
// 只有一个子目录、没有文件的目录和子目录并成一行。收起的目录不列出下面的行。
func changeTree(paths []string, folded map[string]bool) []treeRow {
	root := &treeNode{}
	for i, p := range paths {
		n := root
		if dir := filepath.Dir(p); dir != "." {
			for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
				if n.dirs == nil {
					n.dirs = map[string]*treeNode{}
				}
				if n.dirs[part] == nil {
					n.dirs[part] = &treeNode{}
				}
				n = n.dirs[part]
			}
		}
		n.files = append(n.files, i)
	}
	var rows []treeRow
	var walk func(n *treeNode, prefix string, depth int)
	walk = func(n *treeNode, prefix string, depth int) {
		for _, name := range slices.Sorted(maps.Keys(n.dirs)) {
			sub, label := n.dirs[name], name
			for len(sub.files) == 0 && len(sub.dirs) == 1 {
				for next, d := range sub.dirs {
					label, sub = label+"/"+next, d
				}
			}
			path := label
			if prefix != "" {
				path = prefix + "/" + label
			}
			row := treeRow{dir: true, path: path, name: label, depth: depth, entry: -1}
			collectFiles(sub, &row.files)
			rows = append(rows, row)
			if !folded[path] {
				walk(sub, path, depth+1)
			}
		}
		files := slices.Clone(n.files)
		slices.SortFunc(files, func(x, y int) int { return strings.Compare(paths[x], paths[y]) })
		for _, i := range files {
			rows = append(rows, treeRow{path: paths[i], name: filepath.Base(paths[i]), depth: depth, entry: i})
		}
	}
	walk(root, "", 0)
	return rows
}

func collectFiles(n *treeNode, out *[]int) {
	*out = append(*out, n.files...)
	for _, d := range n.dirs {
		collectFiles(d, out)
	}
}

// pickState 是一组文件的勾选状态。
func (r *rollbackDlg) pickState(files []int) int {
	on := 0
	for _, i := range files {
		if r.on[r.entries[i].Path] {
			on++
		}
	}
	switch on {
	case 0:
		return pickNone
	case len(files):
		return pickAll
	}
	return pickSome
}

func (r *rollbackDlg) setPicked(files []int, on bool) {
	for _, i := range files {
		r.on[r.entries[i].Path] = on
	}
}

func (a *app) openRollback(v *filesView) { a.openRollbackFor(v, nil, nil) }

// openRollbackFor 打开回滚对话框，pick 非空时只勾这几个文件；勾的有新文件就默认勾上"删除新文件"，
// 从这个文件发起回滚就是要它回到没有的样子。
func (a *app) openRollbackFor(v *filesView, pick []string, then func(restored, removed []string)) {
	g := &v.git
	if g.status == nil {
		return
	}
	r := rollbackDlg{open: true, v: v, on: map[string]bool{}, folded: map[string]bool{}, then: then}
	if len(pick) > 0 {
		for _, e := range g.status.Entries {
			r.on[e.Path] = slices.Contains(pick, e.Path)
			if r.on[e.Path] && isNewEntry(e) {
				r.delNew = true
			}
		}
	}
	r.sync(g)
	if len(r.entries) == 0 {
		return
	}
	a.rollback = r
	a.rollbackBox.reset()
}

// sync 按最新的状态列文件：已经列着的保留勾选，新出现的按提交时的勾选，没有改动了的去掉。
func (r *rollbackDlg) sync(g *gitState) {
	r.statusFor = g.status
	r.entries = r.entries[:0]
	seen := map[string]bool{}
	for _, e := range g.status.Entries {
		if e.Conflict {
			continue
		}
		r.entries = append(r.entries, e)
		seen[e.Path] = true
		if _, ok := r.on[e.Path]; !ok {
			r.on[e.Path] = g.picked(e)
		}
	}
	for p := range r.on {
		if !seen[p] {
			delete(r.on, p)
		}
	}
}

// entry 返回列表里路径为 path 的文件。
func (r *rollbackDlg) entry(path string) (gitx.Entry, bool) {
	i := slices.IndexFunc(r.entries, func(e gitx.Entry) bool { return e.Path == path })
	if i < 0 {
		return gitx.Entry{}, false
	}
	return r.entries[i], true
}

// rollbackShow 在 diff 浮窗里打开文件的 diff；排版和忽略空白沿用上一个。
func (a *app) rollbackShow(e gitx.Entry, first bool) {
	r := &a.rollback
	df := &diffDoc{diffSpec: changeSpec(e), first: first}
	df.state = editor.DiffState{Layout: editor.DiffSplit, IgnoreWhitespace: true}
	if old := r.detail; old != nil {
		df.state.Layout, df.state.IgnoreWhitespace = old.diff.state.Layout, old.diff.state.IgnoreWhitespace
	} else {
		a.rollbackDiffBox.reset()
	}
	r.detail = &fileDoc{path: diffKey(r.v.git.absPath(e.Path), df.diffSpec), diff: df, kind: docLoading, started: true}
	a.loadDiff(r.v, r.detail)
}

// rollbackStep 是详情里的上一处 / 下一处：到头了换到树里的上一个 / 下一个文件。
func (a *app) rollbackStep(dir int) {
	r := &a.rollback
	d := r.detail
	if d == nil {
		return
	}
	df := d.diff
	if next := df.hunk + dir; d.kind == docDiff && next >= 0 && next < len(df.state.Hunks()) {
		df.hunk = next
		df.state.GoToHunk(next)
		df.state.Current = next + 1
		return
	}
	var files []string
	for _, row := range rollbackTree(r.entries, nil) {
		if !row.dir {
			files = append(files, row.path)
		}
	}
	if j := slices.Index(files, df.rel) + dir; j >= 0 && j < len(files) && slices.Contains(files, df.rel) {
		e, _ := r.entry(files[j])
		a.rollbackShow(e, dir > 0)
		return
	}
	msg := "已经是最后一处改动"
	if dir < 0 {
		msg = "已经是第一处改动"
	}
	df.hint, df.hintAt = msg, time.Now()
}

func isNewEntry(e gitx.Entry) bool { return e.Untracked || e.X == 'A' }

// rollbackPaths 把勾上的文件分成要恢复成 HEAD 的、要删除的和只移出暂存区的；n 是真正会动的文件数，
// 勾了但没勾"删除新文件"的未跟踪文件不算。
func (r *rollbackDlg) rollbackPaths() (restore, remove, unstage []string, n int) {
	for _, e := range r.entries {
		if !r.on[e.Path] {
			continue
		}
		switch {
		case isNewEntry(e) && r.delNew:
			remove = append(remove, e.Path)
		case e.X == 'A':
			unstage = append(unstage, e.Path)
		case e.Untracked:
			continue
		default:
			restore = append(restore, e.Path)
			if e.Orig != "" {
				restore = append(restore, e.Orig)
			}
		}
		n++
	}
	return restore, remove, unstage, n
}

func (a *app) rollbackDialog(c *ui.Context) {
	r := &a.rollback
	if !r.open {
		return
	}
	t := c.Theme()
	g := &r.v.git
	if g.status != nil && g.status != r.statusFor {
		r.sync(g)
		if d := r.detail; d != nil && d.kind != docLoading {
			a.reloadDiff(r.v, d)
		}
	}
	box := &a.rollbackBox
	box.min[0] = ui.Rect{W: 420, H: 300}
	panel := ui.Modal(c, &r.open, func() {
		defer box.grips(c)
		a.rollbackMain(c, t, g)
	})
	box.apply(panel)
	if r.open && r.detail != nil {
		a.rollbackDiffDialog(c)
	}
}

func (a *app) rollbackMain(c *ui.Context, t *ui.Theme, g *gitState) {
	r := &a.rollback
	col := ui.Column(c).Gap(10)
	if !a.rollbackBox.sized() {
		col.Width(560)
	} else {
		col.Grow(1).Shrink(1)
	}
	col.Children(func() {
		a.rollbackBox.title(c, "回滚改动")
		ui.Text(c, "勾上的已跟踪文件会恢复成上一次提交时的样子，没提交的改动会丢失。点文件名看 diff，可以只回滚其中几处。").FontSize(13).TextColor(t.TextMuted).LineHeight(1.45)
		all := make([]int, len(r.entries))
		for i := range all {
			all[i] = i
		}
		rows := rollbackTree(r.entries, r.folded)
		ui.Row(c).Height(26).Padding(0, 4, 0, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			state := r.pickState(all)
			if pickBox(c, "all", "全部", state) {
				r.setPicked(all, state != pickAll)
			}
			ui.Text(c, fmt.Sprintf("%d 个文件", len(r.entries))).FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c)
			var dirs []string
			for _, row := range rollbackTree(r.entries, nil) {
				if row.dir {
					dirs = append(dirs, row.path)
				}
			}
			if len(dirs) > 0 {
				if pressable(iconButtonSized(c, iconDown, "全部展开", 24, 14)).Clicked() {
					clear(r.folded)
				}
				if pressable(iconButtonSized(c, iconUp, "全部收起", 24, 14)).Clicked() {
					for _, d := range dirs {
						r.folded[d] = true
					}
				}
			}
		})
		box := ui.Column(c).Radius(rInput).Background(colors(t).fill)
		if !a.rollbackBox.sized() {
			box.Height(min(max(float32(len(rows))*26+8, 300), 420)) // 文件少时也留出一块，不显得局促
		} else {
			box.Grow(1).Shrink(1)
		}
		box.Children(func() {
			r.list.Key = func(i int) any { return rows[i].path }
			ui.List(c, &r.list, len(rows), func(i int) {
				a.rollbackRowView(c, g, rows[i])
			}).Grow(1).Padding(4, 0)
		})
		ui.Checkbox(c, &r.delNew, "同时删除新文件（未跟踪的和刚加入 Git 的）")
		restore, remove, unstage, n := r.rollbackPaths()
		ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
			if pressable(textButton(c, "取消")).Clicked() {
				r.open = false
			}
			verb := fmt.Sprintf("回滚 %d 个文件", n)
			btn := pressable(accentButton(c, verb))
			if n == 0 {
				btn.Opacity(0.5)
			} else if btn.Clicked() {
				r.open = false
				var done func()
				if then := r.then; then != nil {
					done = func() { then(restore, remove) }
				}
				a.gitDoThen(r.v, "回滚", func(ctx context.Context, root string) error {
					if len(restore) > 0 {
						if err := gitx.Rollback(ctx, root, restore...); err != nil {
							return err
						}
					}
					if len(unstage) > 0 {
						if err := gitx.Unstage(ctx, root, unstage...); err != nil {
							return err
						}
					}
					if len(remove) > 0 {
						return gitx.RemoveNew(ctx, root, remove...)
					}
					return nil
				}, done)
			}
		})
	})
}

// rollbackDiffDialog 是回滚对话框上面的 diff 浮窗，照 WebStorm 另开一个窗口：可以拖动、拉伸，
// 每处改动旁边有回滚（立刻写回磁盘，列表跟着状态更新），"包含在回滚中"就是文件树里的勾选。
func (a *app) rollbackDiffDialog(c *ui.Context) {
	r := &a.rollback
	d := r.detail
	t := c.Theme()
	df := d.diff
	_, listed := r.entry(df.rel)
	box := &a.rollbackDiffBox
	box.min[0] = ui.Rect{W: 600, H: 360}
	open := true
	panel := ui.Modal(c, &open, func() {
		defer box.grips(c)
		switch {
		case c.Shortcut(0, ui.KeyF7):
			a.rollbackStep(1)
		case c.Shortcut(ui.Shift, ui.KeyF7):
			a.rollbackStep(-1)
		case c.Shortcut(ui.Cmd|ui.Alt, ui.KeyZ) && d.kind == docDiff && listed:
			if hunks := df.state.Hunks(); df.hunk < len(hunks) {
				a.revertHunk(r.v, d, df.hunk)
			}
		}
		col := ui.Column(c).Gap(10)
		if box.sized() {
			col.Grow(1).Shrink(1)
		} else {
			col.Width(1080).Height(640)
		}
		col.Children(func() {
			box.title(c, df.rel)
			ui.Row(c).Height(28).Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
				if pressable(iconButtonSized(c, iconArrowUp, "上一处改动 ⇧F7", 28, 17)).Clicked() {
					a.rollbackStep(-1)
				}
				if pressable(iconButtonSized(c, iconArrowDown, "下一处改动 F7", 28, 17)).Clicked() {
					a.rollbackStep(1)
				}
				if d.kind == docDiff {
					split := df.state.Layout == editor.DiffSplit
					if toggleChip(c, "左右", "左右对照，关掉是上下合并", split) {
						if split {
							df.state.Layout = editor.DiffUnified
						} else {
							df.state.Layout = editor.DiffSplit
						}
					}
					if toggleChip(c, "忽略空白", "只差空白的行不算改动", df.state.IgnoreWhitespace) {
						a.toggleIgnoreSpace(d)
					}
				}
				if listed {
					on := r.on[df.rel]
					if ui.Checkbox(c.Key("include"), &on, "包含在回滚中").Padding(0, 6).Changed() {
						r.on[df.rel] = !r.on[df.rel]
					}
				}
				ui.Spacer(c)
				if d.kind == docDiff && listed {
					if n := len(df.state.Hunks()); n > 0 {
						added, deleted := df.state.Stats()
						ui.Text(c, fmt.Sprintf("第 %d / %d 处  +%d −%d", min(df.hunk+1, n), n, added, deleted)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
					}
					// 每处旁边的 >> 要找到那一处才点得到，这里常驻一个，作用在 F7 走到的那一处
					if df.hunk < len(df.state.Hunks()) {
						if pressable(textButton(c, "回滚这一处")).Tooltip("回滚当前这处改动，立刻写回磁盘 ⌥⌘Z").Clicked() {
							a.revertHunk(r.v, d, df.hunk)
						}
					}
				}
			})
			ui.Column(c).Grow(1).Shrink(1).Radius(rInput).Background(colors(t).fill).Clip().Children(func() {
				muted := func(msg string) {
					ui.Text(c, msg).FontSize(12.5).TextColor(t.TextMuted).Padding(16)
				}
				switch {
				case !listed:
					muted("这个文件已经没有改动了")
				case d.kind == docLoading:
					muted(diffLoadingText(df))
				case d.kind == docError:
					ui.Text(c, d.err).FontSize(12.5).TextColor(t.Danger).Padding(16)
				case d.kind == docImageDiff:
					imageDiffView(c, d)
				case d.kind == docBinary:
					muted("二进制文件，只能整个回滚")
				case d.kind == docTooLarge:
					a.tooLargeDiffView(c, r.v, d)
				default:
					a.diffView(c, r.v, d)
				}
				diffHint(c, df)
			})
		})
	})
	box.apply(panel)
	if !open {
		r.detail = nil
	}
}

// rollbackRowView 画文件树的一行。尺寸照文件栏的文件树（行高 24 起、每层缩进 18）；
// 文件没有展开箭头，留出同样的宽度，勾选框和上一层目录名对齐。
func (a *app) rollbackRowView(c *ui.Context, g *gitState, row treeRow) {
	t := c.Theme()
	p := colors(t)
	r := &a.rollback
	line := ui.Row(c.Key(row.path)).Height(26).Padding(0, 10, 0, 4+float32(row.depth)*18).Gap(6).AlignItems(ui.Center)
	if !row.dir {
		// 文件行点了在 diff 浮窗里看；勾选框自己接住点击
		line.Role(ui.RoleButton).Label("查看 " + row.path)
		selectedBg(c, line, r.detail != nil && r.detail.diff.rel == row.path)
		if line.Clicked() {
			if e, ok := r.entry(row.path); ok && (r.detail == nil || r.detail.diff.rel != row.path) {
				a.rollbackShow(e, true)
			}
		}
	}
	line.Children(func() {
		if row.dir {
			open := !r.folded[row.path]
			disclosure(c, &open, "展开 "+row.path)
			if open == r.folded[row.path] {
				r.folded[row.path] = !open
			}
			state := r.pickState(row.files)
			if pickBox(c, "dir:"+row.path, row.path, state) {
				r.setPicked(row.files, state != pickAll)
			}
			ui.Icon(c, iconFolder).FontSize(15).TextColor(t.Accent).Shrink(0)
			ui.Text(c, row.name).FontSize(13).SingleLine().Shrink(1)
			ui.Text(c, fmt.Sprint(len(row.files))).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			return
		}
		e := r.entries[row.entry]
		ui.Box(c).Width(20).Shrink(0)
		state := pickNone
		if r.on[e.Path] {
			state = pickAll
		}
		if pickBox(c, "file", e.Path, state) {
			r.on[e.Path] = state != pickAll
		}
		letter := statusLetter(e)
		ui.Text(c, string(letter)).FontSize(11).Font(monoFont).FontWeight(500).TextColor(letterColor(c, letter)).Width(10).Shrink(0)
		name := ui.Text(c, row.name).FontSize(13).SingleLine().Shrink(1)
		if letter == 'D' {
			name.Strikethrough().TextColor(t.TextMuted)
		}
		ui.Spacer(c)
		if s, ok := g.head[e.Path]; ok && !s.Binary && !e.Untracked {
			if s.Added > 0 {
				ui.Text(c, fmt.Sprintf("+%d", s.Added)).FontSize(11).FontFeatures("tnum").TextColor(p.sage).Shrink(0)
			}
			if s.Deleted > 0 {
				ui.Text(c, fmt.Sprintf("−%d", s.Deleted)).FontSize(11).FontFeatures("tnum").TextColor(t.Danger).Shrink(0)
			}
		}
	})
}
