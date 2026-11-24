package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/store"
	"posse/internal/term"
)

// 单条消息同时 @ 两位成员：共用同一目录时后一位自动 held；不同目录时并行启动；上下文都写出全部 @ 对象。
func TestDiscussionMultiTargetDispatch(t *testing.T) {
	d, w, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	b := addMember(t, d, disc, "Bob", "codex")

	// 共用同一工作区目录：同时 @ Alice 和 Bob，Alice 启动，Bob 进入 held
	r := call(t, d.discPost, map[string]any{
		"discussion": disc.Key,
		"body":       "evaluate together",
		"targets":    []string{a.ID, b.ID},
		"clientId":   "c-same-dir",
	}).(discPostResult)
	if len(r.Executions) != 2 || len(r.Message.TargetList()) != 2 {
		t.Fatalf("multi post result: %+v", r)
	}
	xa := waitExec(t, d, r.Executions[0].ID, store.ExecRunning)
	xb := waitExec(t, d, r.Executions[1].ID, store.ExecHeld)
	if !strings.HasPrefix(xb.Reason, "concurrent:") {
		t.Fatalf("expected concurrent hold, got %q", xb.Reason)
	}

	// 读取 Alice 的上下文：头里同时标注 @Alice, @Bob
	page := call(t, d.discRead, map[string]any{"execution": xa.ID, "caller": xa.Worker}).(store.ContextPage)
	if !strings.Contains(page.Text, "to @Alice, @Bob") {
		t.Fatalf("expected both targets in context, got:\n%s", page.Text)
	}

	// 不同目录（新建独立目录工作区给 Bob）：两者并行进入 running
	dir2 := t.TempDir()
	w2 := store.Workspace{ID: store.NewID("w_"), ProjectID: w.ProjectID, Name: "other", Path: dir2, Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddWorkspace(w2); err != nil {
		t.Fatal(err)
	}
	c := call(t, d.discMemberAdd, map[string]any{"discussion": disc.Key, "name": "Carol", "agent": "codex", "workspace": w2.ID}).(store.Member)
	// 先把 Alice 汇报完成、Bob 取消，清空占用
	call(t, d.discAck, map[string]any{"execution": xa.ID, "from": 0, "to": page.Total, "caller": xa.Worker})
	call(t, d.taskDone, map[string]any{"task": xa.TaskID, "execution": xa.ID, "body": "alice done", "caller": xa.Worker})
	call(t, d.discCancel, map[string]any{"execution": xb.ID})
	// Alice 的 worker 设回 idle 才能接下一轮
	if s := d.liveSession(xa.Worker); s != nil {
		d.setAgent(s, term.AgentState{Status: agent.StatusIdle})
	}

	r2 := call(t, d.discPost, map[string]any{
		"discussion": disc.Key,
		"body":       "parallel run",
		"targets":    []string{a.ID, c.ID},
		"clientId":   "c-parallel",
	}).(discPostResult)
	if len(r2.Executions) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(r2.Executions))
	}
	waitExec(t, d, r2.Executions[0].ID, store.ExecRunning)
	waitExec(t, d, r2.Executions[1].ID, store.ExecRunning)
}

// 实时进度（discView.Progress）和按轮次切分的过程摘要（discussion.execution.trace）。
func TestDiscussionProgressAndTrace(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")

	// 准备模拟会话文件，写入第 1 轮之前的旧记录和本轮记录
	trPath := filepath.Join(t.TempDir(), "s1.jsonl")
	pre := `{"type":"assistant","timestamp":"2026-11-24T08:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"old","name":"Read","input":{"file_path":"/tmp/old.go"}}]}}` + "\n"
	if err := os.WriteFile(trPath, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	r1 := postTo(t, d, disc, "round one", a.ID, "c-r1")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	s := d.liveSession(x1.Worker)
	if s == nil {
		t.Fatal("missing worker session")
	}
	d.setAgent(s, term.AgentState{
		Status:         agent.StatusWaiting,
		SessionID:      "s1",
		TranscriptPath: trPath,
		ToolName:       "Bash",
		ToolInput:      "go test ./internal/store",
		Prompt:         `{"approval":{"tool":"Bash","summary":"go test ./internal/store"}}`,
		Subagents:      []string{"sub1"},
	})

	// discShow 带出本机活跃执行的实时进度
	v := call(t, d.discShow, map[string]any{"discussion": disc.Key, "limit": 20}).(discView)
	p, ok := v.Progress[x1.ID]
	if !ok || p.Tool != "Bash" || p.ToolInput != "go test ./internal/store" || p.Subagents != 1 {
		t.Fatalf("unexpected progress: %+v", v.Progress)
	}

	// 完成第 1 轮后开启第 2 轮，并往会话文件追加第 2 轮的工具调用
	page1 := call(t, d.discRead, map[string]any{"execution": x1.ID, "caller": x1.Worker}).(store.ContextPage)
	call(t, d.discAck, map[string]any{"execution": x1.ID, "from": 0, "to": page1.Total, "caller": x1.Worker})
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "execution": x1.ID, "body": "done 1", "caller": x1.Worker})
	d.setAgent(s, term.AgentState{Status: agent.StatusIdle, SessionID: "s1", TranscriptPath: trPath, Subagents: []string{}})

	r2 := postTo(t, d, disc, "round two", a.ID, "c-r2")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	round2Line := `{"type":"assistant","timestamp":"2026-11-24T08:05:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c2","name":"Bash","input":{"command":"go vet ./..."}}]}}` + "\n"
	f, err := os.OpenFile(trPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(round2Line)
	f.Close()

	// 第 2 轮进行中查过程：按复用 worker 记录的 start_offset 切分，只看到第 2 轮的 go vet，不混入第 1 轮的 old.go
	tr := call(t, d.discExecTrace, map[string]any{"execution": x2.ID}).(store.ExecTrace)
	if tr.EventCount != 1 || len(tr.Summary.Events) != 1 || tr.Summary.Events[0].Detail != "go vet ./..." {
		t.Fatalf("round 2 live trace: %+v", tr)
	}

	// 第 2 轮完成后异步固化，discShow 返回 TraceCount
	page2 := call(t, d.discRead, map[string]any{"execution": x2.ID, "caller": x2.Worker}).(store.ContextPage)
	call(t, d.discAck, map[string]any{"execution": x2.ID, "from": 0, "to": page2.Total, "caller": x2.Worker})
	call(t, d.taskDone, map[string]any{"task": x2.TaskID, "execution": x2.ID, "body": "done 2", "caller": x2.Worker})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := d.store.ExecTrace(x2.ID); err == nil && got.Finalized && got.EventCount == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	v2 := call(t, d.discShow, map[string]any{"discussion": disc.Key, "limit": 20}).(discView)
	foundCount := 0
	for _, x := range v2.Executions {
		if x.ID == x2.ID {
			foundCount = x.TraceCount
		}
	}
	if foundCount != 1 {
		t.Fatalf("expected finalized traceCount=1 in discShow, got %d", foundCount)
	}
}

// 局域网协作：多目标支持、多意图核对、旧房主拒绝多目标、不能看他人电脑的执行过程。
func TestCollabMultiTargetAndTracePrivacy(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	// 房主放开自己的 Agent 给成员，成员也加一个自己的 Agent
	alice := call(t, h.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Alice", "agent": "claude", "access": "all"}).(store.Member)
	waitFor(t, "alice synced to member", func() bool {
		_, err := m.store.Member(alice.ID)
		return err == nil
	})
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)

	// 成员发一条同时 @ Alice（房主放开）和 Bob（自己）的消息
	res := call(t, m.discPost, map[string]any{
		"discussion": p.disc.Key,
		"body":       "both look at this",
		"targets":    []string{alice.ID, bob.ID},
		"clientId":   "c-collab-multi",
	}).(discPostResult)
	if len(res.Executions) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(res.Executions))
	}
	// 房主的 Alice 和成员的 Bob 分别在各自电脑启动
	xa := waitExec(t, h, res.Executions[0].ID, store.ExecRunning)
	waitExec(t, m, res.Executions[1].ID, store.ExecRunning)

	// 成员不能看房主 Alice 的执行过程（隐私边界）
	if _, err := callErr(m.discExecTrace, map[string]any{"execution": xa.ID}); err == nil {
		t.Fatal("member should not be able to read host execution trace")
	}

	// 旧版房主（没有 FeatureMultiTarget）拒绝多目标，不静默丢弃第 2 个目标
	if err := m.store.SetHostFeatures(p.disc.Key, []string{store.FeatureChoices}); err != nil {
		t.Fatal(err)
	}
	if _, err := callErr(m.discPost, map[string]any{
		"discussion": p.disc.Key,
		"body":       "old host",
		"targets":    []string{alice.ID, bob.ID},
		"clientId":   "c-old-host",
	}); err == nil || !strings.Contains(err.Error(), "older Posse") {
		t.Fatalf("expected older Posse error, got %v", err)
	}
}
