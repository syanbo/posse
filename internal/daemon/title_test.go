package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/errtext"
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
	// 类型词跟着系统语言（CI 的 runner 是英文）
	want := "｜修复｜登录按钮"
	if errtext.SystemLang() == errtext.EN {
		want = "｜fix｜登录按钮"
	}
	if !strings.HasSuffix(r.Title, want) || r.Agent != "claude" {
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
	if _, err := d.sessionTitle(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "bad title format") {
		t.Fatalf("bad format: %v", err)
	}
}

func TestSessionTitleCodexReplacesCustomName(t *testing.T) {
	d, w := newTestDaemon(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT NOT NULL, history_mode TEXT NOT NULL, name TEXT)`,
		`INSERT INTO threads VALUES ('c1', 'first message', 'paginated', 'old title')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	d.setAgent(s, term.AgentState{Status: agent.StatusIdle, SessionID: "previous"})
	call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": "old custom name"})
	// 写入会话失败时，保留原来的标签名。
	raw, _ := json.Marshal(map[string]any{"terminal": s.ID, "title": "fix｜rename", "sessionIds": map[string]string{"codex": "missing"}})
	if _, err := d.sessionTitle(context.Background(), raw); err == nil {
		t.Fatal("missing Codex session should fail")
	}
	if s.userName() != "old custom name" {
		t.Fatal("failed rename cleared the custom name")
	}
	for range 2 {
		r := call(t, d.sessionTitle, map[string]any{"terminal": s.ID, "title": "fix｜rename", "sessionIds": map[string]string{"codex": "c1"}}).(sessionTitleResult)
		d.setAgent(s, term.AgentState{Status: agent.StatusIdle, SessionID: "c1", Event: "Stop"})
		info := d.terminalInfo(s)
		if info.Name != "" || info.AgentState.Title != r.Title {
			t.Fatalf("after rename and Stop: %+v, state %+v", info, info.AgentState)
		}
		rec, err := d.store.TerminalResume(s.ID)
		if err != nil || rec.Title != "" {
			t.Fatalf("persisted name = %q, err %v", rec.Title, err)
		}
		var name string
		if err := db.QueryRow(`SELECT name FROM threads WHERE id = 'c1'`).Scan(&name); err != nil || name != r.Title {
			t.Fatalf("Codex title = %q, err %v", name, err)
		}
	}
	// 用户之后仍可单独设置标签名。
	call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": "later custom name"})
	if s.userName() != "later custom name" {
		t.Fatal("later custom name was ignored")
	}
}

func TestConcurrentSessionAndTerminalRename(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	rename, _ := json.Marshal(map[string]any{"terminal": s.ID, "name": "custom name"})
	title, _ := json.Marshal(map[string]any{"terminal": s.ID, "title": "fix｜rename"})
	for range 30 {
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-start
			_, err := d.terminalRename(context.Background(), rename)
			done <- err
		}()
		go func() {
			<-start
			_, err := d.sessionTitle(context.Background(), title)
			done <- err
		}()
		close(start)
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		rec, err := d.store.TerminalResume(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.userName(); got != rec.Title {
			t.Fatalf("memory name %q != persisted name %q", got, rec.Title)
		}
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

func TestRenameDatabaseWaitKeepsSessionResponsive(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(d.cfg.Home, "posse.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE terminals SET title = title WHERE id = ?", s.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.setTerminalName(s, "new custom name") }()
	// 外部写事务占住 SQLite，确认改名已经进入串行区，不能靠调度碰巧绕过阻塞。
	deadline := time.Now().Add(time.Second)
	for {
		if !s.nameMu.TryLock() {
			break
		}
		s.nameMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("rename did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if !s.mu.TryLock() {
		t.Fatal("database wait blocked session state and PTY callbacks")
	}
	s.mu.Unlock()
	select {
	case err := <-done:
		t.Fatalf("rename passed the database writer: %v", err)
	default:
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rename did not finish after database unlock")
	}
	rec, err := d.store.TerminalResume(s.ID)
	if err != nil || rec.Title != "new custom name" || s.userName() != rec.Title {
		t.Fatalf("name after unlock: %+v, %v", rec, err)
	}
}
