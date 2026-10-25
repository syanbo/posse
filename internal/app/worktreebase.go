package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/gitx"
)

// worktree 工作区相对基准分支（建工作区时分出来的那个）的状态：改动页顶上一行，
// 写基准分支上有几个新提交、这个分支有几个提交待合回去、现在合并会不会冲突，带同步和合并的按钮。
// 冲突照前面讨论的做法放在 worktree 里解决：先把基准分支合并进来（或 rebase 上去），再合回项目目录。

// hasBase 报告工作区是不是记着基准分支的 worktree。
func hasBase(w *workspace) bool { return w.Kind == "worktree" && w.BaseRef != "" }

// pollBase 和传入、传出一起每 10 秒在后台重算，结果记在 tr 上。
func pollBase(ctx context.Context, w *workspace, prev *gitx.Divergence) (gitx.Divergence, bool) {
	if !hasBase(w) {
		return gitx.Divergence{}, false
	}
	d, err := gitx.BaseDivergence(ctx, w.Path, w.BaseRef, prev)
	return d, err == nil
}

// baseTip 是分支按钮悬停提示里相对基准分支的几行。
func baseTip(base string, tr *trackInfo) string {
	if !tr.baseOK {
		return ""
	}
	d := tr.base
	var tip string
	if d.Behind > 0 {
		tip += fmt.Sprintf("\n%s 上有 %d 个新提交", base, d.Behind)
	}
	if d.Ahead > 0 {
		tip += fmt.Sprintf("\n%d 个提交待合回 %s", d.Ahead, base)
	}
	if len(d.Conflicts) > 0 {
		tip += fmt.Sprintf("\n现在合并会有 %d 个文件冲突", len(d.Conflicts))
	}
	return tip
}

// baseRow 画在改动页的分支行下面；rebase、合并进行中时让给操作横幅。
func (a *app) baseRow(c *ui.Context, w *workspace, v *filesView) {
	if !hasBase(w) || v.git.status == nil || v.git.status.Operation != "" {
		return
	}
	tr := a.pollTrack(c, w)
	if !tr.baseOK {
		return
	}
	t := c.Theme()
	d, base := tr.base, w.BaseRef
	ui.Row(c).Height(30).Shrink(0).Padding(0, 6, 0, 16).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "相对 "+base).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(0)
		switch {
		case d.Ahead == 0 && d.Behind == 0:
			ui.Text(c, "没有差别").FontSize(11.5).TextColor(t.TextMuted).Shrink(0)
		default:
			baseBadge(c, iconFetch, incomingColor(t), d.Behind, fmt.Sprintf("%s 上有 %d 个新提交，这个分支还没有", base, d.Behind))
			baseBadge(c, iconPush, colors(t).sage, d.Ahead, fmt.Sprintf("%d 个提交待合回 %s", d.Ahead, base))
		}
		if len(d.Conflicts) > 0 {
			tip := "现在合并会冲突的文件：\n" + strings.Join(d.Conflicts, "\n")
			ui.Text(c, fmt.Sprintf("会冲突 %d", len(d.Conflicts))).FontSize(11.5).TextColor(t.Danger).Shrink(0).Tooltip(tip)
		}
		ui.Spacer(c)
		if d.Behind > 0 {
			sync := pressable(smallButton(c, "同步")).Tooltip("把 " + base + " 的新提交拿过来")
			menuButton(c, sync, func(m *popMenu) {
				if m.Item("把 " + base + " 合并进来").Chosen() {
					a.syncBase(w, false)
				}
				if m.Item("rebase 到 " + base + " 上").Chosen() {
					a.syncBase(w, true)
				}
			})
		}
		if d.Ahead > 0 && pressable(smallButton(c, "合并").Tooltip("合并到 "+base)).Clicked() {
			a.askMergeWorkspace(w.ID)
		}
	})
}

// smallButton 是放在 30 高的行里的 textButton。
func smallButton(c *ui.Context, label string) ui.Element {
	return textButton(c, label).Height(24).Padding(0, 8)
}

// baseBadge 是箭头加数字；数字为 0 不画。
func baseBadge(c *ui.Context, icon *ui.SVG, color ui.Color, n int, tip string) {
	if n == 0 {
		return
	}
	ui.Row(c).Gap(1).AlignItems(ui.Center).Shrink(0).Tooltip(tip).Label(tip).Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(color).Shrink(0)
		ui.Text(c, fmt.Sprint(n)).FontSize(11).FontFeatures("tnum").TextColor(color)
	})
}

// syncBase 在 worktree 里把基准分支合并进来或 rebase 上去。停在冲突上时改动页有操作横幅，
// 解决后继续，再合回项目目录就不会冲突了。
func (a *app) syncBase(w *workspace, rebase bool) {
	base := w.BaseRef
	what := "把 " + base + " 合并进来"
	run := gitx.MergeInto
	if rebase {
		what, run = "rebase 到 "+base, gitx.Rebase
	}
	// 停在冲突上是这一步预期之内的结果：横幅会说明怎么继续，不再在错误条上报"失败"
	stopped := false
	fn := func(ctx context.Context, root string) error {
		err := run(ctx, root, base)
		if err != nil {
			if st, _ := gitx.ReadStatus(ctx, root); st != nil && st.Operation != "" {
				stopped = true
				return nil
			}
		}
		return err
	}
	a.guardAgent(w, what, func() {
		v := a.openFilesPanel(w, sideChanges)
		v.git.tab = 0
		// 文件栏没打开过时还没确认仓库根目录；worktree 工作区的目录就是它的根目录
		if !v.git.checked {
			v.git.checked, v.git.repo = true, filepath.Clean(w.Path)
		}
		a.gitDoThen(v, what, fn, func() {
			if !stopped {
				a.notify("已"+what, "", nil)
			}
		})
	})
}

// askMergeWorkspace 把 worktree 的分支合进项目目录（workspace.merge，冲突时常驻进程会还原）。
// 先现算一次会不会冲突（轮询的结果可能是同步之前的），会就不去试，改问要不要先在工作区里同步；
// 常驻进程合并失败时再算一次，确实是冲突的同样引过去，别的原因（项目目录不在基准分支上等）照常报错。
func (a *app) askMergeWorkspace(wsID string) {
	w := a.workspace(wsID)
	if w == nil || !hasBase(w) {
		return
	}
	ws := *w
	branch, base := ws.Branch, ws.BaseRef
	conflicts := func() []string {
		d, _ := gitx.BaseDivergence(context.Background(), ws.Path, base, nil)
		return d.Conflicts
	}
	toSync := func(lead string, files []string) {
		a.confirm("合并会冲突", fmt.Sprintf("%s%s 上的新提交和 %s 改了同样的地方（%s）。先在这个工作区里把 %s 合并进来、解决冲突，再合回去。", lead, base, branch, fileList(files, 3), base),
			"把 "+base+" 合并进来", func() {
				a.update(func() {
					if w := a.workspace(wsID); w != nil {
						a.syncBase(w, false)
					}
				})
			})
	}
	go func() {
		if files := conflicts(); len(files) > 0 {
			a.update(func() { toSync("", files) })
			return
		}
		a.update(func() {
			a.confirm("把 "+branch+" 合并到 "+base+"？", "在项目目录里以 merge commit 合入。项目目录必须在 "+base+" 分支上，两边都不能有未提交的改动（未跟踪的新文件不算）；冲突时会自动还原。", "合并", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				err := a.c.Call(ctx, "workspace.merge", map[string]any{"workspace": wsID}, nil)
				var files, untracked []string
				if err != nil {
					files = conflicts()
				} else {
					untracked = untrackedFiles(ctx, ws.Path)
				}
				a.update(func() {
					a.refsChanged(ws.ProjectID)
					switch {
					case err != nil && len(files) > 0:
						toSync("项目目录已经还原。", files)
					case err != nil:
						a.err = err.Error()
					default:
						a.confirm("已合并到 "+base, mergedBody(branch, untracked), "删除工作区", func() {
							a.call("workspace.rm", map[string]any{"workspace": wsID, "force": true, "deleteBranch": true}, nil, 30*time.Second)
							a.refresh()
						})
					}
				})
				a.refresh()
			})
		})
	}()
}

// untrackedFiles 是 worktree 里没提交的文件。合并要求已跟踪的文件干净，剩下的只会是未跟踪的；
// 照 WebStorm 删工作区用 worktree remove --force，它们会一起删掉，删之前列出来。
func untrackedFiles(ctx context.Context, dir string) []string {
	st, err := gitx.ReadStatus(ctx, dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range st.Entries {
		files = append(files, e.Path)
	}
	return files
}

// mergedBody 是合并成功后问要不要删工作区的说明。
func mergedBody(branch string, untracked []string) string {
	body := "worktree 和分支 " + branch + " 还在，不需要了可以删掉。"
	if len(untracked) > 0 {
		body += "删除时会连同没提交的文件一起删掉：" + fileList(untracked, 3) + "。"
	}
	return body
}

// fileList 列出前 n 个文件，多的写"等 N 个"。
func fileList(files []string, n int) string {
	if len(files) <= n {
		return strings.Join(files, "、")
	}
	return strings.Join(files[:n], "、") + fmt.Sprintf(" 等 %d 个文件", len(files))
}
