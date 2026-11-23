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
)

// agy 不写终端标题、没有 hooks：会话按它持有的 presence 锁认，标题读它自己起的，posse 起的标题不被覆盖。
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
