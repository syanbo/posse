package app

import (
	"context"
	"time"

	"posse/internal/errtext"
	"posse/internal/history"
	"posse/locales"
)

// askTrashSession 的确认固定目标身份；真正的路径检查、运行状态复查和移动都在目标端执行。
func (a *app) askTrashSession(m locales.Messages, d *historyDetail) {
	r := a.request()
	if !r.mayWrite() {
		return
	}
	s := d.Session
	if s.Path == "" {
		a.err = m.TrashUnknownPath(s.Path)
		return
	}
	if d.Terminal != "" {
		a.err = m.TrashSessionRunning()
		return
	}
	title := m.TrashSessionConfirm(oneLine(s.Title))
	body := m.TrashSessionBody(agentTitle(s.Agent))
	verb := m.CommonMoveToTrash()
	if history.IsCursorIDEChat(s.Path) {
		title = m.HistoryDeleteCursorConfirm(oneLine(s.Title))
		body = m.HistoryDeleteCursorBody()
		verb = m.CommonDelete()
	}
	a.confirm(title, body, verb, func() {
		go func() {
			ctx, cancel := context.WithTimeout(a.windowContext(), time.Minute)
			err := r.invoke(ctx, "history.trash", map[string]any{"session": s.ID, "path": s.Path}, nil)
			cancel()
			r.update(func() {
				// 多文件会话可能只移动了一部分，报错时也重新读取目标状态。
				a.recent.poll.at = time.Time{}
				a.searchHistory(0)
				if err != nil {
					a.err = m.TrashMoveError(errtext.Text(err))
					return
				}
				if a.history.sel == sessionKey(s) {
					a.history.detailReq++
					a.history.sel, a.history.detail = "", nil
				}
			})
		}()
	})
}

// trashListedSession 的预查只决定确认框内容，服务端删除时仍重新查运行状态。
func (a *app) trashListedSession(m locales.Messages, s history.Session) {
	r := a.request()
	if !r.mayWrite() {
		return
	}
	go func() {
		var d historyDetail
		if r.call("history.show", map[string]any{"session": s.ID, "path": s.Path, "tail": 1, "tools": false}, &d, 10*time.Second) != nil {
			return
		}
		r.update(func() { a.askTrashSession(m, &d) })
	}()
}
