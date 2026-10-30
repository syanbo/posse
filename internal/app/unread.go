package app

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
)

// statusUnread 是本轮完成时你不在看这个会话，还没切过去看。照 Orca 的 "Unread agent completion"，
// 标签和侧栏用铃铛代替完成的蜂巢格。记在常驻进程里，手机上看过也算。
const statusUnread = "unread"

// markRead 在每帧画主区域时调用：窗口有焦点、当前看着的会话就算看过，常驻进程记着它没看的就报一次。
func (a *app) markRead(cur *terminalInfo) {
	prev := a.viewing
	a.viewing = ""
	if cur != nil && a.focused.Load() {
		a.viewing = cur.ID
	}
	if a.viewing != prev {
		a.updateBadge()
	}
	if a.viewing == "" || !cur.Unread {
		a.seenFor = ""
		return
	}
	if a.seenFor == cur.ID {
		return
	}
	a.seenFor = cur.ID
	id := cur.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.c.Call(ctx, "terminal.seen", map[string]any{"terminal": id}, nil); err != nil {
			log.Printf("terminal.seen %s: %v", id, err)
		}
	}()
}

// watchFocus 记下窗口焦点；切回窗口时重画一帧，看着的会话马上算看过。
func (a *app) watchFocus() {
	a.focused.Store(true)
	a.win.OnFocus(func() {
		a.focused.Store(true)
		a.win.Update(func() {})
	})
	a.win.OnBlur(func() { a.focused.Store(false) })
}

// shownStatus 把还没看过的完成换成 statusUnread；正看着的不换，常驻进程马上会收到看过。
// 看过的完成不画：它是多数会话平时的样子，画出来反而冲淡了要处理的几种。
// 常驻进程里照旧是 idle（terminal wait、任务投递、手机都靠它），只是界面上留空。
func (a *app) shownStatus(t terminalInfo, status string) string {
	if status != agent.StatusIdle {
		return status
	}
	if t.Unread && t.ID != a.viewing {
		return statusUnread
	}
	return ""
}

// 没看的完成一直摇铃铛：每轮 bellRing 内来回摆三次、越摆越小，再静止 bellRest，看过了才停。
// 摇的时候 30 帧（图标只有十几个像素，看着已经顺），静止时不安排帧：平均每秒 10 帧，
// 而且只是重画（Painter.After），不重建界面；窗口整个被挡住时 mygo 也不画。
const (
	bellRing  = 800 * time.Millisecond
	bellRest  = 1600 * time.Millisecond
	bellSwing = 16 // 每轮第一下的角度
	bellFrame = time.Second / 30
)

// statusIcon 是会话行和标签上的状态图形：完成了还没看的是隔一会儿摇一轮的铃铛，其余同 statusGlyph。
func (a *app) statusIcon(c *ui.Context, id, status string, size float32) {
	at, ok := a.unreadAt[id]
	if status != statusUnread || !ok || c.Preferences().ReduceMotion {
		statusGlyph(c, status, size)
		return
	}
	color := c.Theme().Accent
	ui.Box(c).Size(size, size).Shrink(0).Tooltip(statusText(status)).Draw(func(p *ui.Painter, r ui.Rect) {
		d := p.Now().Sub(at) % (bellRing + bellRest)
		if d >= bellRing {
			p.After(bellRing + bellRest - d)
			p.Icon(iconBell, r, color)
			return
		}
		p.After(bellFrame)
		x := float64(d) / float64(bellRing)
		angle := float32(bellSwing * (1 - x) * math.Sin(x*3*2*math.Pi))
		// 绕顶上的挂钩摆
		p.Transform(ui.Rotation(angle), r.X+r.W/2, r.Y+r.H*0.1, func() { p.Icon(iconBell, r, color) })
	})
}
