package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/term"
)

func TestNativeChat(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	cols, rows := tm.Size()

	dir := t.TempDir()
	transcript := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ask := `{"z":1,"a":2,"questions":[{"question":"Pick <one>"}]}`
	payload := `{"session_id":"sess-ask","transcript_path":` + strconv.Quote(transcript) +
		`,"tool_name":"AskUserQuestion","tool_input":` + ask + `}`
	st, ok := agent.MapEvent("claude", "PreToolUse", json.RawMessage(payload))
	if !ok {
		t.Fatal("ask event ignored")
	}
	tm.SetAgent(st)
	row := phoneAgent(t, p)
	if row["state"] != "waiting" || row["toolName"] != "AskUserQuestion" || row["interactivePrompt"] != ask {
		t.Fatalf("ask row %v", row)
	}
	ps := row["providerSession"].(map[string]any)
	if ps["key"] != "session_id" || ps["id"] != "sess-ask" || ps["transcriptPath"] != transcript {
		t.Fatalf("providerSession %v", ps)
	}
	// 答题卡还在时 Claude 会再发一条不带 tool_input 的 permission_prompt，卡片要留着。
	note, ok := agent.MapEvent("claude", "Notification", json.RawMessage(`{"notification_type":"permission_prompt"}`))
	if !ok || note.Status != agent.StatusWaiting {
		t.Fatalf("permission_prompt %+v ok=%v", note, ok)
	}
	tm.SetAgent(note)
	row = phoneAgent(t, p)
	if row["state"] != "waiting" || row["toolName"] != "AskUserQuestion" || row["interactivePrompt"] != ask {
		t.Fatalf("permission_prompt cleared the card %v", row)
	}

	st, ok = agent.MapEvent("claude", "PermissionRequest", json.RawMessage(
		`{"tool_name":"Bash","tool_input":{"command":["/bin/zsh","-lc","go test ./..."]}}`))
	if !ok {
		t.Fatal("permission ignored")
	}
	tm.SetAgent(st)
	row = phoneAgent(t, p)
	if row["toolName"] != "Bash" || row["toolInput"] != "go test ./..." ||
		row["interactivePrompt"] != `{"approval":{"tool":"Bash","summary":"go test ./..."}}` {
		t.Fatalf("approval row %v", row)
	}
	if row["providerSession"].(map[string]any)["transcriptPath"] != transcript {
		t.Fatal("transcript path not inherited across events")
	}

	// 非 waiting 的更新即使自己还带着工具字段，手机上也要清掉；会话 ID 留下。
	tm.SetAgent(term.AgentState{Status: "working", ToolName: "Bash", ToolInput: "stale", Prompt: "stale"})
	row = phoneAgent(t, p)
	if row["state"] != "working" || row["toolName"] != nil || row["toolInput"] != nil || row["interactivePrompt"] != nil {
		t.Fatalf("cleared row %v", row)
	}
	if row["providerSession"].(map[string]any)["id"] != "sess-ask" {
		t.Fatalf("session dropped %v", row["providerSession"])
	}
	// 另一条终端没有报过路径。空路径要走磁盘兜底，对不上就省略 transcriptPath，不能沿用上一条的。
	tm2, err := term.Start(term.Options{ID: "t_2", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm2.Close(time.Second)
	absent := "posse-test-absent-7c1e0b9a-4d22-4b0e-9c11-000000000001"
	tm2.SetAgent(term.AgentState{Status: "waiting", SessionID: absent, ToolName: "Read", Prompt: "x"})
	host.mu.Lock()
	host.terms["t_2"] = tm2
	host.order = append(host.order, "t_2")
	host.mu.Unlock()
	row = phoneAgentSession(t, p, absent)
	ps = row["providerSession"].(map[string]any)
	if ps["id"] != absent || row["toolName"] != "Read" {
		t.Fatalf("fallback row %v", row)
	}
	if _, ok := ps["transcriptPath"]; ok {
		t.Fatalf("unknown session invented a path %v", ps)
	}

	tm.SetAgent(term.AgentState{Status: "waiting", SessionID: "sess-1", TranscriptPath: transcript, ToolName: "Bash", Prompt: "p1"})
	tabsID := p.call("session.tabs.subscribe", map[string]any{"worktree": "id:p1::w1"})
	snap := result(t, p.next(tabsID))
	upd := result(t, p.next(tabsID))
	if snap["type"] != "snapshot" || upd["type"] != "updated" {
		t.Fatalf("tabs open %v %v", snap["type"], upd["type"])
	}
	if tabAgent(t, upd)["interactivePrompt"] != "p1" {
		t.Fatalf("tabs prompt %v", tabAgent(t, upd))
	}
	ver := upd["snapshotVersion"].(float64)
	tm.SetAgent(term.AgentState{Status: "waiting", SessionID: "sess-1", TranscriptPath: transcript, ToolName: "Bash", Prompt: "p2"})
	changed := waitTab(t, p, tabsID)
	if changed["type"] != "updated" || changed["snapshotVersion"].(float64) <= ver {
		t.Fatalf("prompt did not bump tabs %v", changed["snapshotVersion"])
	}
	if tabAgent(t, changed)["interactivePrompt"] != "p2" || tabAgent(t, changed)["providerSession"].(map[string]any)["id"] != "sess-1" {
		t.Fatalf("prompt update %v", tabAgent(t, changed))
	}
	ver = changed["snapshotVersion"].(float64)
	tm.SetAgent(term.AgentState{Status: "waiting", SessionID: "sess-2", TranscriptPath: transcript, ToolName: "Bash", Prompt: "p2"})
	changed = waitTab(t, p, tabsID)
	if changed["snapshotVersion"].(float64) <= ver || tabAgent(t, changed)["providerSession"].(map[string]any)["id"] != "sess-2" {
		t.Fatalf("session update %v", tabAgent(t, changed))
	}

	sendID := p.call("terminal.send", map[string]any{"terminal": "t_1", "text": "printf 'SETTLE_OK\\n'", "enter": true})
	send := result(t, p.next(sendID))["send"].(map[string]any)
	if send["accepted"] != true || send["writeSettlement"].(map[string]any)["outcome"] != "accepted" {
		t.Fatalf("send %v", send)
	}
	qID := p.call("terminal.send", map[string]any{
		"terminal": "t_1", "text": "QUERYSHOULDNOTSHOW", "enter": true, "inputKind": "query-reply",
	})
	q := result(t, p.next(qID))["send"].(map[string]any)
	if q["accepted"] != true || q["bytesWritten"] != float64(0) || q["writeSettlement"].(map[string]any)["outcome"] != "accepted" {
		t.Fatalf("query reply %v", q)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !tm.ContainsText("SETTLE_OK") {
		if time.Now().After(deadline) {
			t.Fatalf("no shell output %q", strings.Join(tm.Lines(), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tm.ContainsText("QUERYSHOULDNOTSHOW") {
		t.Fatal("query reply was written to the pty")
	}

	readPath := filepath.Join(dir, "read.jsonl")
	writeLines(t, readPath, claudeLine("a", "A"), claudeLine("b", "B"), claudeLine("c", "C"))
	page := result(t, p.next(p.call("nativeChat.readSession", map[string]any{
		"agent": "claude", "sessionId": "read-1", "transcriptPath": readPath, "limit": 1,
	})))
	if page["hasMore"] != true || msgText(t, page, 0) != "C" {
		t.Fatalf("read last %v", page)
	}
	prev := result(t, p.next(p.call("nativeChat.readSession", map[string]any{
		"agent": "claude", "sessionId": "read-1", "transcriptPath": readPath, "limit": 1,
		"beforeOffset": page["beforeOffset"],
	})))
	if msgText(t, prev, 0) != "B" || prev["beforeOffset"].(float64) >= page["beforeOffset"].(float64) {
		t.Fatalf("read prev %v", prev)
	}
	missing := p.next(p.call("nativeChat.readSession", map[string]any{
		"agent": "claude", "sessionId": "nope", "transcriptPath": filepath.Join(dir, "missing.jsonl"),
	}))
	if missing["ok"] != false || missing["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("missing %v", missing)
	}

	live := filepath.Join(dir, "live.jsonl")
	writeLines(t, live, claudeLine("m1", "one"), claudeLine("m2", "two"))
	sub := "claude:live:1"
	sid := p.call("nativeChat.subscribe", map[string]any{
		"agent": "claude", "sessionId": "live", "transcriptPath": live, "limit": 40, "subscriptionId": sub,
	})
	frame := result(t, p.next(sid))
	if frame["type"] != "snapshot" || frame["pending"] != nil || msgText(t, frame, 0) != "one" || msgText(t, frame, 1) != "two" {
		t.Fatalf("snapshot %v", frame)
	}
	appendLine(t, live, claudeLine("m3", "three"))
	app := waitFrame(t, p, sid, "appended")
	if msgText(t, app, 0) != "three" {
		t.Fatalf("appended %v", app)
	}
	writeLines(t, live, claudeLine("m9", "rewritten"))
	rep := waitFrame(t, p, sid, "replacement")
	if msgText(t, rep, 0) != "rewritten" {
		t.Fatalf("shrink %v", rep)
	}
	replaced := live + ".new"
	writeLines(t, replaced, claudeLine("m8", "inode"))
	if err := os.Rename(replaced, live); err != nil {
		t.Fatal(err)
	}
	rep = waitFrame(t, p, sid, "replacement")
	if msgText(t, rep, 0) != "inode" {
		t.Fatalf("inode %v", rep)
	}
	if c, r := tm.Size(); c != cols || r != rows {
		t.Fatalf("chat subscribe resized pty to %dx%d", c, r)
	}

	unsub := p.call("nativeChat.unsubscribe", map[string]any{"subscriptionId": sub})
	waitEnd(t, p, sid, unsub)
	appendLine(t, live, claudeLine("m-after", "after"))
	if m, ok := p.nextWithin(sid, 1500*time.Millisecond); ok {
		t.Fatalf("frame after unsubscribe %v", m)
	}

	pendingPath := filepath.Join(dir, "later.jsonl")
	pid := p.call("nativeChat.subscribe", map[string]any{
		"agent": "claude", "sessionId": "pending-1", "transcriptPath": pendingPath, "limit": 10,
		"subscriptionId": "pend", "capabilities": map[string]any{"transcriptPending": 1},
	})
	pend := result(t, p.next(pid))
	msgs, _ := pend["messages"].([]any)
	if pend["type"] != "snapshot" || pend["pending"] != true || len(msgs) != 0 {
		t.Fatalf("pending %v", pend)
	}
	writeLines(t, pendingPath, claudeLine("p1", "arrived"))
	arrived := waitFrame(t, p, pid, "snapshot")
	if arrived["pending"] == true || msgText(t, arrived, 0) != "arrived" {
		t.Fatalf("appear %v", arrived)
	}

	// 同一个 subscriptionId 的新订阅替换旧的：旧请求收到 end，新行只出现在新请求上。
	fileA := filepath.Join(dir, "a.jsonl")
	writeLines(t, fileA, claudeLine("a1", "alpha"))
	const same = "shared-sub"
	oldID := p.call("nativeChat.subscribe", map[string]any{
		"agent": "claude", "sessionId": "a", "transcriptPath": fileA, "limit": 10, "subscriptionId": same,
	})
	if result(t, p.next(oldID))["type"] != "snapshot" {
		t.Fatal("old snapshot missing")
	}
	newID := p.call("nativeChat.subscribe", map[string]any{
		"agent": "claude", "sessionId": "a", "transcriptPath": fileA, "limit": 10, "subscriptionId": same,
	})
	sawOldEnd := false
	sawNewSnap := false
	until := time.Now().Add(3 * time.Second)
	var aside []map[string]any
	for !sawOldEnd || !sawNewSnap {
		m, ok := p.nextAny(time.Until(until))
		if !ok {
			p.held = append(aside, p.held...)
			t.Fatalf("replace end=%v snap=%v", sawOldEnd, sawNewSnap)
		}
		switch m["id"] {
		case oldID:
			if result(t, m)["type"] == "end" {
				sawOldEnd = true
			}
		case newID:
			if result(t, m)["type"] == "snapshot" {
				sawNewSnap = true
			}
		default:
			aside = append(aside, m)
		}
	}
	p.held = append(aside, p.held...)
	appendLine(t, fileA, claudeLine("a2", "beta"))
	beta := waitFrame(t, p, newID, "appended")
	if msgText(t, beta, 0) != "beta" {
		t.Fatalf("replaced stream %v", beta)
	}
	if m, ok := p.nextWithin(oldID, 1500*time.Millisecond); ok {
		t.Fatalf("old subscription still live %v", m)
	}

	// 空的 subscriptionId 停掉这条连接上的对话流，不停标签流。
	stopAll := p.call("nativeChat.unsubscribe", map[string]any{"subscriptionId": ""})
	waitEnd(t, p, newID, stopAll)
	if !takeType(t, p, pid, "end", 3*time.Second) {
		t.Fatal("empty unsubscribe did not end the pending stream")
	}
	tm.SetAgent(term.AgentState{Status: "waiting", SessionID: "sess-3", TranscriptPath: transcript, ToolName: "Bash", Prompt: "p3"})
	tabDeadline := time.Now().Add(3 * time.Second)
	for {
		m, ok := p.nextWithin(tabsID, time.Until(tabDeadline))
		if !ok {
			t.Fatal("tabs subscription stopped with the chat streams")
		}
		if tabAgent(t, result(t, m))["interactivePrompt"] == "p3" {
			break
		}
	}
	if c, r := tm.Size(); c != cols || r != rows {
		t.Fatalf("pty resized to %dx%d", c, r)
	}

	tm.Close(time.Second)
	refID := p.call("terminal.send", map[string]any{"terminal": "t_1", "text": "echo no", "enter": true})
	ref := p.next(refID)
	if ref["ok"] != true {
		t.Fatalf("refused send returned an error %v", ref)
	}
	body := ref["result"].(map[string]any)["send"].(map[string]any)
	if body["accepted"] != false || body["writeSettlement"].(map[string]any)["outcome"] != "refused" {
		t.Fatalf("refused %v", body)
	}
}

func phoneAgent(t *testing.T, p *phone) map[string]any {
	t.Helper()
	return phoneAgents(t, p)[0]
}

func phoneAgentSession(t *testing.T, p *phone, sessionID string) map[string]any {
	t.Helper()
	for _, row := range phoneAgents(t, p) {
		ps, _ := row["providerSession"].(map[string]any)
		if ps["id"] == sessionID {
			return row
		}
	}
	t.Fatalf("no agent session %s", sessionID)
	return nil
}

func phoneAgents(t *testing.T, p *phone) []map[string]any {
	t.Helper()
	ps := result(t, p.next(p.call("worktree.ps", map[string]any{"limit": 100})))
	rows := ps["worktrees"].([]any)
	raw := rows[0].(map[string]any)["agents"].([]any)
	if len(raw) == 0 {
		t.Fatal("no agents")
	}
	out := make([]map[string]any, len(raw))
	for i, a := range raw {
		out[i] = a.(map[string]any)
	}
	return out
}

func tabAgent(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	tabs := frame["tabs"].([]any)
	tab := tabs[0].(map[string]any)
	ag, ok := tab["agentStatus"].(map[string]any)
	if !ok {
		t.Fatalf("no agentStatus in %v", tab)
	}
	return ag
}

func waitTab(t *testing.T, p *phone, id string) map[string]any {
	t.Helper()
	m, ok := p.nextWithin(id, 3*time.Second)
	if !ok {
		t.Fatal("tabs subscription did not update")
	}
	r := result(t, m)
	if r["type"] != "updated" {
		t.Fatalf("tabs frame %v", r["type"])
	}
	return r
}

func waitFrame(t *testing.T, p *phone, id, typ string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m, ok := p.nextWithin(id, time.Until(deadline))
		if !ok {
			t.Fatalf("no %s frame", typ)
		}
		r := result(t, m)
		if r["type"] == typ {
			return r
		}
	}
}

func waitEnd(t *testing.T, p *phone, streamID, replyID string) {
	t.Helper()
	sawEnd, sawReply := false, false
	deadline := time.Now().Add(3 * time.Second)
	var aside []map[string]any
	defer func() { p.held = append(aside, p.held...) }()
	for !sawEnd || !sawReply {
		m, ok := p.nextAny(time.Until(deadline))
		if !ok {
			t.Fatalf("end=%v reply=%v", sawEnd, sawReply)
		}
		switch m["id"] {
		case streamID:
			if result(t, m)["type"] == "end" {
				sawEnd = true
			}
		case replyID:
			if result(t, m)["unsubscribed"] != true {
				t.Fatalf("unsubscribe reply %v", m)
			}
			sawReply = true
		default:
			aside = append(aside, m)
		}
	}
}

func takeType(t *testing.T, p *phone, id, typ string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	var aside []map[string]any
	defer func() { p.held = append(aside, p.held...) }()
	for {
		m, ok := p.nextAny(time.Until(deadline))
		if !ok {
			return false
		}
		r, _ := m["result"].(map[string]any)
		if m["id"] == id && r["type"] == typ {
			return true
		}
		aside = append(aside, m)
	}
}

func msgText(t *testing.T, frame map[string]any, i int) string {
	t.Helper()
	msgs, _ := frame["messages"].([]any)
	if i >= len(msgs) {
		t.Fatalf("message %d missing in %v", i, frame["type"])
	}
	m := msgs[i].(map[string]any)
	blocks := m["blocks"].([]any)
	return blocks[0].(map[string]any)["text"].(string)
}

func claudeLine(id, text string) string {
	return `{"type":"user","uuid":"` + id + `","message":{"content":"` + text + `"}}`
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}
