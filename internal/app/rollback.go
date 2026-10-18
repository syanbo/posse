package app

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// rollbackDlg 是改动页的批量回滚，照 WebStorm 提交窗口的 Rollback：列出改动的文件，默认勾上提交时勾着的
// （已跟踪的改动），新文件默认不勾。新文件（未跟踪的、刚加入 Git 的）只有勾了"删除新文件"才删磁盘上的，
// 否则刚加入 Git 的移出暂存区、未跟踪的不动。
type rollbackDlg struct {
	open    bool
	v       *filesView
	entries []gitx.Entry
	on      map[string]bool
	delNew  bool
	list    ui.ListState
	folded  map[string]bool // 收起的目录，按 treeRow.path
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

func (a *app) openRollback(v *filesView) {
	g := &v.git
	if g.status == nil {
		return
	}
	r := rollbackDlg{open: true, v: v, on: map[string]bool{}, folded: map[string]bool{}}
	for _, e := range g.status.Entries {
		if e.Conflict {
			continue
		}
		r.entries = append(r.entries, e)
		r.on[e.Path] = g.picked(e)
	}
	if len(r.entries) == 0 {
		return
	}
	a.rollback = r
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
	ui.Modal(c, &r.open, func() {
		ui.Column(c).Width(560).Gap(10).Children(func() {
			titleText(c, "回滚改动", fsCardTitle)
			ui.Text(c, "勾上的已跟踪文件会恢复成上一次提交时的样子，没提交的改动会丢失。").FontSize(13).TextColor(t.TextMuted).LineHeight(1.45)
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
			box := ui.Column(c).Height(min(float32(len(rows))*26+8, 360)).Radius(rInput).Background(colors(t).fill)
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
					a.gitDo(r.v, "回滚", func(ctx context.Context, root string) error {
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
					})
				}
			})
		})
	})
}

// rollbackRowView 画文件树的一行。尺寸照文件栏的文件树（行高 24 起、每层缩进 18）；
// 文件没有展开箭头，留出同样的宽度，勾选框和上一层目录名对齐。
func (a *app) rollbackRowView(c *ui.Context, g *gitState, row treeRow) {
	t := c.Theme()
	p := colors(t)
	r := &a.rollback
	line := ui.Row(c.Key(row.path)).Height(26).Padding(0, 10, 0, 4+float32(row.depth)*18).Gap(6).AlignItems(ui.Center)
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
		name := ui.Text(c, row.name).FontSize(13).SingleLine().Shrink(1).Tooltip(e.Path)
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
