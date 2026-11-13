package term

import (
	"sync"
	"testing"
	"time"
)

func TestSessionTitleChangesSession(t *testing.T) {
	tm := &Terminal{agent: AgentState{
		SessionID: "old", Status: "working", Title: "old title",
		TranscriptPath: "/old/session", Message: "old message",
		ToolName: "old tool", ToolInput: "old input", Prompt: "old prompt", Subagents: []string{"old child"},
	}}
	tm.SetTitle("new title", "new", true)
	st := tm.Agent()
	if st.SessionID != "new" || st.Title != "new title" || !st.TitlePending {
		t.Fatalf("title belongs to the wrong session: %+v", st)
	}
	if st.TranscriptPath != "" || st.Message != "" || st.ToolName != "" || st.ToolInput != "" || st.Prompt != "" || len(st.Subagents) != 0 {
		t.Fatalf("kept old session metadata: %+v", st)
	}
	tm.SetAgent(AgentState{SessionID: "new", Status: "idle", TranscriptPath: "/new/session"})
	if st := tm.Agent(); st.Title != "new title" || !st.TitlePending || st.TranscriptPath != "/new/session" {
		t.Fatalf("new session hook lost the title: %+v", st)
	}
	tm.SetTitle("next title", "new", false)
	if st := tm.Agent(); st.TranscriptPath != "/new/session" || st.Title != "next title" || st.TitlePending {
		t.Fatalf("same-session rename: %+v", st)
	}
}

// 程序清空标题后，Title 和标题回调都要跟着变成空的。
func TestEmptyTitleClears(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	tm, err := Start(Options{ID: "t_t", Dir: t.TempDir(),
		OnTitle: func(_ *Terminal, s string) { mu.Lock(); seen = append(seen, s); mu.Unlock() },
		Command: []string{"/bin/sh", "-c", "printf '\\033]2;Action Required\\007'; sleep 0.3; printf '\\033]2;\\007'; sleep 0.3; echo done; sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	waitFor(t, "done", func() bool { return tm.ContainsText("done") })
	mu.Lock()
	defer mu.Unlock()
	if tm.Title() != "" || len(seen) == 0 || seen[len(seen)-1] != "" {
		t.Fatalf("title %q, callbacks %q", tm.Title(), seen)
	}
}

func TestTitleIsNotDeliveredToDifferentHookSession(t *testing.T) {
	tm := &Terminal{}
	tm.SetTitle("pending title", "environment-session", true)
	// hook 确认属于另一个会话时，不能把环境里那个会话的标题交给它。
	tm.SetAgent(AgentState{SessionID: "hook-session", Status: "working"})
	if st := tm.Agent(); st.SessionID != "hook-session" || st.Title != "" || st.TitlePending {
		t.Fatalf("title crossed sessions: %+v", st)
	}
	if title := tm.TakeTitle(); title != "" {
		t.Fatalf("delivered another session's title: %q", title)
	}
}
