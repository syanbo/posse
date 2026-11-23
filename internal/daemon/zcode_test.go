package daemon

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/agent"
)

func TestZCodeStatusBindsForegroundTurn(t *testing.T) {
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/zsh", "-f", "-c", "exec -a zcode-cli /bin/cat"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if pg := s.Foreground(); pg > 0 {
			if name, _ := agent.OfProcess(pg); name == "zcode" {
				pid = pg
				break
			}
		}
	}
	if pid == 0 {
		t.Fatal("ZCode never became foreground")
	}
	const sid = "sess_01234567-89ab-4cde-8f01-23456789abcd"
	const other = "sess_01234567-89ab-4cde-8f01-23456789abce"
	stamp := time.Now()
	post := func(event, sessionID, turn string, parent int, offset int) {
		t.Helper()
		payload, err := json.Marshal(agent.ZCodeHook{Name: event, SessionID: sessionID, TurnID: turn,
			Timestamp: stamp.Add(time.Duration(offset) * time.Second), Response: "reply"})
		if err != nil {
			t.Fatal(err)
		}
		d.zcodeHook(s, hookEventParams{Agent: "zcode", Event: event, ParentPID: parent, Payload: payload})
	}
	post("SessionStart", sid, "", pid, 0)
	post("UserPromptSubmit", sid, "first", pid, 1)
	post("SessionStart", other, "", pid, 2)
	post("UserPromptSubmit", other, "second", pid, 3)
	post("Stop", sid, "first", pid, 4)
	post("UserPromptSubmit", sid, "first", pid, 4)
	post("Stop", other, "second", pid+1, 4)
	post("SessionStart", sid, "", pid, 0)
	if a := s.Agent(); a.SessionID != other || a.Status != agent.StatusWorking {
		t.Fatalf("old or non-foreground hook replaced active state: %+v", a)
	}
	post("Stop", other, "second", pid, 4)
	if a := s.Agent(); a.SessionID != other || a.Status != agent.StatusIdle || a.Message != "reply" {
		t.Fatalf("unexpected completed turn: %+v", a)
	}
}

func TestZCodeSyncsNewSessionWithoutHooks(t *testing.T) {
	d, ws := newTestDaemon(t)
	path := filepath.Join(t.TempDir(), "zcode.sqlite")
	t.Setenv("ZCODE_SESSION_DB_PATH", path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := d.startTerminal(ws, []string{"/bin/zsh", "-f", "-c", "exec -a zcode-cli /bin/cat"}, "zcode", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	waitFor(t, "ZCode foreground process", func() bool {
		pid = s.Foreground()
		name, _ := agent.OfProcess(pid)
		return name == "zcode"
	})
	const first = "sess_01234567-89ab-4cde-8f01-23456789abcd"
	const second = "sess_01234567-89ab-4cde-8f01-23456789abce"
	if _, err := db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY, title TEXT, task_type TEXT, time_deleted INTEGER);
		CREATE TABLE session_runtime_presence(session_id TEXT, process_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session VALUES (?, 'First', 'interactive', NULL), (?, 'Second', 'interactive', NULL)`, first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_runtime_presence VALUES (?, ?)`, first, pid); err != nil {
		t.Fatal(err)
	}
	d.syncZCode(t.Context(), s)
	if state := s.Agent(); state.SessionID != first || state.Title != "First" || s.isHooked() {
		t.Fatalf("first session: %+v, hooks=%v", state, s.isHooked())
	}
	if _, err := db.Exec(`UPDATE session_runtime_presence SET session_id = ? WHERE process_id = ?`, second, pid); err != nil {
		t.Fatal(err)
	}
	d.syncZCode(t.Context(), s)
	if state := s.Agent(); state.SessionID != second || state.Title != "Second" {
		t.Fatalf("new session: %+v", state)
	}
}
