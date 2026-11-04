package app

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/gitx"
	"posse/locales"
)

// 单个文件的历史，照 WebStorm 的"历史记录"：左边是改过这个文件的提交（作者、时间、提交信息、标签），
// 右边是选中提交里这个文件的 diff（父提交对比这个提交，根提交的旧一边是空的），默认上下合并显示。
// 不跟踪重命名：只列当前路径的提交。

// fileHistory 是文件历史里选中的提交和它的 diff，挂在 logDoc 上。
type fileHistory struct {
	sel  string   // 选中的提交
	diff *fileDoc // 选中提交里这个文件的 diff，和 diff 标签共用 loadDiff、diffView
	// 换提交时沿用的排版和忽略空白
	layout   editor.DiffLayout
	ignoreWS bool
}

func (a *app) fileHistoryView(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	ld := d.log
	if ld.hist == nil {
		ld.hist = &fileHistory{layout: editor.DiffUnified, ignoreWS: true}
	}
	h := ld.hist
	// 还没选或者选中的提交不在列表里（筛选、刷新过）：选第一个
	if len(ld.commits) > 0 && !containsCommit(ld.commits, h.sel) {
		a.selectHistory(v, d, ld.commits[0])
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(460).Shrink(0).Children(func() { a.historyList(c, w, v, d) })
		vline(c)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() { a.historyDiff(c, v, d) })
	})
}

func containsCommit(cs []gitx.CommitInfo, hash string) bool {
	for _, c := range cs {
		if c.Hash == hash {
			return true
		}
	}
	return false
}

// selectHistory 选中一个提交，在后台读它对这个文件的 diff。
func (a *app) selectHistory(v *filesView, d *fileDoc, ci gitx.CommitInfo) {
	h := d.log.hist
	if old := h.diff; old != nil && old.diff != nil {
		h.layout, h.ignoreWS = old.diff.state.Layout, old.diff.state.IgnoreWhitespace
	}
	h.sel = ci.Hash
	parent := ""
	if len(ci.Parents) > 0 {
		parent = ci.Parents[0]
	}
	spec := diffSpec{root: d.log.root, rel: d.log.filter.Path, oldRev: parent, newRev: ci.Hash}
	df := &diffDoc{diffSpec: spec, first: true}
	df.state = editor.DiffState{Layout: h.layout, IgnoreWhitespace: h.ignoreWS}
	h.diff = &fileDoc{path: "history:" + ci.Hash + ":" + spec.rel, diff: df, started: true}
	a.loadDiff(v, h.diff)
}

// historyList 是左边的提交列表，右键菜单和日志里的一样。
func (a *app) historyList(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	ld := d.log
	h := ld.hist
	ui.Row(c).Height(sz(34)).Shrink(0).Padding(0, 8, 0, 16).Gap(6).AlignItems(ui.Center).Children(func() {
		fileGlyph(c, ld.filter.Path, fsSmall)
		ui.Text(c, ld.filter.Path).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(ld.filter.Path)
		ui.Spacer(c)
		n := L(c).LogCommitCount(len(ld.commits))
		if ld.more {
			n = L(c).LogMoreCommitCount(len(ld.commits))
		}
		ui.Text(c, n).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		if pressable(iconButtonSized(c, iconRefresh, L(c).CommonRefresh(), compactBox(), smallIcon())).Clicked() {
			a.loadLog(v, d, false)
		}
	})
	if len(ld.commits) == 0 {
		msg := L(c).LogFileEmpty()
		if ld.loading {
			msg = L(c).CommonLoading()
		}
		ui.Text(c, msg).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	rows := len(ld.commits)
	if ld.more {
		rows++
	}
	ld.list.Key = func(i int) any {
		if i == len(ld.commits) {
			return "more"
		}
		return ld.commits[i].Hash
	}
	ui.List(c, &ld.list, rows, func(i int) {
		if i == len(ld.commits) {
			b := ui.Box(c.Key("more")).Height(sz(30)).Padding(0, 16).Role(ui.RoleButton).Label(L(c).LogLoadEarlier())
			b.Children(func() { ui.Text(c, L(c).LogLoadEarlier()).FontSize(fsSmall).TextColor(t.Accent) })
			if b.Clicked() && !ld.loading {
				a.loadLog(v, d, true)
			}
			return
		}
		ci := ld.commits[i]
		row := ui.Row(c.Key(ci.Hash)).Height(sz(30)).Margin(2, 6, 0, 6).Padding(0, 8).Gap(8).Radius(rRow).AlignItems(ui.Center).
			Role(ui.RoleButton).Label(ci.Subject)
		selectedBg(c, row, h.sel == ci.Hash)
		row.Children(func() {
			ui.Text(c, ci.Author).FontSize(fsBody).SingleLine().Width(sz(84)).Shrink(0).Tooltip(ci.Author + " <" + ci.Email + ">")
			ui.Text(c, L(c).AppDigitsDateTime(ci.Time.Local().Format("2006/1/2"), ci.Time.Local())).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, ci.Subject).FontSize(fsBody).SingleLine().Shrink(1).Grow(1)
			for _, r := range refNames(ci.Refs) {
				if strings.Contains(ci.Refs, "tag: "+r.name) {
					ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Children(func() {
						ui.Icon(c, iconTag).FontSize(fsSmall).TextColor(t.TextMuted)
						ui.Text(c, r.name).FontSize(fsSmall).TextColor(t.TextMuted)
					})
				}
			}
		})
		if row.Clicked() {
			if h.sel != ci.Hash {
				a.selectHistory(v, d, ci)
			}
			// 行是按钮、不拿焦点；焦点给列表，↑↓ 才能接着换
			ld.list.Focus(c)
		}
		contextMenu(c, row, func(m *popMenu) { a.commitMenu(c, m, w, v, d, ci) })
	}).Grow(1)
	// ↑↓ 换选中的提交，右边跟着换成它的 diff
	cur := slices.IndexFunc(ld.commits, func(x gitx.CommitInfo) bool { return x.Hash == h.sel })
	j := -1
	switch {
	case ld.list.Shortcut(c, 0, ui.KeyDown):
		j = cur + 1
	case ld.list.Shortcut(c, 0, ui.KeyUp):
		j = max(cur-1, 0)
	}
	if j >= 0 && j < len(ld.commits) && j != cur {
		a.selectHistory(v, d, ld.commits[j])
		ld.list.ScrollIntoView(j)
	}
}

// historyDiff 是右边选中提交的 diff：上一处 / 下一处、排版、差异数，两个版本和路径，下面是 diff。
func (a *app) historyDiff(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	hd := d.log.hist.diff
	if hd == nil {
		return
	}
	df := hd.diff
	ui.Row(c).Height(sz(34)).Shrink(0).Padding(0, 12, 0, 6).Gap(4).AlignItems(ui.Center).Children(func() {
		if pressable(iconButtonSized(c, iconArrowUp, L(c).GitPreviousDifference(), buttonBox(), iconSize)).Clicked() {
			stepHunk(L(c), df, -1)
		}
		if pressable(iconButtonSized(c, iconArrowDown, L(c).GitNextDifference(), buttonBox(), iconSize)).Clicked() {
			stepHunk(L(c), df, 1)
		}
		if hd.kind == docDiff {
			split := df.state.Layout == editor.DiffSplit
			if toggleChip(c, L(c).GitSideBySide(), L(c).GitSideBySideTip(), split) {
				if split {
					df.state.Layout = editor.DiffUnified
				} else {
					df.state.Layout = editor.DiffSplit
				}
			}
			if toggleChip(c, L(c).GitIgnoreWhitespace(), L(c).GitIgnoreWhitespaceTip(), df.state.IgnoreWhitespace) {
				a.toggleIgnoreSpace(hd)
			}
		}
		ui.Spacer(c)
		if hd.kind == docDiff {
			msg := L(c).GitNoDifferences()
			if n := len(df.state.Hunks()); n > 0 {
				msg = L(c).GitDifferenceCount(n)
			}
			ui.Text(c, msg).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		}
	})
	// 两个版本：父提交（没有就是"新增"）和这个提交，后面是路径
	ui.Row(c).Shrink(0).Padding(0, 16, 6, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		old := L(c).GitAdded()
		if df.oldRev != "" {
			old = shortRev(L(c), df.oldRev)
		}
		ui.Text(c, old+" → "+shortRev(L(c), df.newRev)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(0).Selectable()
		ui.Text(c, df.rel).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
	})
	ui.Column(c).Grow(1).Children(func() {
		switch hd.kind {
		case docDiff:
			a.diffView(c, v, hd)
		case docImageDiff:
			imageDiffView(c, hd)
		case docBinary:
			docMessage(c, hd, L(c).GitBinaryDiff())
		case docError:
			docMessage(c, hd, hd.err)
		case docTooLarge:
			a.tooLargeDiffView(c, v, hd)
		default:
			ui.Text(c, diffLoadingText(L(c), df)).FontSize(fsSmall).TextColor(t.TextMuted).Padding(16, 16)
		}
		diffHint(c, df)
	})
}

// stepHunk 在 diff 里跳到上一处 / 下一处改动；到头了提示一下。
func stepHunk(m locales.Messages, df *diffDoc, dir int) {
	hunks := df.state.Hunks()
	next := df.hunk + dir
	if next < 0 || next >= len(hunks) {
		msg := m.GitLastDifference()
		if dir < 0 {
			msg = m.GitFirstDifference()
		}
		df.hint, df.hintAt = msg, time.Now()
		return
	}
	df.hunk = next
	df.state.GoToHunk(next)
	df.state.Current = next + 1
}
