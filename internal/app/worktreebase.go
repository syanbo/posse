package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/locales"
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
func baseTip(m locales.Messages, base string, tr *trackInfo) string {
	if !tr.baseOK {
		return ""
	}
	d := tr.base
	var tip string
	if d.Behind > 0 {
		tip += "\n" + m.BranchBaseNew(base, d.Behind)
	}
	if d.Ahead > 0 {
		tip += "\n" + m.BranchBaseAhead(d.Ahead, base)
	}
	if len(d.Conflicts) > 0 {
		tip += "\n" + m.BranchBaseConflicts(len(d.Conflicts))
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
	m := L(c)
	d, base := tr.base, w.BaseRef
	// 英文放不下时同步、合并按钮换到下一行、靠右
	ui.Row(c).MinHeight(sz(30)).Shrink(0).Padding(0, 6, 0, 16).Gap(6).Wrap().Justify(ui.End).AlignItems(ui.Center).AlignContent(ui.Center).Children(func() {
		ui.Text(c, m.BranchRelativeTo(base)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(0)
		switch {
		case d.Ahead == 0 && d.Behind == 0:
			ui.Text(c, m.CompareNoDifference()).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		default:
			baseBadge(c, iconFetch, incomingColor(t), d.Behind, m.BranchBaseNewTip(base, d.Behind))
			baseBadge(c, iconPush, colors(t).sage, d.Ahead, m.BranchBaseAhead(d.Ahead, base))
		}
		if len(d.Conflicts) > 0 {
			tip := m.BranchConflictFilesTip(strings.Join(d.Conflicts, "\n"))
			ui.Text(c, m.BranchConflictCount(len(d.Conflicts))).FontSize(fsSmall).TextColor(t.Danger).Shrink(0).Tooltip(tip)
		}
		ui.Spacer(c)
		// 两个按钮放在一起，换行时一起换
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			if d.Behind > 0 {
				sync := pressable(smallButton(c, m.BranchSync())).Tooltip(m.BranchSyncTip(base))
				menuButton(c, sync, func(pm *popMenu) {
					if pm.Item(m.BranchMergeFrom(base)).Chosen() {
						a.syncBase(m, w, false)
					}
					if pm.Item(m.BranchRebaseOntoBase(base)).Chosen() {
						a.syncBase(m, w, true)
					}
				})
			}
			if d.Ahead > 0 && pressable(smallButton(c, m.GitMerge()).Tooltip(m.BranchMergeToTip(base))).Clicked() {
				a.askMergeWorkspace(m, w.ID)
			}
		})
	})
}

// smallButton 是放在 30 高的行里的 textButton。
func smallButton(c *ui.Context, label string) ui.Element {
	return textButton(c, label).Height(sz(24)).Padding(0, 8)
}

// baseBadge 是箭头加数字；数字为 0 不画。
func baseBadge(c *ui.Context, icon *ui.SVG, color ui.Color, n int, tip string) {
	if n == 0 {
		return
	}
	ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Tooltip(tip).Label(tip).Children(func() {
		ui.Icon(c, icon).FontSize(fsSmall).TextColor(color).Shrink(0)
		ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(color)
	})
}

// syncBase 在 worktree 里把基准分支合并进来或 rebase 上去。停在冲突上时改动页有操作横幅，
// 解决后继续，再合回项目目录就不会冲突了。
func (a *app) syncBase(m locales.Messages, w *workspace, rebase bool) {
	base := w.BaseRef
	what, done := m.BranchMergeFrom(base), m.BranchMerged(base)
	run := gitx.MergeInto
	if rebase {
		what, done, run = m.BranchRebaseToOp(base), m.BranchRebased(base), gitx.Rebase
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
	a.guardAgent(m, w, what, func() {
		v := a.openFilesPanel(w, sideChanges)
		v.git.tab = 0
		// 文件栏没打开过时还没确认仓库根目录；worktree 工作区的目录就是它的根目录
		if !v.git.checked {
			v.git.checked, v.git.repo = true, filepath.Clean(w.Path)
		}
		a.gitDoThen(m, v, what, fn, func() {
			if !stopped {
				a.notify(done, "", nil)
			}
		})
	})
}

// askMergeWorkspace 把 worktree 的分支合进项目目录（workspace.merge，冲突时常驻进程会还原）。
// 先现算一次会不会冲突（轮询的结果可能是同步之前的），会就不去试，改问要不要先在工作区里同步；
// 常驻进程合并失败时再算一次，确实是冲突的同样引过去，别的原因（项目目录不在基准分支上等）照常报错。
func (a *app) askMergeWorkspace(m locales.Messages, wsID string) {
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
	// restored 是常驻进程试过合并、冲突后已经还原了项目目录
	toSync := func(restored bool, files []string) {
		body := m.BranchSyncFirstBody(base, branch, fileList(m, files, 3))
		if restored {
			body = m.BranchSyncFirstBodyRestored(base, branch, fileList(m, files, 3))
		}
		a.confirm(m.BranchMergeConflicts(), body, m.BranchMergeFrom(base), func() {
			a.update(func() {
				if w := a.workspace(wsID); w != nil {
					a.syncBase(m, w, false)
				}
			})
		})
	}
	go func() {
		if files := conflicts(); len(files) > 0 {
			a.update(func() { toSync(false, files) })
			return
		}
		a.update(func() {
			a.confirm(m.BranchMergeWorkspaceConfirm(branch, base), m.BranchMergeWorkspaceBody(base), m.GitMerge(), func() {
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
						toSync(true, files)
					case err != nil:
						a.err = errtext.Text(err)
					default:
						a.confirm(m.BranchMergedTo(base), mergedBody(m, branch, untracked), m.BranchDeleteWorkspace(), func() {
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
func mergedBody(m locales.Messages, branch string, untracked []string) string {
	if len(untracked) > 0 {
		return m.BranchMergedBodyUntracked(branch, fileList(m, untracked, 3))
	}
	return m.BranchMergedBody(branch)
}

// fileList 列出前 n 个文件，多的写"等 N 个"。
func fileList(m locales.Messages, files []string, n int) string {
	if len(files) <= n {
		return strings.Join(files, m.BranchFileListSeparator())
	}
	return m.BranchFileListMore(strings.Join(files[:n], m.BranchFileListSeparator()), len(files))
}
