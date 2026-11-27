package daemon

import (
	"log"
	"time"

	"posse/internal/agent"
	"posse/internal/host"
	"posse/internal/store"
)

// cursorDoneQuiet 是 Cursor worker 汇报 task done 后，等待其收尾输出与缺失 stop hook 的静默兜底窗口。
var cursorDoneQuiet = 5 * time.Second

// cursorDoneWatch 记录 Cursor worker 刚通过 task done 汇报完一轮后的兜底监测态。
// cursor-agent 2026.11.22 的 stop hook 前置守卫漏判 pluginHooks，用户未配 ~/.cursor/hooks.json 时
// 不会发 stop，状态会一直停在 postToolUse 的 working；这里在任务已汇报、输出静默且屏幕回到空闲输入框后兜底置为 idle。
type cursorDoneWatch struct {
	TaskID string    `json:"taskId,omitempty"`
	At     time.Time `json:"at,omitempty"`
	gen    uint64
}

func (s *session) clearCursorDone() {
	s.mu.Lock()
	s.cursorDone = cursorDoneWatch{}
	s.mu.Unlock()
}

// armCursorDoneIdle 在 Cursor worker 汇报 task done 后开启空闲兜底监测。
// 不因当前状态已是 idle 而跳过：若整轮仅执行了 task done，该 shell 调用对应的 postToolUse
// 会在 taskDone 返回之后才到达并把状态置为 working。
func (d *Daemon) armCursorDoneIdle(t store.Task) {
	if t.Worker == "" {
		return
	}
	s := d.liveSession(t.Worker)
	if s == nil || !s.Running() {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.agent != "cursor" {
		s.mu.Unlock()
		return
	}
	s.cursorDone = cursorDoneWatch{TaskID: t.ID, At: now, gen: s.agentGen}
	s.mu.Unlock()
	time.AfterFunc(cursorDoneQuiet, func() { d.checkCursorDoneIdle(s) })
}

func (d *Daemon) checkCursorDoneIdle(s *session) {
	if s == nil || d.stopping.Load() || !s.Running() {
		return
	}
	s.mu.Lock()
	w := s.cursorDone
	curAgent, curGen := s.agent, s.agentGen
	if w.TaskID == "" {
		s.mu.Unlock()
		return
	}
	if curAgent != "cursor" || curGen != w.gen {
		s.cursorDone = cursorDoneWatch{}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	now := time.Now()
	st := s.Agent()
	switch st.Status {
	case agent.StatusExited:
		s.clearCursorDone()
		return
	case agent.StatusIdle:
		if now.Sub(w.At) >= cursorDoneQuiet {
			s.clearCursorDone()
		}
		return
	case agent.StatusWorking:
	default:
		return
	}

	// 用户在汇报之后手动敲过键盘（Send，不含 Posse Inject），视为用户自己开始的新交互，不误判为空闲。
	if s.LastInput().After(w.At) {
		s.clearCursorDone()
		return
	}

	t, err := d.store.Task(w.TaskID)
	if err != nil {
		s.clearCursorDone()
		return
	}
	if !d.cursorDoneTaskEligible(t) {
		s.clearCursorDone()
		return
	}

	if now.Sub(w.At) < cursorDoneQuiet {
		return
	}
	if lastOut := s.LastOutput(); !lastOut.IsZero() && now.Sub(lastOut) < cursorDoneQuiet {
		return
	}
	if !st.At.IsZero() && now.Sub(st.At) < cursorDoneQuiet {
		return
	}
	if !agent.CursorIdlePrompt(s.Screen()) {
		return
	}

	// 消费前再持锁核对一次，防止定时检查与 agentLoop 或新 hook 并发交错。
	s.mu.Lock()
	if s.cursorDone != w || s.agent != "cursor" || s.agentGen != w.gen || s.LastInput().After(w.At) || s.Agent().Status != agent.StatusWorking {
		s.mu.Unlock()
		return
	}
	s.cursorDone = cursorDoneWatch{}
	cur := s.Agent()
	s.mu.Unlock()

	idle := host.AgentState{
		Status:         agent.StatusIdle,
		Event:          "cursor-idle-fallback",
		SessionID:      cur.SessionID,
		TranscriptPath: cur.TranscriptPath,
		Title:          cur.Title,
		TitlePending:   cur.TitlePending,
		At:             time.Now(),
	}
	log.Printf("terminal %s: cursor idle fallback after task %s done (no stop hook)", s.ID, w.TaskID)
	d.setAgentFor(s, idle, w.gen)
}

func (d *Daemon) cursorDoneTaskEligible(t store.Task) bool {
	if t.Status == store.TaskDone || t.Status == store.TaskFailed {
		return true
	}
	// 普通任务在 taskDone 后被 taskSend 立即重开成 running，只要追加消息尚未提醒送达，worker 仍停在上一轮结束后的空闲框。
	if t.DiscussionID == "" && t.Status == store.TaskRunning {
		msgs, err := d.store.Inbox(store.Inbox{TaskID: t.ID, Unread: true, Unnudged: true})
		return err == nil && len(msgs) > 0
	}
	return false
}
