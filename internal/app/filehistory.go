package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/gitx"
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
	p := colors(c.Theme())
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(460).Shrink(0).Children(func() { a.historyList(c, w, v, d) })
		ui.Box(c).Width(1).Shrink(0).Background(p.line)
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
	spec := diffSpec{rel: d.log.filter.Path, oldRev: parent, newRev: ci.Hash}
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
	ui.Row(c).Height(34).Shrink(0).Padding(0, 8, 0, 14).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, ld.filter.Path).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(ld.filter.Path)
		ui.Spacer(c)
		n := fmt.Sprintf("%d 个提交", len(ld.commits))
		if ld.more {
			n = fmt.Sprintf("%d+ 个提交", len(ld.commits))
		}
		ui.Text(c, n).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		if pressable(iconButtonSized(c, iconRefresh, "刷新", 24, 14)).Clicked() {
			a.loadLog(v, d, false)
		}
	})
	if len(ld.commits) == 0 {
		msg := "这个文件还没有提交过"
		if ld.loading {
			msg = "正在读取…"
		}
		ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).Padding(12, 14)
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
			b := ui.Box(c.Key("more")).Height(30).Padding(0, 14).Role(ui.RoleButton).Label("加载更早的提交")
			b.Children(func() { ui.Text(c, "加载更早的提交").FontSize(12).TextColor(t.Accent) })
			if b.Clicked() && !ld.loading {
				a.loadLog(v, d, true)
			}
			return
		}
		ci := ld.commits[i]
		row := ui.Row(c.Key(ci.Hash)).Height(30).Margin(0, 6).Padding(0, 8).Gap(10).Radius(6).AlignItems(ui.Center).
			Role(ui.RoleButton).Label(ci.Subject)
		selectedBg(c, row, h.sel == ci.Hash)
		row.Children(func() {
			ui.Text(c, ci.Author).FontSize(12.5).SingleLine().Width(84).Shrink(0).Tooltip(ci.Author + " <" + ci.Email + ">")
			ui.Text(c, ci.Time.Format("2006/1/2 15:04")).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, ci.Subject).FontSize(12.5).SingleLine().Shrink(1).Grow(1)
			for _, r := range refNames(ci.Refs) {
				if strings.Contains(ci.Refs, "tag: "+r.name) {
					ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Children(func() {
						ui.Icon(c, iconTag).FontSize(11).TextColor(t.TextMuted)
						ui.Text(c, r.name).FontSize(11).TextColor(t.TextMuted)
					})
				}
			}
		})
		if row.Clicked() && h.sel != ci.Hash {
			a.selectHistory(v, d, ci)
		}
		row.ContextMenu(func(m *ui.Menu) { a.commitMenu(c, m, w, v, d, ci) })
	}).Grow(1)
}

// historyDiff 是右边选中提交的 diff：上一处 / 下一处、排版、差异数，两个版本和路径，下面是 diff。
func (a *app) historyDiff(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	hd := d.log.hist.diff
	if hd == nil {
		return
	}
	df := hd.diff
	ui.Row(c).Height(34).Shrink(0).Padding(0, 10, 0, 6).Gap(4).AlignItems(ui.Center).Children(func() {
		if pressable(iconButtonSized(c, iconArrowUp, "上一处差异", 28, 17)).Clicked() {
			stepHunk(df, -1)
		}
		if pressable(iconButtonSized(c, iconArrowDown, "下一处差异", 28, 17)).Clicked() {
			stepHunk(df, 1)
		}
		if hd.kind == docDiff {
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
		ui.Spacer(c)
		if hd.kind == docDiff {
			msg := "没有差异"
			if n := len(df.state.Hunks()); n > 0 {
				msg = fmt.Sprintf("%d 处差异", n)
			}
			ui.Text(c, msg).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		}
	})
	// 两个版本：父提交（没有就是"新增"）和这个提交，后面是路径
	ui.Row(c).Shrink(0).Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		old := "新增"
		if df.oldRev != "" {
			old = shortRev(df.oldRev)
		}
		ui.Text(c, old+" → "+shortRev(df.newRev)).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).Shrink(0).Selectable()
		ui.Text(c, df.rel).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
	})
	ui.Column(c).Grow(1).Children(func() {
		switch hd.kind {
		case docDiff:
			a.diffView(c, v, hd)
		case docImageDiff:
			imageDiffView(c, hd)
		case docBinary:
			docMessage(c, hd, "二进制文件，不显示 diff")
		case docError:
			docMessage(c, hd, hd.err)
		default:
			ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(16, 18)
		}
		diffHint(c, df)
	})
}

// stepHunk 在 diff 里跳到上一处 / 下一处改动；到头了提示一下。
func stepHunk(df *diffDoc, dir int) {
	hunks := df.state.Hunks()
	next := df.hunk + dir
	if next < 0 || next >= len(hunks) {
		msg := "已经是最后一处差异"
		if dir < 0 {
			msg = "已经是第一处差异"
		}
		df.hint, df.hintAt = msg, time.Now()
		return
	}
	df.hunk = next
	df.state.GoToHunk(next)
	df.state.Current = next + 1
}
