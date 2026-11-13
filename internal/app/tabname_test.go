package app

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"posse/locales"
)

func TestTabLabelCustomName(t *testing.T) {
	cases := []struct {
		term         terminalInfo
		name, detail string
	}{
		{terminalInfo{Agent: "claude", Title: "✳ Claude Code", Name: "修登录", Running: true}, "Claude", "修登录"},
		{terminalInfo{Agent: "claude", AgentState: &agentState{Title: "1020｜修复｜x"}, Name: "修登录", Running: true}, "Claude", "修登录"},
		{terminalInfo{Title: "zsh", Name: "构建", Running: true}, "构建", ""},
		{terminalInfo{Title: "zsh", Running: true}, "zsh", ""},
		// 还没恢复的照旧写常驻进程记下的名字，退出了的才标出来
		{terminalInfo{Agent: "claude", Title: "1020｜修复｜x", Lost: true}, "Claude", "1020｜修复｜x"},
		{terminalInfo{Title: "~/r", Lost: true}, "~/r", ""},
		{terminalInfo{Title: "zsh"}, "zsh", "已退出"},
	}
	for _, tc := range cases {
		if name, detail := tabLabel(locales.In("zh-Hans"), tc.term); name != tc.name || detail != tc.detail {
			t.Errorf("tabLabel(%+v) = %q, %q; want %q, %q", tc.term, name, detail, tc.name, tc.detail)
		}
	}
}

// 标签名交给常驻进程之前先显示 ui.json 里的；常驻进程报上来一样的名字后不再记着，
// 之后再改名照样先显示本地的。
func TestSyncTermName(t *testing.T) {
	a := &app{state: uiState{TermNames: map[string]string{"t1": "旧版存的名字"}}}
	term := terminalInfo{ID: "t1"}
	a.syncTermName(&term)
	if term.Name != "旧版存的名字" || a.nameSent["t1"] != "旧版存的名字" {
		t.Fatalf("pending name %q sent %v", term.Name, a.nameSent)
	}
	term = terminalInfo{ID: "t1", Name: "旧版存的名字"}
	a.syncTermName(&term)
	if _, ok := a.state.TermNames["t1"]; ok {
		t.Fatal("name the daemon already has should leave ui.json")
	}
	// 恢复默认：常驻进程还报着旧名字时显示默认
	a.renameTerminal("t1", "")
	term = terminalInfo{ID: "t1", Name: "旧版存的名字"}
	a.syncTermName(&term)
	if term.Name != "" {
		t.Fatalf("cleared name shows %q", term.Name)
	}
	term = terminalInfo{ID: "t1"}
	a.syncTermName(&term)
	if len(a.state.TermNames) != 0 {
		t.Fatalf("left %v", a.state.TermNames)
	}
}

func TestWorkerSessionTitle(t *testing.T) {
	a := &app{workerTitle: map[string]string{"t1": "old task title"}}
	term := terminalInfo{ID: "t1", Agent: "codex", Running: true}
	if got := a.defaultTaskTitle(term); got != "old task title" {
		t.Fatalf("unnamed worker: %q", got)
	}
	term.AgentState = &agentState{Title: "1112｜fix｜preserve the complete session title"}
	if got := a.defaultTaskTitle(term); got != "" {
		t.Fatalf("task masks the session title: %q", got)
	}
	a.startRename(locales.In("zh-Hans"), term)
	if a.renameText != term.AgentState.Title {
		t.Fatalf("rename input = %q", a.renameText)
	}
	// 保留原样离开输入框，不把会话标题变成固定的自定义名字。
	a.finishRename(locales.In("zh-Hans"), term)
	if len(a.state.TermNames) != 0 {
		t.Fatalf("unchanged title became a custom name: %v", a.state.TermNames)
	}
	term.Name = "later custom name"
	if got := a.defaultTaskTitle(term); got != "" {
		t.Fatalf("task masks the custom name: %q", got)
	}
	_, detail := fullLabel(locales.In("zh-Hans"), term)
	if detail != term.Name {
		t.Fatalf("custom name = %q", detail)
	}
}

func TestPreviewWorkerSessionTitle(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		a.tasks[0].Title = "Mac复现个人厅主播资格异常"
		a.workspaces[0].Terminals[3].AgentState = &agentState{Status: "idle", Title: "1112｜修复｜视频模式主播准备状态漏同步"}
		a.indexTasks()
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		savePNG(t, newPreviewTester(a, dark), "worker-session-title-"+suffix+".png")
	}
}

func TestRenameAckBeforeSessionTitlePoll(t *testing.T) {
	a := &app{}
	a.renameTerminal("t1", "old custom name")
	a.ackTermName("t1", "old custom name")
	// 自定义名的 RPC 已成功，但下一次轮询前 session title 又改了名字。
	term := terminalInfo{ID: "t1", AgentState: &agentState{Title: "new session title"}}
	a.syncTermName(&term)
	if got := termTitle(term); got != "new session title" || len(a.state.TermNames) != 0 {
		t.Fatalf("stale override = %q, pending %v", got, a.state.TermNames)
	}
	a.renameTerminal("t1", "first")
	a.renameTerminal("t1", "second")
	a.ackTermName("t1", "first")
	if a.state.TermNames["t1"] != "second" {
		t.Fatal("old acknowledgement discarded the newer name")
	}
}

func TestRenameAckWaitsForOldPoll(t *testing.T) {
	a := &app{}
	a.renameTerminal("t1", "new name")
	// 轮询已经取到旧名字，但尚未向界面提交结果。
	a.refreshMu.Lock()
	done := make(chan struct{})
	go func() {
		a.confirmTermName("t1", "new name")
		close(done)
	}()
	select {
	case <-done:
		a.refreshMu.Unlock()
		t.Fatal("acknowledgement passed the in-flight poll")
	case <-time.After(20 * time.Millisecond):
	}
	var shown string
	a.update(func() {
		term := terminalInfo{ID: "t1", Name: "old name"}
		a.syncTermName(&term)
		shown = term.Name
	})
	a.refreshMu.Unlock()
	<-done
	a.runPending()
	if shown != "new name" || len(a.state.TermNames) != 0 {
		t.Fatalf("old poll showed %q, pending %v", shown, a.state.TermNames)
	}
	// 确认之后的新轮询可以接受命令设置的会话标题。
	term := terminalInfo{ID: "t1", AgentState: &agentState{Title: "new session title"}}
	a.syncTermName(&term)
	if got := termTitle(term); got != "new session title" {
		t.Fatalf("new poll showed %q", got)
	}
}

func TestTabTooltipSessionTitle(t *testing.T) {
	term := terminalInfo{Agent: "codex", Title: "old terminal title", AgentState: &agentState{Title: "1112｜fix｜preserve the complete session title"}}
	if got := tabTooltip(locales.In("en"), term); got != "Codex\n"+term.AgentState.Title {
		t.Fatalf("session tooltip = %q", got)
	}
	term.Name = "custom name"
	if got := tabTooltip(locales.In("en"), term); got != "Codex\ncustom name" {
		t.Fatalf("custom tooltip = %q", got)
	}
}

func TestTabRef(t *testing.T) {
	cases := []struct {
		term terminalInfo
		task *taskInfo
		want string
	}{
		{terminalInfo{ID: "t_1", Title: "zsh", Running: true}, nil, "zsh：终端 t_1（posse terminal read t_1）"},
		{
			terminalInfo{ID: "t_2", Agent: "cursor", Name: "修登录", AgentState: &agentState{SessionID: "s1"}, Running: true},
			&taskInfo{ID: "k_3"},
			"Cursor「修登录」：终端 t_2（posse terminal read t_2），任务 k_3（posse task show k_3），会话 s1（posse history show s1）",
		},
		// 退出了的不把「已退出」写进名字
		{terminalInfo{ID: "t_4", Agent: "codex"}, nil, "Codex：终端 t_4（posse terminal read t_4）"},
		{terminalInfo{ID: "t_5", Agent: "codex", Name: "修登录"}, nil, "Codex「修登录」：终端 t_5（posse terminal read t_5）"},
		{terminalInfo{ID: "t_6", Name: "已退出", Running: true}, nil, "已退出：终端 t_6（posse terminal read t_6）"},
	}
	for _, tc := range cases {
		if got := tabRef(locales.In("zh-Hans"), tc.term, tc.task); got != tc.want {
			t.Errorf("tabRef(%+v) = %q, want %q", tc.term, got, tc.want)
		}
	}
}

func TestFailedRenameYieldsToNewSessionTitle(t *testing.T) {
	old := terminalInfo{ID: "t1", Agent: "claude", AgentState: &agentState{SessionID: "s1", Title: "old session title"}}
	a := &app{workspaces: []workspace{{ID: "w1", Terminals: []terminalInfo{old}}}}
	a.indexWorkspaces()
	a.renameTerminal("t1", "pending custom name")
	// RPC 失败后，下一次轮询仍可重试；会话标题没变时保留用户的输入。
	delete(a.nameSent, "t1")
	fresh := old
	a.syncTermName(&fresh)
	if fresh.Name != "pending custom name" || a.nameSent["t1"] != "pending custom name" {
		t.Fatalf("retry lost the name: %+v", fresh)
	}
	delete(a.nameSent, "t1")
	// 待交名字和它对应的会话信息都能随窗口重开恢复。
	home := t.TempDir()
	b, err := json.Marshal(a.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uiStatePath(home), b, 0o600); err != nil {
		t.Fatal(err)
	}
	a = &app{state: loadUIState(home)}
	fresh = terminalInfo{ID: "t1", Agent: "claude", AgentState: &agentState{SessionID: "s1", Title: "later session title"}}
	a.syncTermName(&fresh)
	if fresh.Name != "" || termTitle(fresh) != "later session title" || len(a.state.TermNames) != 0 || len(a.state.TermNameBases) != 0 || len(a.nameSent) != 0 {
		t.Fatalf("old failed rename masked the new title: %+v, pending %+v", fresh, a.state)
	}
	// 会话没变时，后来一次手动改名仍然优先。
	a.workspaces = []workspace{{ID: "w1", Terminals: []terminalInfo{fresh}}}
	a.indexWorkspaces()
	a.renameTerminal("t1", "latest manual name")
	a.syncTermName(&fresh)
	if fresh.Name != "latest manual name" {
		t.Fatalf("new manual name ignored: %+v", fresh)
	}
}
