package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"hive/internal/history"
)

// nopConn 让界面里的终端只显示喂进去的内容。
type nopConn struct{ done chan struct{} }

func (c nopConn) Read(p []byte) (int, error)  { <-c.done; return 0, io.EOF }
func (c nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (c nopConn) Close() error                { return nil }

func state(s string) *agentState { return &agentState{Status: s} }

// previewApp 构造一份有代表性的界面数据：等你处理、工作中、已完成、普通终端都有。
func previewApp(t *testing.T) *app {
	a := &app{
		activeTerm:   map[string]string{},
		sectionsOpen: map[string]*bool{},
		lastStatus:   map[string]string{},
		lastTask:     map[string]string{},
		diffs:        map[string]*worktreeDiff{},
		fontSize:     defaultFontSize,
		loaded:       true,
		daemon:       daemonStatus{FootprintKB: 17 << 10, Running: 4},
		selfKB:       150 << 10,
	}
	a.projects = []project{
		{ID: "p1", Name: "Apps"}, {ID: "p2", Name: "Chat"}, {ID: "p3", Name: "react-native-syan-image-picker"},
		{ID: "p4", Name: "Server"}, {ID: "p5", Name: "Skill", Kind: "git"}, {ID: "p6", Name: "Works"},
	}
	a.workspaces = []workspace{
		{ID: "w1", ProjectID: "p1", Name: "main", Kind: "main", Project: "Apps", Label: "Apps/main", Path: os.Getenv("HOME") + "/Desktop/AI/Apps",
			Terminals: []terminalInfo{
				{ID: "t1", Agent: "claude", Title: "✳ 重写 hive 界面", Running: true, AgentState: state("waiting")},
				{ID: "t2", Agent: "codex", Title: "codex", Running: true, AgentState: state("working")},
				{ID: "t3", Title: "zsh", Running: true},
			}},
		{ID: "w2", ProjectID: "p2", Name: "main", Kind: "main", Project: "Chat", Label: "Chat/main", Path: "/Users/x/Chat"},
		{ID: "w3", ProjectID: "p3", Name: "main", Kind: "main", Project: "react-native-syan-image-picker", Label: "r/main", Path: "/Users/x/rn",
			Terminals: []terminalInfo{{ID: "t4", Agent: "claude", Running: true, AgentState: state("idle")}}},
		{ID: "w4", ProjectID: "p4", Name: "main", Kind: "main", Project: "Server", Label: "Server/main", Path: "/Users/x/Server"},
		{ID: "w5", ProjectID: "p5", Name: "main", Kind: "main", Project: "Skill", Label: "Skill/main", Branch: "main", Path: "/Users/x/Skill"},
		{ID: "w6", ProjectID: "p5", Name: "feat-login", Kind: "worktree", Project: "Skill", Label: "Skill/feat-login", Branch: "feat-login", Path: "/Users/x/.hive/worktrees/Skill/feat-login",
			Terminals: []terminalInfo{{ID: "t5", Agent: "codex", Running: true, AgentState: state("working")}}},
		{ID: "w9", ProjectID: "p5", Name: "ios-crash", Kind: "worktree", Project: "Skill", Label: "Skill/ios-crash", Branch: "hotfix/ios-crash-212", Path: "/Users/x/.hive/worktrees/Skill/ios-crash"},
		{ID: "w7", ProjectID: "p6", Name: "app", Kind: "main", Project: "Works", Label: "Works/app", Path: "/Users/x/Works"},
		{ID: "w8", ProjectID: "p6", Name: "web", Kind: "folder", Project: "Works", Label: "Works/web", Path: "/Users/x/Works"},
	}
	for _, p := range a.projects {
		open := true
		a.sectionsOpen[p.ID] = &open
	}
	now := time.Now()
	a.workspaces[0].Terminals = append(a.workspaces[0].Terminals, terminalInfo{ID: "t6", Agent: "grok", Title: "⠹ - Running tool - grok", Running: true, AgentState: state("working")})
	ended := now.Add(-3 * time.Minute)
	a.tasks = []taskInfo{
		{ID: "k1", Title: "真机验证动图帧回调空指针修复", Agent: "grok", Workspace: "Apps/main", WorkspaceID: "w1", Worker: "t6", Status: "running",
			Coordinator: "t1", CreatedAt: now.Add(-12 * time.Minute), Terminal: &terminalInfo{ID: "t6", Agent: "grok", Running: true, AgentState: state("working")}},
		{ID: "k2", Title: "给登录页加上记住密码", Agent: "codex", Workspace: "Skill/feat-login", WorkspaceID: "w6", WorkspaceKind: "worktree", Branch: "feat-login", BaseRef: "main",
			Worker: "t5", Status: "running", Questions: 1, CreatedAt: now.Add(-25 * time.Minute), Terminal: &terminalInfo{ID: "t5", Agent: "codex", Running: true, AgentState: state("working")},
			Spec: "在登录页加一个“记住密码”开关，密码存进系统钥匙串。不要改注册流程。完成标准：单测通过，手动登录两次第二次免输密码。"},
		{ID: "k3", Title: "统计 IM 模块的重连路径", Agent: "claude", Workspace: "Apps/main", WorkspaceID: "w1", Worker: "t9", Status: "done", Coordinator: "t1",
			Summary: "梳理了 4 条重连路径，其中飞行模式恢复后会重复建连。已在报告里附上日志位置和复现步骤，剩下服务端超时参数待确认。",
			Report:  "/tmp/im-reconnect/report.md", CreatedAt: now.Add(-2 * time.Hour), EndedAt: &ended},
		{ID: "k4", Title: "打 qa 包并安装到两台安卓机", Agent: "grok", Workspace: "Works/app", WorkspaceID: "w7", Status: "failed", Coordinator: "t1",
			Summary: "打包在 resguardArmQa 失败，原因是 JDK 版本不对。没有改构建配置。", CreatedAt: now.Add(-5 * time.Hour), EndedAt: &ended},
	}
	a.indexWorkspaces()
	a.indexTasks()
	a.diffs["w6"] = &worktreeDiff{Commits: 2, Files: 3, Insertions: 86, Deletions: 7, Dirty: true}
	a.selected = "w1"
	a.activeTerm["w1"] = "t1"
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	light, dark := terminalThemes()
	term, err := terminal.New(terminal.Options{Conn: nopConn{done}, Font: terminal.Font{Family: termFontFamily, Size: defaultFontSize}, Theme: light, DarkTheme: dark})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	term.Feed([]byte("\x1b[1m\x1b[38;2;215;119;87m ▐▛███▜▌\x1b[0m   \x1b[1mClaude Code\x1b[0m v2.1.299\r\n" +
		"\x1b[38;2;215;119;87m▝▜█████▛▘\x1b[0m  Opus 5.5 · ~/Desktop/AI/Apps\r\n\r\n" +
		"\x1b[2m> \x1b[0m把侧栏的状态改成蜂巢格\r\n\r\n" +
		"\x1b[38;2;120;180;120m⏺\x1b[0m 最近6次构建都用的是 develop_700 分支。#211之后只有我这次推送的提交，Podfile没变化。\r\n\r\n" +
		"\x1b[38;2;120;180;120m⏺\x1b[0m Update(internal/app/view.go)\r\n" +
		"  ⎿  Updated internal/app/view.go with 42 additions and 7 removals\r\n\r\n" +
		"\x1b[38;2;242;170;46m╭──────────────────────────────────────────────╮\r\n" +
		"│ Bash command                                 │\r\n" +
		"│   go test ./...                              │\r\n" +
		"│ Do you want to proceed?                      │\r\n" +
		"│ ❯ 1. Yes                                     │\r\n" +
		"│   2. No, and tell Claude what to do          │\r\n" +
		"╰──────────────────────────────────────────────╯\x1b[0m\r\n"))
	a.view = &termView{id: "t1", term: term}
	return a
}

// previewHistory 填一份历史页的数据：四家 agent 的会话、一次搜索的命中和一个打开的会话。
func previewHistory(a *app, query string) {
	now := time.Now()
	home := os.Getenv("HOME")
	sessions := []history.Session{
		{Agent: "claude", ID: "s1", Title: "Orca 到 mygo 迁移方案", Cwd: home + "/Desktop/AI/Apps", UpdatedAt: now.Add(-20 * time.Minute), Messages: 412, Model: "claude-opus-5-5"},
		{Agent: "grok", ID: "s2", Title: "清理磁盘", Cwd: home + "/Desktop/AI/Grok", Branch: "main", UpdatedAt: now.Add(-3 * time.Hour), Messages: 630, Model: "grok-4.7-build"},
		{Agent: "codex", ID: "s3", Title: "0927｜文档｜统一去除章节标题序号", Cwd: home + "/Desktop/AI/Claude", UpdatedAt: now.Add(-26 * time.Hour), Messages: 15, Model: "gpt-6-astra"},
		{Agent: "cursor", ID: "s4", Title: "TikTok Ark Video", Cwd: home + "/Desktop/AI/Video/Halloween-3", UpdatedAt: now.Add(-50 * time.Hour), Messages: 58},
		{Agent: "claude", ID: "s5", Title: "0918｜修复｜im_sdk 拉黑后消息页空转鉴权问题", Cwd: home + "/Desktop/AI/Works", UpdatedAt: now.Add(-12 * 24 * time.Hour), Messages: 96},
		{Agent: "grok", ID: "s6", Title: "Ghostty 快捷键与浅色主题轮播", Cwd: home + "/Desktop/AI/Grok", UpdatedAt: now.Add(-46 * 24 * time.Hour), Messages: 72},
	}
	h := &a.history
	h.loaded, h.status = true, historyStatus{Sessions: 424}
	a.selected = historyBoardID
	h.query = query
	if query == "" {
		h.setSessions(sessions)
		return
	}
	snip := "…先看看 Library/Caches 有多大，再决定清理磁盘上的哪些缓存。"
	i := strings.Index(snip, "磁盘")
	h.hits = []history.Hit{
		{Session: sessions[1], Matches: 0, Role: "title", Seq: -1, Snippet: "清理磁盘", Spans: []history.Span{{Start: 6, End: 12}}},
		{Session: sessions[0], Matches: 4, Role: "assistant", Seq: 3, Snippet: snip, Spans: []history.Span{{Start: i, End: i + len("磁盘")}}},
		{Session: sessions[4], Matches: 1, Role: "tool", Seq: 9, Snippet: "df -h / 磁盘只剩约 2.5Gi", Spans: []history.Span{{Start: 8, End: 14}}},
	}
	h.sel, h.hitSeq, h.placed = "s1", 3, false
	h.detail = &historyDetail{Session: sessions[0], Messages: []history.Message{
		{Seq: 0, Role: "user", Text: "我的 Mac 磁盘快满了，帮我看看能清理什么", At: now.Add(-40 * time.Minute)},
		{Seq: 1, Role: "assistant", Text: "先看各目录占用，再按能否安全删除分类。", At: now.Add(-39 * time.Minute)},
		{Seq: 2, Role: "tool", Text: "Bash du -sh ~/Library/Caches ~/Library/Developer", At: now.Add(-39 * time.Minute)},
		{Seq: 3, Role: "assistant", Text: "Caches 有 12G，Xcode 的 DerivedData 有 31G。先看看 Library/Caches 有多大，再决定清理磁盘上的哪些缓存：\n\n- DerivedData 可以整个删，下次编译会重建\n- 模拟器运行时用 xcrun simctl delete unavailable 清理", At: now.Add(-38 * time.Minute)},
		{Seq: 4, Role: "user", Text: "DerivedData 删掉吧，模拟器先别动", At: now.Add(-30 * time.Minute)},
	}}
}

// previewMarkdown 打开一个回复里有标题、表格、列表和代码的会话。
func previewMarkdown(a *app) {
	h := &a.history
	now := time.Now()
	h.sel, h.hitSeq, h.placed = "s1", -1, false
	h.detail = &historyDetail{Session: h.sessions[0], Messages: []history.Message{
		{Seq: 0, Role: "user", Text: "评估一下用 mygo 重写 Orca", At: now.Add(-40 * time.Minute)},
		{Seq: 1, Role: "assistant", At: now.Add(-39 * time.Minute), Text: "我按 [egoist/mygo](https://pkg.go.dev/github.com/egoist/mygo) 来理解\"mygo\"，先实测了 Orca。\n\n" +
			"## 结论\n\n可以重写，但性能提升主要来自两点：**只做这三个功能**，后台换成 Go 常驻进程。\n\n" +
			"## 实测现状（已核实）\n\n| 项目 | 数据 |\n|---|---|\n| Orca 版本 | 1.4.224，Electron 加一个 Node 后台进程 |\n| 自身内存 | 约 1.1 GB：主进程 235 MB，界面进程 442 MB |\n| 每个 agent 终端 | 540 到 700 MB，**重写后不会变** |\n\n" +
			"下一步：\n\n1. 先做终端和 `hive daemon`\n2. 再做编排\n   - worktree 隔离\n   - 任务汇报\n\n- [x] 调研 Orca\n- [ ] 手机遥控\n\n" +
			"> 换框架本身带来的收益有限。\n\n```go\nfunc main() {\n\tmygo.Run(app)\n}\n```"},
	}}
}

// previewFiles 在 w1 的文件标签里依次打开 rels（最后一个是当前文件），等它们读进来；之后不再轮询磁盘。
func previewFiles(t *testing.T, a *app, rels ...string) *filesView {
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.state.FilesPanel = true
	tt := ui.NewTester(a.build, 1200, 760)
	for _, rel := range rels {
		a.openFile(w, filepath.Join(root, rel), 0, [2]int{})
		v := a.files[w.ID]
		until(t, tt, rel, func() bool { return v.cur.kind != docLoading && v.list != nil && len(v.tree.Rows()) > 0 })
	}
	v := a.files[w.ID]
	v.poll.at = time.Now().Add(time.Hour)
	return v
}

// previewGit 切到 w1 的改动列表，打开 main.go 的 diff。
func previewGit(t *testing.T, a *app) {
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	gitRun(t, v.root, "stash", "push", "-q", "-m", "试验：换一种日志格式", "--", "README.md")
	until(t, tt, "stash", func() bool { return len(v.git.stashes) == 1 })
	for _, r := range v.git.rows {
		if !r.header && r.entry.Path == "main.go" {
			a.openDiff(&a.workspaces[0], changeSpec(r.entry), nil, true)
		}
	}
	until(t, tt, "diff", func() bool { return v.cur != nil && v.cur.kind == docDiff && v.cur.diff.jumped })
	v.git.message = "feat(cmd): 启动时记录配置"
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

func previewBranches(t *testing.T, a *app) {
	branchWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	a.openBranches(&a.workspaces[0])
	v := a.files["w1"]
	until(t, tt, "branches", func() bool { return v.git.branches.list != nil })
}

// previewImageDiff 打开一张改了尺寸和颜色的图片的 diff。
func previewImageDiff(t *testing.T, a *app) {
	root := gitWorkspace(t, a)
	pngOf := func(w, h int, c color.RGBA) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if (x-w/2)*(x-w/2)+(y-h/2)*(y-h/2) < (h/2)*(h/2) {
					img.SetRGBA(x, y, c)
				}
			}
		}
		var b bytes.Buffer
		png.Encode(&b, img)
		return b.Bytes()
	}
	os.WriteFile(filepath.Join(root, "logo.png"), pngOf(320, 240, color.RGBA{217, 119, 87, 255}), 0o644)
	gitRun(t, root, "add", "logo.png")
	gitRun(t, root, "commit", "-q", "-m", "logo")
	os.WriteFile(filepath.Join(root, "logo.png"), pngOf(480, 360, color.RGBA{90, 130, 220, 255}), 0o644)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], diffSpec{rel: "logo.png", oldRev: "HEAD"}, nil, true)
	until(t, tt, "image diff", func() bool { return v.cur.kind == docImageDiff })
	a.state.RightPanel = ""
}

// previewCompare 打开 feat 和 main 的比较，选中 feat 独有的那个提交。
func previewCompare(t *testing.T, a *app) {
	branchWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	w := &a.workspaces[0]
	v := a.filesFor(w)
	a.loadBranches(v) // 顺带查出仓库根目录
	until(t, tt, "repo", func() bool { return v.git.repo != "" })
	a.openCompare(w, v, "feat", "main", false)
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare })
	cd := v.cur.comp
	a.selectCommit(v, &cd.commit, cd.onlyOther[0])
	until(t, tt, "commit files", func() bool { return cd.commit.files != nil })
}

func previewPush(t *testing.T, a *app) {
	branchWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	a.openPush(&a.workspaces[0])
	until(t, tt, "push plan", func() bool { return a.push.loaded })
	a.selectCommit(a.files["w1"], &a.push.commit, a.push.plan.Commits[0])
	until(t, tt, "commit files", func() bool { return a.push.commit.files != nil })
}

// previewLog 把 feat 合并进 main，打开日志并选中合并提交。
func previewLog(t *testing.T, a *app) {
	root := branchWorkspace(t, a)
	gitRun(t, root, "merge", "-q", "--no-ff", "-m", "Merge branch 'feat'", "feat")
	gitRun(t, root, "tag", "v0.2.0")
	tt := ui.NewTester(a.build, 1200, 760)
	a.openLog(&a.workspaces[0], "", "")
	v := a.files["w1"]
	until(t, tt, "log", func() bool { return v.cur.kind == docLog })
	a.selectCommit(v, &v.cur.log.commit, v.cur.log.commits[0])
	until(t, tt, "detail", func() bool { return v.cur.log.commit.files != nil })
}

// previewUsageEvents 是一段从 10:02 开始的使用记录，最后几条是连着调同一个工具。
func previewUsageEvents() []history.UsageEvent {
	at := time.Date(2026, 10, 16, 10, 2, 0, 0, time.Local)
	var evs []history.UsageEvent
	add := func(min int, e history.UsageEvent) {
		e.At = at.Add(time.Duration(min) * time.Minute)
		evs = append(evs, e)
	}
	add(0, history.UsageEvent{Skill: "computer-use"})
	add(1, history.UsageEvent{Server: "codex-cu", Tool: "js_reset"})
	add(2, history.UsageEvent{Server: "codex-cu", Tool: "js"})
	add(3, history.UsageEvent{Server: "codex-cu", Tool: "js"})
	add(9, history.UsageEvent{Skill: "chatgpt-imagegen"})
	add(12, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	add(13, history.UsageEvent{Server: "androidmirror", Tool: "screen_act"})
	add(14, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	add(15, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	return evs
}

func savePNG(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("HIVE_PREVIEW_DIR")
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func newPreviewTester(a *app, dark bool) *ui.Tester {
	tt := ui.NewTester(a.build, 1200, 760)
	tt.SetScale(2)
	tt.SetDark(dark)
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 78})
	// 背景色有 120ms 的过渡，等它走完再取图
	for range 3 {
		tt.Frame()
		time.Sleep(80 * time.Millisecond)
	}
	tt.Frame()
	return tt
}

// TestPreview 把主要界面渲染成 PNG，供改界面时对照；设置 HIVE_PREVIEW_DIR 才运行。
func TestPreview(t *testing.T) {
	if os.Getenv("HIVE_PREVIEW_DIR") == "" {
		t.Skip("set HIVE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		a := previewApp(t)
		savePNG(t, newPreviewTester(a, dark), "main-"+suffix+".png")

		// 右侧"技能与 MCP"面板：刚读过一次，预览期间不会再去找会话文件
		a = previewApp(t)
		a.state.RightPanel = panelUsage
		a.usage = usageView{term: "t1", found: true, poll: panelPoll{at: time.Now()}, data: history.Usage{
			Skills: []history.UsageItem{{Name: "computer-use", Count: 3}, {Name: "chatgpt-imagegen", Count: 1}},
			MCP: []history.MCPUsage{
				{Server: "codex-cu", Count: 55, Tools: []history.UsageItem{{Name: "js", Count: 54}, {Name: "js_reset", Count: 1}}},
				{Server: "androidmirror", Count: 4, Tools: []history.UsageItem{{Name: "screen_observe", Count: 3}, {Name: "screen_act", Count: 1}}},
			},
			Events: previewUsageEvents(),
		}}
		savePNG(t, newPreviewTester(a, dark), "usage-"+suffix+".png")

		// 右侧历史面板：当前项目的会话已经查过一次
		a = previewApp(t)
		previewHistory(a, "")
		a.selected = "w1" // previewHistory 会切到历史页
		a.state.RightPanel = panelHistory
		a.recent = recentView{key: "0\x00p1", loaded: true, poll: panelPoll{at: time.Now()}, sessions: a.history.sessions,
			previews: map[string]recentPreview{}}
		for i, s := range a.history.sessions {
			p := recentPreview{at: s.UpdatedAt, text: "迁移按三步走：先把终端和常驻进程换成 Go，再做多 agent 编排，最后补手机遥控。内存目标是窗口关掉后只剩 20 MB。"}
			if i == 1 {
				p = recentPreview{at: s.UpdatedAt, user: true, text: "清理一下 ~/Library/Caches 里超过 1 GB 的目录"}
			}
			a.recent.previews[sessionKey(s)] = p
		}
		savePNG(t, newPreviewTester(a, dark), "recent-"+suffix+".png")

		a = previewApp(t)
		a.selected = "w2"
		savePNG(t, newPreviewTester(a, dark), "empty-"+suffix+".png")

		a = previewApp(t)
		a.projects, a.workspaces, a.selected = nil, nil, ""
		a.indexWorkspaces()
		savePNG(t, newPreviewTester(a, dark), "first-run-"+suffix+".png")

		a = previewApp(t)
		a.openNewWorkspace()
		a.newProject = "Skill"
		savePNG(t, newPreviewTester(a, dark), "new-workspace-"+suffix+".png")

		a = previewApp(t)
		a.selected, a.taskSel = taskBoardID, "k2"
		a.detail = &taskDetail{taskInfo: *a.task("k2"), Messages: []taskMessage{
			{ID: "m1", Kind: "note", Body: "钥匙串的 service 名用 com.example.skill。", CreatedAt: time.Now().Add(-20 * time.Minute)},
			{ID: "m2", Kind: "question", Body: "现在的单测用的是内存版钥匙串，要不要给它补一个“记住密码”的用例？还是只测登录流程？", CreatedAt: time.Now().Add(-2 * time.Minute)},
		}}
		savePNG(t, newPreviewTester(a, dark), "tasks-"+suffix+".png")

		a = previewApp(t)
		a.selected, a.taskSel = taskBoardID, "k3"
		a.detail = &taskDetail{taskInfo: *a.task("k3")}
		savePNG(t, newPreviewTester(a, dark), "task-done-"+suffix+".png")

		a = previewApp(t)
		a.selected, a.tasks = taskBoardID, nil
		a.indexTasks()
		savePNG(t, newPreviewTester(a, dark), "tasks-empty-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "")
		savePNG(t, newPreviewTester(a, dark), "history-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "磁盘")
		savePNG(t, newPreviewTester(a, dark), "history-search-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "nothing")
		a.history.hits, a.history.sel, a.history.detail = nil, "", nil
		savePNG(t, newPreviewTester(a, dark), "history-empty-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "")
		previewMarkdown(a)
		savePNG(t, newPreviewTester(a, dark), "history-markdown-"+suffix+".png")

		a = previewApp(t)
		a.openDispatch()
		a.dWorkspace, a.dMode = "Skill/main", 1
		a.dAgents = [nAgents]bool{true, true}
		a.dTitle, a.dSpec = "给登录页加上记住密码", "在登录页加一个“记住密码”开关，密码存进系统钥匙串。"
		savePNG(t, newPreviewTester(a, dark), "dispatch-"+suffix+".png")

		// 文件标签：树展开到打开的文件，README 有未保存的修改
		a = previewApp(t)
		v := previewFiles(t, a, "internal/app/view.go", "README.md", "main.go")
		v.doc(filepath.Join(v.root, "README.md")).ed.Insert("<!-- draft -->\n")
		savePNG(t, newPreviewTester(a, dark), "files-"+suffix+".png")

		a = previewApp(t)
		v = previewFiles(t, a, "README.md")
		v.cur.preview = true
		savePNG(t, newPreviewTester(a, dark), "files-markdown-"+suffix+".png")

		a = previewApp(t)
		v = previewFiles(t, a, "main.go")
		v.side, v.search.query.Text = 1, "config"
		a.startSearch(v, 0)
		until(t, ui.NewTester(a.build, 1200, 760), "search", func() bool { return !v.search.running && len(v.search.hits) > 0 })
		savePNG(t, newPreviewTester(a, dark), "files-search-"+suffix+".png")

		a = previewApp(t)
		previewFiles(t, a, "main.go")
		a.openQuick(&a.workspaces[0])
		a.quick.query = "view"
		savePNG(t, newPreviewTester(a, dark), "quick-open-"+suffix+".png")

		// 左栏"改动"和 main.go 的 diff
		a = previewApp(t)
		previewGit(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-changes-"+suffix+".png")

		// 分支弹窗：在终端标签上点分支名打开
		a = previewApp(t)
		previewBranches(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-branches-"+suffix+".png")

		a = previewApp(t)
		previewPush(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-push-"+suffix+".png")

		a = previewApp(t)
		previewCompare(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-compare-"+suffix+".png")

		a = previewApp(t)
		previewImageDiff(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-image-diff-"+suffix+".png")

		a = previewApp(t)
		previewLog(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-log-"+suffix+".png")

		a = previewApp(t)
		url := "orca://pair?code=eyJ2IjoyLCJlbmRwb2ludCI6IndzOi8vMTcyLjMxLjAuMjMwOjY3ODgiLCJkZXZpY2VUb2tlbiI6IjAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwIn0"
		img, err := qrImage(url)
		if err != nil {
			t.Fatal(err)
		}
		a.state.MobileAddress = "https://orca.example.uk/v11v7w0"
		a.state.MobileAddresses = []string{a.state.MobileAddress}
		a.mobile = mobileView{open: true, url: url, endpoint: "wss://orca.example.uk/v11v7w0", qr: ui.NewBitmap(img),
			devices: []mobileDevice{{ID: "d1", Name: "手机 10-16 00:30", LastSeen: time.Now().Add(-3 * time.Minute)}}}
		savePNG(t, newPreviewTester(a, dark), "mobile-"+suffix+".png")

		// 底栏的内存浮窗
		a = previewApp(t)
		w := a.workspaces[0]
		claude := []memProc{{"claude", 420 << 10, 1}, {"@sentry/mcp-server", 115 << 10, 1}, {"@mobilenext/mobile-mcp", 91 << 10, 1},
			{"codex-cu-mcp", 30 << 10, 1}, {"iphone-use-mcp", 7 << 10, 2}, {"androidmirror-mcp-server", 5 << 10, 1}, {"zsh", 5 << 10, 1}}
		a.mem = memView{open: true, poll: panelPoll{at: time.Now().Add(time.Hour)}, terms: 1460 << 10, expanded: map[string]bool{"t1": true},
			rows: []memWorkspace{
				{id: w.ID, label: displayName(&w), kb: 1210 << 10, terms: []memTerminal{
					{w.Terminals[0], 820 << 10, claude}, {w.Terminals[1], 360 << 10, nil}, {w.Terminals[2], 30 << 10, nil}}},
				{id: "w3", label: "react-native-syan-image-picker", kb: 250 << 10, terms: []memTerminal{{a.workspaces[2].Terminals[0], 250 << 10, nil}}},
			}}
		savePNG(t, newPreviewTester(a, dark), "memory-"+suffix+".png")
	}
}
