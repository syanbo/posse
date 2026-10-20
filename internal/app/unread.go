package app

import "hive/internal/agent"

// statusUnread 是界面自己的状态：本轮完成时你不在看这个会话，还没切过去看。照 Orca 的
// "Unread agent completion"，标签和侧栏用铃铛代替完成的蜂巢格。
const statusUnread = "unread"

func (a *app) markUnread(id string) {
	if a.unread == nil {
		a.unread = map[string]bool{}
	}
	a.unread[id] = true
}

// markRead 在每帧画主区域时调用：窗口有焦点、当前看着的会话就算看过了。
func (a *app) markRead(cur *terminalInfo) {
	if cur != nil && a.unread[cur.ID] && a.focused.Load() {
		delete(a.unread, cur.ID)
	}
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

// shownStatus 把还没看过的完成换成 statusUnread。
func (a *app) shownStatus(id, status string) string {
	if status == agent.StatusIdle && a.unread[id] {
		return statusUnread
	}
	return status
}
