package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/agent"
	"posse/internal/term"
)

// Claude：起名后先记成待交，下一次 UserPromptSubmit 的 hook 回应里带上标题，只交一次；换了会话标题就不再算它的
func TestSessionTitleClaude(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	d.setAgent(s, term.AgentState{Status: agent.StatusIdle, SessionID: "c1"})
	r := call(t, d.sessionTitle, map[string]any{"terminal": s.ID, "title": "fix｜登录按钮。", "sessionIds": map[string]string{"codex": "outer"}}).(sessionTitleResult)
	if !strings.HasSuffix(r.Title, "｜修复｜登录按钮") || r.Agent != "claude" {
		t.Fatalf("result %+v", r)
	}
	if st := d.terminalInfo(s).AgentState; st == nil || st.Title != r.Title || !st.TitlePending {
		t.Fatalf("state %+v", st)
	}
	submit := func(sid string) string {
		payload, _ := json.Marshal(map[string]string{"session_id": sid, "hook_event_name": "UserPromptSubmit"})
		res := call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "claude", "event": "UserPromptSubmit", "payload": json.RawMessage(payload)})
		b, _ := json.Marshal(res)
		var out struct{ SessionTitle string }
		json.Unmarshal(b, &out)
		return out.SessionTitle
	}
	if got := submit("c1"); got != r.Title {
		t.Fatalf("hook gave %q", got)
	}
	if got := submit("c1"); got != "" {
		t.Fatalf("title handed over twice: %q", got)
	}
	if st := d.terminalInfo(s).AgentState; st.Title != r.Title || st.TitlePending {
		t.Fatalf("after hand-over %+v", st)
	}
	// /clear 之类换了会话
	submit("c2")
	if st := d.terminalInfo(s).AgentState; st.Title != "" {
		t.Fatalf("title kept on a new session: %+v", st)
	}
	// 格式不对报错，不改状态
	raw, _ := json.Marshal(map[string]any{"terminal": s.ID, "title": "随便写写"})
	if _, err := d.sessionTitle(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "格式不对") {
		t.Fatalf("bad format: %v", err)
	}
}

// grok 写 summary.json；普通 shell 没有会话可起名
func TestSessionTitleGrokAndShell(t *testing.T) {
	d, w := newTestDaemon(t)
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	summary := filepath.Join(home, "sessions", "grp", "g1", "summary.json")
	os.MkdirAll(filepath.Dir(summary), 0o755)
	os.WriteFile(summary, []byte(`{"generated_title":"auto"}`), 0o644)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "grok", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	d.setAgent(s, term.AgentState{Status: agent.StatusIdle})
	r := call(t, d.sessionTitle, map[string]any{"terminal": s.ID, "title": "调研｜grok 标题", "sessionIds": map[string]string{"grok": "g1"}}).(sessionTitleResult)
	if b, _ := os.ReadFile(summary); !strings.Contains(string(b), r.Title) {
		t.Fatalf("summary %s", b)
	}
	if st := d.terminalInfo(s).AgentState; st == nil || st.Title != r.Title || st.TitlePending || st.SessionID != "g1" {
		t.Fatalf("state %+v", st)
	}
	sh, err := d.startTerminal(w, []string{"/bin/cat"}, "", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"terminal": sh.ID, "title": "调研｜x"})
	if _, err := d.sessionTitle(context.Background(), raw); err == nil {
		t.Fatal("named a plain shell")
	}
}
