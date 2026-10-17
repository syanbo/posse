package app

import (
	"context"
	"fmt"
	"path/filepath"

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
}

func (a *app) openRollback(v *filesView) {
	g := &v.git
	if g.status == nil || g.busy != "" {
		return
	}
	r := rollbackDlg{open: true, v: v, on: map[string]bool{}}
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
	ui.Modal(c, &r.open, func() {
		ui.Column(c).Width(460).Gap(10).Children(func() {
			titleText(c, "回滚改动", fsCardTitle)
			ui.Text(c, "勾上的已跟踪文件会恢复成上一次提交时的样子，没提交的改动会丢失。").FontSize(13).TextColor(t.TextMuted).LineHeight(1.45)
			all := 0
			for _, e := range r.entries {
				if r.on[e.Path] {
					all++
				}
			}
			state := pickSome
			switch all {
			case 0:
				state = pickNone
			case len(r.entries):
				state = pickAll
			}
			ui.Row(c).Height(26).Padding(0, 8).Gap(8).AlignItems(ui.Center).Children(func() {
				if pickBox(c, "all", "全部", state) {
					for _, e := range r.entries {
						r.on[e.Path] = state != pickAll
					}
				}
				ui.Text(c, fmt.Sprintf("%d 个文件", len(r.entries))).FontSize(12).TextColor(t.TextMuted)
			})
			box := ui.Column(c).Height(min(float32(len(r.entries))*28+8, 300)).Radius(rInput).Background(colors(t).fill)
			box.Children(func() {
				r.list.Key = func(i int) any { return r.entries[i].Path }
				ui.List(c, &r.list, len(r.entries), func(i int) {
					e := r.entries[i]
					letter := statusLetter(e)
					ui.Row(c.Key(e.Path)).Height(28).Padding(0, 8).Gap(8).AlignItems(ui.Center).Children(func() {
						st := pickNone
						if r.on[e.Path] {
							st = pickAll
						}
						if pickBox(c, "file", e.Path, st) {
							r.on[e.Path] = st != pickAll
						}
						ui.Text(c, string(letter)).FontSize(11).Font(monoFont).FontWeight(500).TextColor(letterColor(c, letter)).Width(10).Shrink(0)
						ui.Text(c, filepath.Base(e.Path)).FontSize(13).SingleLine().Shrink(0)
						if dir := filepath.Dir(e.Path); dir != "." {
							ui.Text(c, dir).FontSize(11).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
						}
					})
				}).Grow(1)
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
