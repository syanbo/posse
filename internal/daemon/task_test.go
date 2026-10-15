package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hive/internal/agent"
	"hive/internal/store"
	"hive/internal/term"
)

func newTestDaemon(t *testing.T) (*Daemon, store.Workspace) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "hv")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "hive.db"))
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
	d.setAgent(coord.Terminal, term.AgentState{Status: agent.StatusWorking})
	info := call(t, d.taskStart, map[string]any{"caller": coord.ID, "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "x"}).(taskInfo)
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "all good"})
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(strings.Join(coord.Lines(), "\n"), "Hive:") {
		t.Fatal("nudged a working agent")
	}
	d.setAgent(coord.Terminal, term.AgentState{Status: agent.StatusIdle})
	waitScreen(t, coord, "Hive: "+info.ID+" finished. Run `hive task inbox` to read.")
	time.Sleep(300 * time.Millisecond)
	// PTY 回显一次、cat 再输出一次，所以一次提醒在屏幕上出现两遍
	once := strings.Count(strings.Join(coord.Lines(), "\n"), "Hive:")

	// 已提醒过的消息不再重复提醒
	d.setAgent(coord.Terminal, term.AgentState{Status: agent.StatusWorking})
	d.setAgent(coord.Terminal, term.AgentState{Status: agent.StatusIdle})
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(strings.Join(coord.Lines(), "\n"), "Hive:"); n != once {
		t.Fatalf("nudged again: %d occurrences, %d after the first nudge", n, once)
	}
}

func TestWorkerPrompt(t *testing.T) {
	task := store.Task{ID: "k_1", Spec: "do it"}
	p := workerPrompt(task, store.Workspace{Kind: "worktree", Branch: "feat", BaseRef: "main"})
	for _, want := range []string{"hive task ask k_1", "hive task done k_1 --summary", "the user", "branch feat, created from main", "=== TASK ===\ndo it"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}
