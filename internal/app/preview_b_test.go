package app

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/locales"
)

// TestPreviewSidebarTabs 给侧栏、标签页、任务、终端几块界面各出中文、英文和伪语言（en-XA 把英文加长、加重音）
// 深浅各一套：看英文有没有截断、重叠，中文和原来一样。设置 POSSE_PREVIEW_DIR 才运行。
func TestPreviewSidebarTabs(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	type scene struct {
		name  string
		setup func(t *testing.T) *app
		// act 在换好语言后操作界面（右键菜单、确认对话框），按和语言无关的文字找元素
		act func(a *app, tt *ui.Tester, lang string)
	}
	scenes := []scene{
		{name: "main", setup: func(t *testing.T) *app {
			a := previewApp(t)
			setUnread(a, "t4")
			return a
		}},
		{name: "sidebar-running", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.state.SidebarGroup = sidebarByStatus
			a.workspaces[0].Terminals[1].AgentState = &agentState{Status: "idle"}
			setUnread(a, "t2")
			return a
		}},
		{name: "sidebar-collapsed", setup: func(t *testing.T) *app {
			a := previewApp(t)
			*a.sectionsOpen["p5"] = false
			*a.sectionsOpen["p1"] = false
			return a
		}},
		{name: "tab-menu", setup: previewApp, act: func(_ *app, tt *ui.Tester, _ string) { tt.RightClick("zsh") }},
		{name: "workspace-menu", setup: previewApp, act: func(_ *app, tt *ui.Tester, _ string) { tt.RightClick("Server") }},
		{name: "session-menu", setup: previewApp, act: func(_ *app, tt *ui.Tester, _ string) { tt.RightClick("重写 posse 界面") }},
		{name: "terminal-menu", setup: previewApp, act: func(a *app, _ *ui.Tester, _ string) {
			// 终端插件交来位置时就是这样记下的
			a.termCtx, a.termCtxID = menuState{open: true, x: 700, y: 300}, "t1"
		}},
		{name: "terminal-find-none", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.panes[0].find = termFind{open: true, query: "release"}
			return a
		}},
		{name: "terminal-starting", setup: func(t *testing.T) *app {
			a := previewApp(t)
			blank, err := terminal.New(terminal.Options{Conn: nopConn{make(chan struct{})}, Font: terminal.Font{Family: termFontFamily, Size: defaultFontSize}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { blank.Close() })
			a.panes[0].term.Close()
			a.panes[0] = &termView{id: "t1", term: blank, attached: time.Now().Add(-time.Second)}
			return a
		}},
		{name: "phone-sized", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.workspaces[0].Terminals[0].PhoneSized = true
			a.indexWorkspaces()
			return a
		}},
		{name: "missing", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.workspaces[0].Missing = true
			a.indexWorkspaces()
			return a
		}},
		{name: "missing-empty", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.workspaces[5].Missing = true
			a.indexWorkspaces()
			a.selected = "w6"
			a.workspaces[5].Terminals = nil
			a.indexWorkspaces()
			return a
		}},
		{name: "stale-app", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.staleApp = true
			return a
		}},
		{name: "session-buttons-folded", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.state.SessionButtonsHidden = true
			return a
		}},
		{name: "empty", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.selected = "w2"
			return a
		}},
		{name: "first-run", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.projects, a.workspaces, a.selected = nil, nil, ""
			a.indexWorkspaces()
			return a
		}},
		{name: "new-workspace", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.openNewWorkspace()
			a.newProject, a.newAgent = "Skill", 1
			return a
		}},
		{name: "confirm-close", setup: previewApp, act: func(a *app, _ *ui.Tester, lang string) {
			a.askCloseAll(locales.In(lang), a.workspaces[0].Terminals)
		}},
		{name: "confirm-remove", setup: previewApp, act: func(a *app, _ *ui.Tester, lang string) {
			a.askRemoveProject(locales.In(lang), a.workspaces[3])
		}},
		{name: "tasks", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.selected, a.taskSel = taskBoardID, "k2"
			a.detail = &taskDetail{taskInfo: *a.task("k2"), Messages: []taskMessage{
				{ID: "m1", Kind: "note", Body: "钥匙串的 service 名用 com.example.skill。", CreatedAt: time.Now().Add(-20 * time.Minute)},
				{ID: "m2", Kind: "question", Body: "要不要补一个“记住密码”的用例？", CreatedAt: time.Now().Add(-2 * time.Minute)},
			}}
			return a
		}},
		{name: "task-done", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.selected, a.taskSel = taskBoardID, "k3"
			a.detail = &taskDetail{taskInfo: *a.task("k3")}
			return a
		}},
		{name: "tasks-home", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.selected, a.taskHome = taskBoardID, true
			return a
		}},
		{name: "tasks-empty", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.selected, a.tasks = taskBoardID, nil
			a.indexTasks()
			return a
		}},
		{name: "dispatch", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.openDispatch()
			a.dWorkspace, a.dMode = "Skill/main", 1
			a.dAgents = map[string]bool{"claude": true, "codex": true}
			a.dTitle = "Remember password"
			return a
		}},
		{name: "project-icon-dialog", setup: func(t *testing.T) *app {
			a := previewApp(t)
			a.home = t.TempDir()
			a.editProjectIcon(a.projects[1])
			return a
		}},
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		for _, s := range scenes {
			for _, lang := range []string{"zh-Hans", "en", "en-XA"} {
				a := s.setup(t)
				tt := newPreviewTester(a, dark)
				tt.SetLocale(lang)
				tt.Frame()
				if s.act != nil {
					s.act(a, tt, lang)
				}
				for range 4 {
					tt.Frame()
					pass(tt, 80*time.Millisecond)
				}
				tt.Frame()
				savePNG(t, tt, "b-"+s.name+"-"+lang+"-"+suffix+".png")
			}
		}
	}
}
