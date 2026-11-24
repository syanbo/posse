package daemon

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/procmem"
	"posse/internal/usage"
)

// agy 不写终端标题：会话按它持有的 presence 锁认，标题读它自己起的，posse 起的标题不被覆盖。
func TestSyncAntigravity(t *testing.T) {
	d, w := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	base := history.AntigravityBase(home)
	const sid = "ce5e30fa-8b92-42ae-8769-e19e80bdc9da"
	if err := os.MkdirAll(filepath.Join(base, "presence"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(base, "presence", sid+".lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(base, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversation_summaries (conversation_id text PRIMARY KEY, title text NOT NULL DEFAULT "")`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// 经叫 agy 的符号链接运行的 sleep 冒充 agy（复制系统程序会被签名检查杀掉），把锁开在标准输入上
	bin := filepath.Join(t.TempDir(), "agy")
	if err := os.Symlink("/bin/sleep", bin); err != nil {
		t.Fatal(err)
	}
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "exec " + bin + " 30 < " + lock}, "antigravity", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if found, _ := agent.OfProcess(s.Foreground()); found == "antigravity" {
			break
		}
		if time.Now().After(deadline) {
			found, args := agent.OfProcess(s.Foreground())
			t.Fatalf("fake agy never reached the foreground: %q %q running=%v", found, args, s.Running())
		}
		time.Sleep(20 * time.Millisecond)
	}

	d.syncAntigravity(s)
	dirs, err := d.store.AntigravitySessionDirs(t.Context())
	if err != nil || dirs[sid] != resolvePath(w.Path) {
		t.Fatalf("bound conversation directory = %q, %v", dirs[sid], err)
	}
	if st := s.Agent(); st.SessionID != sid || st.Title != "" {
		t.Fatalf("before agy titles the session: %+v", st)
	}
	if got := (mobileHost{d: d}).TerminalSessionID(s.ID); got != sid {
		t.Fatalf("current session not eligible for mobile input: %q", got)
	}

	db, err = sql.Open("sqlite", filepath.Join(base, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO conversation_summaries VALUES (?, 'Review Flutter Commit')`, sid); err != nil {
		t.Fatal(err)
	}
	d.syncAntigravity(s)
	if st := s.Agent(); st.Title != "Review Flutter Commit" {
		t.Fatalf("native title not shown: %+v", st)
	}
	if info := d.terminalInfo(s); info.AgentState == nil || info.AgentState.Title != "Review Flutter Commit" {
		t.Fatalf("terminal info: %+v", info.AgentState)
	}

	s.SetTitle("1120｜docs｜renamed", sid, false)
	d.syncAntigravity(s)
	if st := s.Agent(); st.Title != "1120｜docs｜renamed" {
		t.Fatalf("unchanged native title overwrote Posse title: %+v", st)
	}
	if _, err := db.Exec(`UPDATE conversation_summaries SET title = 'Something Else'`); err != nil {
		t.Fatal(err)
	}
	d.syncAntigravity(s)
	if st := s.Agent(); st.Title != "Something Else" {
		t.Fatalf("native rename not shown: %+v", st)
	}

	d.usage.store = usage.Open(d.cfg.Home)
	now := time.Now()
	reset := now.Add(time.Hour).Truncate(time.Second)
	d.usage.store.Update(usage.Usage{Agent: "antigravity", UpdatedAt: now.Add(-time.Minute).UnixMilli(),
		Windows: []usage.Window{{QuotaID: "gemini-weekly", Name: "Gemini", Percent: 10, ResetsAt: reset.UnixMilli()}}})
	path := filepath.Join(base, "brain", sid, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"PLANNER_RESPONSE","input_tokens":1000}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := func(at time.Time, session, title string, current map[string]any) {
		t.Helper()
		call(t, d.antigravityStatusline, map[string]any{"terminal": s.ID, "at": at, "payload": map[string]any{
			"product": "antigravity", "conversation_id": session, "conversation_title": title,
			"transcript_path": filepath.Join(home, ".gemini", "antigravity", "brain", session, ".system_generated", "logs", "transcript.jsonl"),
			"context_window":  map[string]any{"context_window_size": 1_048_576, "used_percentage": 19.7, "current_usage": current},
			"quota":           map[string]any{"gemini-weekly": map[string]any{"remaining_fraction": 0.75, "reset_time": reset.Format(time.RFC3339)}},
		}})
	}
	report(now, sid, "From status line", map[string]any{"input_tokens": 1_000, "cache_read_input_tokens": 2_000})
	report(now.Add(-time.Second), sid, "Late status line", nil)
	report(now.Add(time.Millisecond), "66fedceb-d0b4-48ef-af10-c6f3adac9977", "Wrong session", nil)
	d.syncAntigravity(s)
	if st := s.Agent(); st.SessionID != sid || st.Title != "From status line" || st.TranscriptPath != path {
		t.Fatalf("status line binding: %+v", st)
	}
	if c, ok := history.ReadReportedContext(d.cfg.Home, "antigravity", sid, path); !ok || c.Used != 3000 || c.Percent() != 20 || c.Window != 1048576 {
		t.Fatalf("status line context: %+v, %v", c, ok)
	}
	if got := d.usage.store.List(now)[0].Windows[0]; got.Percent != 25 || got.Name != "Gemini" {
		t.Fatalf("status line quota: %+v", got)
	}
	d.usage.store.SetOff(true)
	report(now.Add(2*time.Millisecond), sid, "From status line", nil)
	if c, ok := history.ReadReportedContext(d.cfg.Home, "antigravity", sid, path); !ok || c.Used != 0 || c.Window != 0 {
		t.Fatalf("empty report or quota switch affected context: %+v, %v", c, ok)
	}

	// agy 空闲超过 5 秒后状态栏不再刷新，此时摘要库被其他进程回写旧标题也不应覆盖已上报标题。
	if _, err := db.Exec(`UPDATE conversation_summaries SET title = 'Stale Summary Title'`); err != nil {
		t.Fatal(err)
	}
	s.agyReportMu.Lock()
	s.agyReportAt = now.Add(-time.Minute)
	s.agyReportMu.Unlock()
	d.syncAntigravity(s)
	if st := s.Agent(); st.Title != "From status line" || st.NativeTitle != "From status line" {
		t.Fatalf("stale summary overwrote status line title after idle: %+v", st)
	}

	// 尚未收到状态栏上报时（如重启/交接后），优先读单会话独立的 annotations/<UUID>.pbtxt。
	if err := os.MkdirAll(filepath.Join(base, "annotations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "annotations", sid+".pbtxt"), []byte("title:\"1124｜修复｜原生标注\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.agyReportMu.Lock()
	s.agyReportPG, s.agyReportSID, s.agyReportTitle = 0, "", ""
	s.agyReportMu.Unlock()
	d.syncAntigravity(s)
	if st := s.Agent(); st.Title != "1124｜修复｜原生标注" || st.NativeTitle != "1124｜修复｜原生标注" {
		t.Fatalf("annotation title did not take precedence over stale summary: %+v", st)
	}
}

func TestAntigravityMobileInputRejectsAmbiguousLock(t *testing.T) {
	d, w := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	presence := filepath.Join(history.AntigravityBase(home), "presence")
	if err := os.MkdirAll(presence, 0o700); err != nil {
		t.Fatal(err)
	}
	presence, err := filepath.EvalSymlinks(presence)
	if err != nil {
		t.Fatal(err)
	}
	const old = "ce5e30fa-8b92-42ae-8769-e19e80bdc9da"
	const next = "66fedceb-d0b4-48ef-af10-c6f3adac9977"
	first, second := filepath.Join(presence, old+".lock"), filepath.Join(presence, next+".lock")
	for _, file := range []string{first, second} {
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "agy")
	if err := os.Symlink("/bin/sleep", bin); err != nil {
		t.Fatal(err)
	}
	command := "exec " + agent.Quote(bin) + " 30 3<" + agent.Quote(first) + " 4<" + agent.Quote(second)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", command}, "antigravity", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTitle("", old, false)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		pg := s.Foreground()
		name, _ := agent.OfProcess(pg)
		files := procmem.OpenFiles(pg)
		if name == "antigravity" && slices.Contains(files, first) && slices.Contains(files, second) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake agy did not open both presence locks")
		}
	}
	if got := (mobileHost{d: d}).TerminalSessionID(s.ID); got != "" {
		t.Fatalf("ambiguous session eligible for mobile input: %q", got)
	}
	if got := s.Agent().SessionID; got != old {
		t.Fatalf("checking input changed the displayed session: %q", got)
	}
}

func TestAntigravityOpenSession(t *testing.T) {
	home := "/Users/x"
	presence := filepath.Join(history.AntigravityBase(home), "presence")
	a, b := "ce5e30fa-8b92-42ae-8769-e19e80bdc9da", "66fedceb-d0b4-48ef-af10-c6f3adac9977"
	for _, c := range []struct {
		files []string
		want  string
	}{
		{nil, ""},
		{[]string{filepath.Join(presence, a+".lock"), "/dev/null"}, a},
		{[]string{filepath.Join(presence, a+".lock"), filepath.Join(presence, a+".lock")}, a},
		{[]string{filepath.Join(presence, a+".lock"), filepath.Join(presence, b+".lock")}, ""},
		{[]string{filepath.Join(presence, "not-a-uuid.lock")}, ""},
		{[]string{filepath.Join("/tmp/presence", a+".lock")}, ""},
	} {
		if got := history.AntigravityOpenSession(home, c.files); got != c.want {
			t.Errorf("%q: got %q, want %q", c.files, got, c.want)
		}
	}
}
