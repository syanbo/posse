package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// changePop 是编辑器行号旁改动标记（相对 HEAD）点开的卡片，照 WebStorm 的 gutter 弹窗：
// 上一处 / 下一处、回滚这一处、看这个文件的 diff，下面是改之前的内容。回滚改的是编辑器里的内容，
// 和 ⌥⌘Z 一样可以撤销，保存后才写回磁盘。
type changePop struct {
	index int
}

// changePopMax 是卡片里最多显示几行改之前的内容。
const changePopMax = 14

func (a *app) changePopup(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	cp := d.change
	if cp == nil {
		return
	}
	chs := d.ed.Changes()
	if cp.index < 0 || cp.index >= len(chs) {
		d.change = nil
		return
	}
	ch := chs[cp.index]
	// 卡片贴在这一处最后一行下面；只删了行的地方贴在删除位置那一行下面。随编辑器滚动
	last := ch.NewStart
	if ch.NewEnd > ch.NewStart {
		last = ch.NewEnd - 1
	}
	t := c.Theme()
	panel := floating(c, ui.Column(c.Key("change-pop")).Absolute().Top(d.ed.LineBottom(last)+2).Left(36).Width(560).Padding(6).Gap(6))
	panel.Children(func() {
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			step := func(i int) {
				if i < 0 || i >= len(chs) {
					return
				}
				cp.index = i
				d.ed.ScrollTo(chs[i].NewStart)
			}
			if pressable(iconButtonSized(c, iconArrowUp, "上一处改动", 26, 15)).Clicked() {
				step(cp.index - 1)
			}
			if pressable(iconButtonSized(c, iconArrowDown, "下一处改动", 26, 15)).Clicked() {
				step(cp.index + 1)
			}
			ui.Text(c, fmt.Sprintf("第 %d / %d 处", cp.index+1, len(chs))).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted).Padding(0, 6)
			ui.Spacer(c)
			if !d.ed.ReadOnly && pressable(iconButtonSized(c, iconRollback, "回滚这一处 ⌥⌘Z", 26, 15)).Clicked() {
				d.ed.RevertChange(cp.index)
				d.change = nil
			}
			if rel := v.git.relPath(d.path); rel != "" && pressable(iconButtonSized(c, iconGitLog, "显示 diff", 26, 15)).Clicked() {
				d.change = nil
				a.openDiff(w, diffSpec{rel: rel, oldRev: "HEAD"}, nil, true)
			}
			if pressable(iconButtonSized(c, iconClose, "关闭 Esc", 26, 14)).Clicked() {
				d.change = nil
			}
		})
		// 改之前的内容：删掉或改掉的行，淡红底；只新增了行时说明一句
		box := ui.Column(c).Radius(rInput).Background(t.Danger.Alpha(0.08)).Padding(6, 10)
		box.Children(func() {
			if ch.Old == "" {
				ui.Text(c, fmt.Sprintf("这里新增了 %d 行，上一次提交时没有", ch.NewEnd-ch.NewStart)).FontSize(12).TextColor(t.TextMuted)
				return
			}
			lines := strings.Split(ch.Old, "\n")
			more := 0
			if len(lines) > changePopMax {
				more, lines = len(lines)-changePopMax, lines[:changePopMax]
			}
			ui.Text(c, strings.Join(lines, "\n")).FontSize(a.fontSize - editorInset).Font(monoFont).LineHeight(1.45).Selectable()
			if more > 0 {
				ui.Text(c, fmt.Sprintf("还有 %d 行", more)).FontSize(11).TextColor(t.TextMuted).Padding(4, 0, 0, 0)
			}
		})
	})
	// 焦点还在编辑器里，Esc 在窗口这一级接（卡片开着时才注册，不抢编辑器平时的 Esc）
	if panel.PressedOutside() || c.Shortcut(0, ui.KeyEscape) {
		d.change = nil
	}
}
