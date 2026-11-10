package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/host"
	"posse/internal/store"
)

// stripIdle 去掉随时间变化的 idleMs，便于比较两次生成的结果。
func stripIdle(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	json.Unmarshal(b, &out)
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			delete(v, "idleMs")
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(out)
	return out
}

// 列表接口批量查询，结果要与逐条查询的结果一致。
func TestListsMatchPerItemLookups(t *testing.T) {
	ctx := context.Background()
	d, w := newTestDaemon(t)
	p2 := store.Project{ID: store.NewID("p_"), Name: "other", Path: d.cfg.Home + "/other", Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddProject(p2); err != nil {
		t.Fatal(err)
	}
	empty := store.Project{ID: store.NewID("p_"), Name: "empty", Path: d.cfg.Home + "/empty", Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddProject(empty); err != nil {
		t.Fatal(err)
	}
	var ws2 []store.Workspace
	for _, name := range []string{"main", "side"} {
		x := store.Workspace{ID: store.NewID("w_"), ProjectID: p2.ID, Name: name, Path: d.cfg.Home, Kind: "folder", CreatedAt: time.Now()}
		if err := d.store.AddWorkspace(x); err != nil {
			t.Fatal(err)
		}
		ws2 = append(ws2, x)
	}
	for _, ws := range []store.Workspace{w, w, ws2[1]} {
		if _, err := d.startTerminal(ws, []string{"/bin/sh", "-c", "sleep 30"}, "", 80, 24); err != nil {
			t.Fatal(err)
		}
	}
	call(t, d.taskStart, map[string]any{"workspace": ws2[0].ID, "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "a"})
	call(t, d.taskStart, map[string]any{"workspace": w.ID, "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "b"})
	gone := store.Task{ID: store.NewID("k_"), Title: "gone", Spec: "x", WorkspaceID: "w_missing", Worker: "t_missing", Status: store.TaskRunning, CreatedAt: time.Now()}
	if err := d.store.AddTask(gone); err != nil {
		t.Fatal(err)
	}

	ps, _ := d.store.Projects()
	var wantProjects []projectInfo
	for _, p := range ps {
		ws, _ := d.store.Workspaces(p.ID)
		wantProjects = append(wantProjects, projectInfo{Project: p, Workspaces: len(ws)})
	}
	if got := call(t, d.projectList, nil); !reflect.DeepEqual(stripIdle(t, got), stripIdle(t, wantProjects)) {
		t.Fatalf("project.list = %+v, want %+v", got, wantProjects)
	}

	all, _ := d.store.Workspaces("")
	var wantWS []workspaceInfo
	for _, x := range all {
		wantWS = append(wantWS, d.workspaceInfo(ctx, x, false))
	}
	if got := call(t, d.workspaceList, nil); !reflect.DeepEqual(stripIdle(t, got), stripIdle(t, wantWS)) {
		t.Fatalf("workspace.list differs\n got %v\nwant %v", stripIdle(t, got), stripIdle(t, wantWS))
	}

	ts, _ := d.store.Tasks(store.TaskFilter{})
	questions, _ := d.store.OpenQuestions()
	var wantTasks []taskInfo
	for _, x := range ts {
		wantTasks = append(wantTasks, d.taskInfo(x, questions, nil))
	}
	if len(wantTasks) != 3 {
		t.Fatalf("got %d tasks", len(wantTasks))
	}
	if got := call(t, d.taskList, map[string]any{"all": true}); !reflect.DeepEqual(stripIdle(t, got), stripIdle(t, wantTasks)) {
		t.Fatalf("task.list differs\n got %v\nwant %v", stripIdle(t, got), stripIdle(t, wantTasks))
	}
}

// 用 grok 开的终端里，用户自己启动了 claude：agent 换成 claude，状态按 Claude 的标题判断。
func TestDetectForegroundAgent(t *testing.T) {
	d, ws := newTestDaemon(t)
	script := `exec -a claude /bin/sh -c 'printf "\033]0;✳ hi\007"; sleep 0.3; printf "\033]0;◑ work\007"; sleep 30'`
	s, err := d.startTerminal(ws, []string{"/bin/zsh", "-f", "-c", script}, "grok", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info := d.terminalInfo(s)
		if info.Agent == "claude" && info.AgentState != nil && info.AgentState.Status == agent.StatusWorking {
			if info.AgentState.SessionID != "" {
				t.Fatalf("kept grok's session %q", info.AgentState.SessionID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent %q state %+v", info.Agent, info.AgentState)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 经同名脚本手动启动的 agent 带着 hooks：收到 hook 就按它记 agent，状态以 hooks 为准。
func TestHookMarksManualAgent(t *testing.T) {
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, "grok", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.hooked = false
	s.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"terminalId": s.ID, "agent": "claude", "event": "UserPromptSubmit", "payload": map[string]any{"session_id": "sid"}})
	if _, err := d.hookEvent(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if s.agentName() != "claude" || !s.isHooked() {
		t.Fatalf("agent %q hooked %v", s.agentName(), s.isHooked())
	}
	if st := s.Agent(); st.Status != agent.StatusWorking || st.SessionID != "sid" {
		t.Fatalf("state %+v", st)
	}
}

// agent 退出（SessionEnd）后终端退回普通 shell：不再有 agent，也不再给出最后的状态。
func TestSessionEndClearsAgent(t *testing.T) {
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []map[string]any{
		{"terminalId": s.ID, "agent": "claude", "event": "UserPromptSubmit", "payload": map[string]any{"session_id": "sid"}},
		{"terminalId": s.ID, "agent": "claude", "event": "SessionEnd", "payload": map[string]any{"session_id": "sid", "reason": "prompt_input_exit"}},
	} {
		raw, _ := json.Marshal(ev)
		if _, err := d.hookEvent(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	info := d.terminalInfo(s)
	if info.Agent != "" || info.AgentState != nil {
		t.Fatalf("agent %q state %+v", info.Agent, info.AgentState)
	}
	// terminal wait --for status 仍然等得到退出
	if st := s.Agent(); st.Status != agent.StatusExited {
		t.Fatalf("state %+v", st)
	}
}

// 别的会话（Codex 子 agent、嵌套启动的 agent）报的 SessionEnd 不摘掉终端里还在跑的 agent。
func TestOtherSessionEndKeepsAgent(t *testing.T) {
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hook(t, d, s.ID, "UserPromptSubmit", map[string]any{"session_id": "main"})
	hook(t, d, s.ID, "SessionEnd", map[string]any{"session_id": "child", "reason": "prompt_input_exit"})
	if info := d.terminalInfo(s); info.Agent != "claude" || s.Agent().Status != agent.StatusWorking {
		t.Fatalf("agent %q state %+v", info.Agent, s.Agent())
	}
	hook(t, d, s.ID, "SessionEnd", map[string]any{"session_id": "main", "reason": "prompt_input_exit"})
	if info := d.terminalInfo(s); info.Agent != "" || s.Agent().Status != agent.StatusExited {
		t.Fatalf("agent %q state %+v", info.Agent, s.Agent())
	}
}

// 后台子 agent 还在跑时主线程停下：仍是工作中，等它结束、Claude 开新一轮再停下才是完成。
// 事件顺序照 Claude Code 2.1.306 实测（后台子 agent 不发 SubagentStart）。
func TestBackgroundSubagentKeepsWorking(t *testing.T) {
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(event string, payload map[string]any) {
		t.Helper()
		payload["session_id"] = "sid"
		raw, _ := json.Marshal(map[string]any{"terminalId": s.ID, "agent": "claude", "event": event, "payload": payload})
		if _, err := d.hookEvent(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	running := []map[string]any{{"id": "a1", "type": "subagent", "status": "running"}}
	hook("UserPromptSubmit", map[string]any{})
	hook("Stop", map[string]any{"last_assistant_message": "launched", "background_tasks": running})
	if st := s.Agent(); st.Status != agent.StatusWorking || !agent.Parked(st) || st.Message != "launched" {
		t.Fatalf("parked state %+v", st)
	}
	// 标题兜底（Esc 后标题回到 ✳）也不把它记成完成
	d.setAgent(s, host.AgentState{Status: agent.StatusIdle, Event: "title", At: time.Now()})
	if st := s.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("title made it idle: %+v", st)
	}
	// 子 agent 里的工具事件不动主线程的状态
	hook("PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "a1"})
	hook("SubagentStop", map[string]any{"agent_id": "a1", "background_tasks": running})
	if st := s.Agent(); st.Status != agent.StatusWorking || len(st.Subagents) != 0 {
		t.Fatalf("after SubagentStop %+v", st)
	}
	hook("UserPromptSubmit", map[string]any{"prompt": "<task-notification>"})
	hook("Stop", map[string]any{"last_assistant_message": "finished", "background_tasks": []any{}})
	if st := s.Agent(); st.Status != agent.StatusIdle {
		t.Fatalf("final state %+v", st)
	}
}

// 最后一个子 agent 结束后迟迟没有新一轮（被停掉了）：过一会儿按完成算。
func TestStoppedSubagentEndsTurn(t *testing.T) {
	old := subagentGrace
	subagentGrace = 50 * time.Millisecond
	t.Cleanup(func() { subagentGrace = old })
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []map[string]any{
		{"event": "Stop", "payload": map[string]any{"session_id": "sid", "background_tasks": []map[string]any{{"id": "a1", "type": "subagent", "status": "running"}}}},
		{"event": "SubagentStop", "payload": map[string]any{"session_id": "sid", "agent_id": "a1"}},
	} {
		ev["terminalId"], ev["agent"] = s.ID, "claude"
		raw, _ := json.Marshal(ev)
		if _, err := d.hookEvent(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	if st := s.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("state %+v", st)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Agent().Status != agent.StatusIdle {
		if time.Now().After(deadline) {
			t.Fatalf("still %+v", s.Agent())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 续接的 codex 在第一轮对话前只有会话 ID、没有状态：列表也要带上会话 ID，技能与工具面板凭它找会话文件。
func TestTerminalInfoKeepsSessionWithoutStatus(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "sleep 30"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if info := d.terminalInfo(s); info.AgentState != nil {
		t.Fatalf("agent state before any session = %+v, want nil", info.AgentState)
	}
	s.SetAgent(host.AgentState{SessionID: "sid-1", At: time.Now()})
	info := d.terminalInfo(s)
	if info.AgentState == nil || info.AgentState.SessionID != "sid-1" {
		t.Fatalf("agent state = %+v, want session sid-1", info.AgentState)
	}
	if info.Unread {
		t.Fatal("unread without status")
	}
}
