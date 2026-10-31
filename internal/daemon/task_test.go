package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/store"
	"posse/internal/term"
)

func newTestDaemon(t *testing.T) (*Daemon, store.Workspace) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "hv")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "posse.db"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Home: home, Scrollback: 1 << 20}, store: st, terms: map[string]*session{}, tchanged: make(chan struct{})}
	t.Cleanup(func() {
		d.closeAll()
		st.Close()
		os.RemoveAll(home)
	})
	p := store.Project{ID: store.NewID("p_"), Name: "proj", Path: home, Kind: "folder", CreatedAt: time.Now()}
	w := store.Workspace{ID: store.NewID("w_"), ProjectID: p.ID, Name: "main", Path: home, Kind: "main", CreatedAt: time.Now()}
	if err := st.AddProject(p); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWorkspace(w); err != nil {
		t.Fatal(err)
	}
	return d, w
}

func call(t *testing.T, h func(context.Context, json.RawMessage) (any, error), params map[string]any) any {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := h(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func waitScreen(t *testing.T, s *session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(s.Lines(), "\n"), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; screen:\n%s", want, strings.Join(s.Lines(), "\n"))
}

// 派发方是 agent 时，消息在它空闲时以一行提醒输入它的终端；工作中先不打扰。
func TestDeliverNudgesIdleCoordinator(t *testing.T) {
	d, w := newTestDaemon(t)
	// cat 在 PTY 里会回显收到的输入，用它充当派发方 agent
	coord, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	d.setAgent(coord, term.AgentState{Status: agent.StatusWorking})
	info := call(t, d.taskStart, map[string]any{"caller": coord.ID, "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "x"}).(taskInfo)
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "all good"})
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(strings.Join(coord.Lines(), "\n"), "Posse:") {
		t.Fatal("nudged a working agent")
	}
	d.setAgent(coord, term.AgentState{Status: agent.StatusIdle})
	waitScreen(t, coord, "Posse: "+info.ID+" finished. Run `posse task inbox` to read.")
	time.Sleep(300 * time.Millisecond)
	// PTY 回显一次、cat 再输出一次，所以一次提醒在屏幕上出现两遍
	once := strings.Count(strings.Join(coord.Lines(), "\n"), "Posse:")

	// 已提醒过的消息不再重复提醒
	d.setAgent(coord, term.AgentState{Status: agent.StatusWorking})
	d.setAgent(coord, term.AgentState{Status: agent.StatusIdle})
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(strings.Join(coord.Lines(), "\n"), "Posse:"); n != once {
		t.Fatalf("nudged again: %d occurrences, %d after the first nudge", n, once)
	}
}

// 主线程已经停在输入框、只是后台子 agent 还在跑：状态是工作中，但照常提醒。
func TestDeliverNudgesParkedCoordinator(t *testing.T) {
	d, w := newTestDaemon(t)
	coord, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	d.setAgent(coord, term.AgentState{Status: agent.StatusWorking})
	info := call(t, d.taskStart, map[string]any{"caller": coord.ID, "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "x"}).(taskInfo)
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "all good"})
	d.setAgent(coord, term.AgentState{Status: agent.StatusIdle, Event: "Stop", Subagents: []string{"a1"}})
	if st := coord.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("state %+v", st)
	}
	waitScreen(t, coord, "Posse: "+info.ID+" finished.")
}

func TestWorkerPrompt(t *testing.T) {
	task := store.Task{ID: "k_1", Spec: "do it"}
	p := workerPrompt(task, store.Workspace{Kind: "worktree", Branch: "feat", BaseRef: "main"})
	for _, want := range []string{"posse task ask k_1", "posse task done k_1 --summary", "the user", "branch feat, created from main", "edit only what it lets you change", "=== TASK ===\ndo it"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

// 汇报过的任务可以追加：同一个 worker 被提醒接着做、再汇报一次；worker 的 agent 不在了就只能重派。
func TestSendReopensReportedTask(t *testing.T) {
	d, w := newTestDaemon(t)
	info := call(t, d.taskStart, map[string]any{"workspace": w.ID, "command": []string{"/bin/cat"}, "spec": "x"}).(taskInfo)
	worker := d.liveSession(info.Worker)
	worker.mu.Lock()
	worker.agent = "claude"
	worker.mu.Unlock()
	d.setAgent(worker, term.AgentState{Status: agent.StatusWorking})
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "first", "failed": true})
	res := call(t, d.taskSend, map[string]any{"task": info.ID, "body": "also do y"}).(map[string]string)
	if got, _ := d.store.Task(info.ID); got.Status != store.TaskRunning || got.EndedAt != nil || res["reopened"] != store.TaskFailed {
		t.Fatalf("after send: %+v %v", got, res)
	}
	// 重新打开前没读的汇报照旧按汇报时的结果给出
	items := call(t, d.taskWait, map[string]any{}).([]inboxItem)
	if len(items) != 1 || items[0].Kind != "done" || items[0].Status != store.TaskFailed {
		t.Fatalf("inbox after reopen = %+v", items)
	}
	// 已经在运行中：只追加消息，不算重新打开
	if res := call(t, d.taskSend, map[string]any{"task": info.ID, "body": "and z"}).(map[string]string); res["reopened"] != "" {
		t.Fatalf("second send reopened: %v", res)
	}
	d.setAgent(worker, term.AgentState{Status: agent.StatusIdle})
	waitScreen(t, worker, "Posse: your coordinator sent a message for task "+info.ID)
	notes := call(t, d.taskCheck, map[string]any{"task": info.ID, "caller": info.Worker}).([]store.Message)
	if len(notes) != 2 || notes[0].Body != "also do y" {
		t.Fatalf("check = %+v", notes)
	}
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "second"})
	if got, _ := d.store.Task(info.ID); got.Status != store.TaskDone || got.Summary != "second" {
		t.Fatalf("after second done: %+v", got)
	}

	// agent 退出、终端回到 shell 后不再接活
	worker.mu.Lock()
	worker.agent = ""
	worker.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"task": info.ID, "body": "more"})
	if _, err := d.taskSend(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "task retry") {
		t.Fatalf("send to a gone worker = %v", err)
	}
	if got, _ := d.store.Task(info.ID); got.Status != store.TaskDone {
		t.Fatalf("status changed to %s", got.Status)
	}
}

// 有 hook 的 agent 由 hook 交提醒：工具做完时随结果进上下文，本轮结束时让它接着处理，都不往终端里打字。
func TestHookDeliversNudge(t *testing.T) {
	d, w := newTestDaemon(t)
	coord, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(event string) hookEventResult {
		raw, _ := json.Marshal(map[string]any{"terminalId": coord.ID, "agent": "claude", "event": event, "payload": map[string]any{"session_id": "s1"}})
		res, err := d.hookEvent(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.(hookEventResult)
	}
	hook("UserPromptSubmit")
	info := call(t, d.taskStart, map[string]any{"caller": coord.ID, "command": []string{"/bin/cat"}, "spec": "x"}).(taskInfo)
	worker := d.liveSession(info.Worker)
	worker.mu.Lock()
	worker.agent = "claude"
	worker.mu.Unlock()
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "ok"})
	want := "Posse: " + info.ID + " finished. Run `posse task inbox` to read."
	if res := hook("PostToolUse"); res.Deliver != want || res.Status != agent.StatusWorking {
		t.Fatalf("PostToolUse = %+v", res)
	}
	// 交过的不再交，Stop 照常空闲
	if res := hook("Stop"); res.Deliver != "" || res.Status != agent.StatusIdle {
		t.Fatalf("Stop after delivery = %+v", res)
	}
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(strings.Join(coord.Lines(), "\n"), "Posse:") {
		t.Fatal("typed into the terminal although the hook delivered")
	}

	// 本轮结束时才到的：Stop 交给它并让它接着做，不记空闲
	hook("UserPromptSubmit")
	call(t, d.taskSend, map[string]any{"task": info.ID, "caller": coord.ID, "body": "more"})
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "again"})
	if res := hook("Stop"); res.Deliver != want || res.Status != agent.StatusWorking {
		t.Fatalf("Stop = %+v", res)
	}
	if st := coord.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("state after delivering at Stop: %+v", st)
	}
}

// 本轮结束时交了提醒，agent 却没接着做（之后没有 hook 事件）：退回提醒、记回空闲，改为往终端里打字。
func TestStopDeliveryFallsBack(t *testing.T) {
	old := stopDeliveryGrace
	stopDeliveryGrace = 300 * time.Millisecond
	t.Cleanup(func() { stopDeliveryGrace = old })
	d, w := newTestDaemon(t)
	coord, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(event string) hookEventResult {
		raw, _ := json.Marshal(map[string]any{"terminalId": coord.ID, "agent": "claude", "event": event, "payload": map[string]any{"session_id": "s1"}})
		res, err := d.hookEvent(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.(hookEventResult)
	}
	hook("UserPromptSubmit")
	info := call(t, d.taskStart, map[string]any{"caller": coord.ID, "command": []string{"/bin/cat"}, "spec": "x"}).(taskInfo)
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "ok"})
	if res := hook("Stop"); res.Deliver == "" {
		t.Fatalf("Stop = %+v", res)
	}
	waitScreen(t, coord, "Posse: "+info.ID+" finished.")
	if st := coord.Agent(); st.Status != agent.StatusIdle {
		t.Fatalf("state %+v", st)
	}

	// 接着做了（有下一个 hook 事件）就不退回
	hook("UserPromptSubmit")
	worker := d.liveSession(info.Worker)
	worker.mu.Lock()
	worker.agent = "claude"
	worker.mu.Unlock()
	call(t, d.taskSend, map[string]any{"task": info.ID, "caller": coord.ID, "body": "more"})
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "again"})
	time.Sleep(300 * time.Millisecond) // PTY 回显一次、cat 再输出一次，等两遍都到了再数
	before := strings.Count(strings.Join(coord.Lines(), "\n"), "Posse:")
	if res := hook("Stop"); res.Deliver == "" {
		t.Fatalf("second Stop = %+v", res)
	}
	hook("PostToolUse")
	time.Sleep(600 * time.Millisecond)
	if n := strings.Count(strings.Join(coord.Lines(), "\n"), "Posse:"); n != before {
		t.Fatalf("typed again although the agent continued: %d -> %d", before, n)
	}
}
