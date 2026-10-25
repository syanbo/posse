package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/gitx"
)

// changePop 是编辑器行号旁改动标记（相对 HEAD）点开的卡片，照 WebStorm 的 gutter 弹窗排成一行：
// 上一处 / 下一处、回滚这一处、看这个文件的 diff、复制改之前的内容、只提交这一处，下面是改之前的内容。
// 回滚改的是编辑器里的内容，和 ⌥⌘Z 一样可以撤销，保存后才写回磁盘。
type changePop struct {
	index   int
	message string // "提交此更改"的提交信息
	opened  bool   // 已经画过一帧：打开它的那次点击在卡片外面（色条上），不能当成点外面关掉
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
			ui.Text(c, fmt.Sprintf("第 %d / %d 处", cp.index+1, len(chs))).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted).Padding(0, 6).Shrink(0)
			if !d.ed.ReadOnly && pressable(iconButtonSized(c, iconRollback, "回滚这一处 ⌥⌘Z", 26, 15)).Clicked() {
				d.ed.RevertChange(cp.index)
				d.change = nil
			}
			rel := v.git.relPath(d.path)
			if rel != "" && pressable(iconButtonSized(c, iconDiff, "显示 diff", 26, 15)).Clicked() {
				d.change = nil
				a.openDiff(w, diffSpec{rel: rel, oldRev: "HEAD"}, nil, true)
			}
			copyOld := pressable(iconButtonSized(c, iconCopy, "复制改之前的内容", 26, 15))
			if ch.Old == "" {
				copyOld.Disabled(true)
			} else if copyOld.Clicked() {
				c.WriteClipboard(ch.Old)
				d.change = nil
			}
			if rel != "" {
				a.commitChangeInput(c, v, d, cp, rel, ch)
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
	outside := panel.PressedOutside() && cp.opened
	cp.opened = true
	if outside || c.Shortcut(0, ui.KeyEscape) {
		d.change = nil
	}
}

// commitChangeInput 是卡片里的"提交此更改"：写好提交信息回车，只把这一处提交上去，文件里别的改动不动。
// 提交的是编辑器里这一处的内容，所以有没保存的改动时先保存；合并、rebase 进行中不做。
func (a *app) commitChangeInput(c *ui.Context, v *filesView, d *fileDoc, cp *changePop, rel string, ch editor.BaseChange) {
	g := &v.git
	placeholder := "提交此更改"
	blocked := ""
	switch {
	case d.headBuf == nil:
		blocked = "上一次提交里没有这个文件"
	case d.isModified():
		blocked = "先保存再提交这一处"
	case g.status != nil && g.status.Operation != "":
		blocked = "合并或 rebase 进行中，到改动页里提交"
	}
	if blocked != "" {
		placeholder = blocked
	}
	ui.Column(c).Grow(1).Shrink(1).Padding(0, 4).Children(func() {
		in := fieldInput(c, &cp.message, placeholder)
		if blocked != "" {
			in.Disabled(true)
			return
		}
		msg := strings.TrimSpace(cp.message)
		if !in.Submitted() || msg == "" || g.pending("提交") {
			return
		}
		content := changeContent(d.headBuf.String(), d.ed.Buffer.String(), ch)
		a.gitDoThen(v, "提交", func(ctx context.Context, root string) error {
			return gitx.CommitChange(ctx, root, rel, []byte(content), msg)
		}, func() {
			if d.change == cp {
				d.change = nil
			}
			a.notify("已提交这一处："+msg, "", nil)
		})
	})
}

// changeContent 是 HEAD 的内容只换上这一处改动后的样子：基准的 OldStart 到 OldEnd 行换成编辑器里的
// NewStart 到 NewEnd 行，别的改动不带上。
func changeContent(base, buf string, ch editor.BaseChange) string {
	old, cur := strings.SplitAfter(base, "\n"), strings.SplitAfter(buf, "\n")
	clamp := func(lines []string, i int) int { return max(0, min(i, len(lines))) }
	var b strings.Builder
	for _, l := range old[:clamp(old, ch.OldStart)] {
		b.WriteString(l)
	}
	for _, l := range cur[clamp(cur, ch.NewStart):clamp(cur, ch.NewEnd)] {
		b.WriteString(l)
	}
	for _, l := range old[clamp(old, ch.OldEnd):] {
		b.WriteString(l)
	}
	return b.String()
}
