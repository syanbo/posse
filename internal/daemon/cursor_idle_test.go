package daemon

import (
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/host"
	"posse/internal/store"
)

func setFastCursorDoneQuiet(t *testing.T, d time.Duration) {
	t.Helper()
	old := cursorDoneQuiet
	cursorDoneQuiet = d
	t.Cleanup(func() { cursorDoneQuiet = old })
}

func emitToScreen(t *testing.T, s *session, text string) {
	t.Helper()
	// 测试里的假 worker 跑 /bin/cat，回显写入的内容作为终端屏幕输出。
	if err := s.Inject([]byte(text)); err != nil {
		t.Fatalf("inject screen text: %v", err)
	}
}

func TestDiscussionCursorIdleFallbackWithoutStop(t *testing.T) {
	setFastCursorDoneQuiet(t, 80*time.Millisecond)
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "CursorBot", "cursor")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	r2 := postTo(t, d, disc, "second", m.ID, "")
	s := d.liveSession(x1.Worker)

	// 模拟 Cursor 没配 ~/.cursor/hooks.json：只收到 postToolUse（working），之后不发 stop。
	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-1"}})
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "reply one"})
	// task done 自身的 shell 工具结束在汇报后才发 postToolUse。
	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-1"}})

	// 终端输出回到 Cursor 空闲输入提示，不发 stop hook。
	emitToScreen(t, s, "\r\n  → Add a follow-up\r\n  Auto | ctx 5%\r\n")
	waitScreen(t, s, "Add a follow-up")

	// 静默超过 cursorDoneQuiet 后，经手动/定时触发兜底：切回 idle 并放行第 2 轮。
	time.Sleep(110 * time.Millisecond)
	d.checkCursorDoneIdle(s)

	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	waitScreen(t, s, "--execution "+x2.ID)
	if st := s.Agent(); st.Status != agent.StatusIdle || st.Event != "cursor-idle-fallback" || st.SessionID != "cur-1" {
		t.Fatalf("unexpected agent state after fallback: %+v", st)
	}
	// 等 deliver 在 150ms 后补发的回车回显完成，避免回显刷新第 2 轮的 LastOutput。
	time.Sleep(200 * time.Millisecond)

	// 第 2 轮同样缺失 stop hook，第 3 轮仍能靠兜底连续放行。
	r3 := postTo(t, d, disc, "third", m.ID, "")
	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-1"}})
	readAll(t, d, x2)
	call(t, d.taskDone, map[string]any{"task": x2.TaskID, "caller": x2.Worker, "execution": x2.ID, "body": "reply two"})
	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-1"}})
	emitToScreen(t, s, "\r\n  → Add a follow-up\r\n")
	waitScreen(t, s, "Add a follow-up")
	time.Sleep(110 * time.Millisecond)
	d.checkCursorDoneIdle(s)
	x3 := waitExec(t, d, r3.Execution.ID, store.ExecRunning)
	waitScreen(t, s, "--execution "+x3.ID)
}

func TestDiscussionCursorIdleFallbackRespectsBusyPrompt(t *testing.T) {
	setFastCursorDoneQuiet(t, 60*time.Millisecond)
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "CursorBot", "cursor")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	r2 := postTo(t, d, disc, "second", m.ID, "")
	s := d.liveSession(x1.Worker)

	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-busy"}})
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "reply one"})

	// 屏幕仍显示生成中的 ctrl+c to stop，不应放行。
	emitToScreen(t, s, "\r\n  → Add a follow-up                    ctrl+c to stop\r\n")
	waitScreen(t, s, "ctrl+c to stop")
	time.Sleep(90 * time.Millisecond)
	d.checkCursorDoneIdle(s)

	if st := s.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("prematurely fell back to idle while busy: %+v", st)
	}
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("premature dispatch while busy: %+v", x)
	}

	// 清屏后显示干净空闲框并静默，再检查则放行。
	if err := s.Clear(false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	emitToScreen(t, s, "\r\n  → Add a follow-up\r\n")
	waitScreen(t, s, "Add a follow-up")
	time.Sleep(90 * time.Millisecond)
	d.checkCursorDoneIdle(s)

	waitExec(t, d, r2.Execution.ID, store.ExecRunning)
}

func TestDiscussionCursorIdleFallbackAbortedByUserInput(t *testing.T) {
	setFastCursorDoneQuiet(t, 60*time.Millisecond)
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "CursorBot", "cursor")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	r2 := postTo(t, d, disc, "second", m.ID, "")
	s := d.liveSession(x1.Worker)

	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-user"}})
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "reply one"})

	// 用户在汇报后自己往终端里键入，取消兜底。
	time.Sleep(5 * time.Millisecond)
	if err := s.Send([]byte("manual prompt\r")); err != nil {
		t.Fatalf("send user input: %v", err)
	}
	emitToScreen(t, s, "\r\n  → Add a follow-up\r\n")
	waitScreen(t, s, "Add a follow-up")
	time.Sleep(90 * time.Millisecond)
	d.checkCursorDoneIdle(s)

	if st := s.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("fallback triggered despite user input: %+v", st)
	}
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("dispatched despite user input: %+v", x)
	}
}

func TestDiscussionCursorIdleFallbackDelayedPostToolUse(t *testing.T) {
	setFastCursorDoneQuiet(t, 80*time.Millisecond)
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "CursorBot", "cursor")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	s := d.liveSession(x1.Worker)

	// 模拟整轮没收到 beforeSubmitPrompt：taskDone 时状态还是 idle，15ms 后才收到 task done 的 postToolUse 变 working，
	// 随后第 2 条消息到达并排队。
	d.setAgent(s, host.AgentState{Status: agent.StatusIdle, Event: "stop", SessionID: "cur-delay", At: time.Now()})
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "reply one"})
	time.Sleep(15 * time.Millisecond)
	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-delay"}})
	r2 := postTo(t, d, disc, "second", m.ID, "")

	emitToScreen(t, s, "\r\n  → Add a follow-up\r\n")
	waitScreen(t, s, "Add a follow-up")
	time.Sleep(110 * time.Millisecond)
	d.checkCursorDoneIdle(s)

	waitExec(t, d, r2.Execution.ID, store.ExecRunning)
}

func TestTaskSendCursorIdleFallback(t *testing.T) {
	setFastCursorDoneQuiet(t, 60*time.Millisecond)

	d, w := newTestDaemon(t)
	// 用假终端启动任务，再通过 cursor hook 绑定 cursor 身份。
	info := call(t, d.taskStart, map[string]any{"workspace": w.ID, "title": "check", "spec": "do it", "command": []string{"/bin/cat"}}).(taskInfo)
	s := d.liveSession(info.Worker)
	call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "postToolUse", "payload": map[string]any{"session_id": "cur-task"}})

	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "done round 1"})
	// 汇报后立刻追问（任务立刻重新打开为 running，但消息尚未送达）。
	call(t, d.taskSend, map[string]any{"task": info.ID, "body": "follow up work"})

	emitToScreen(t, s, "\r\n  → Add a follow-up\r\n")
	waitScreen(t, s, "Add a follow-up")
	time.Sleep(90 * time.Millisecond)
	d.checkCursorDoneIdle(s)

	waitScreen(t, s, "posse task check "+info.ID)
}
