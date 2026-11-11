package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/history"
	"posse/internal/media"
	"posse/internal/skills"
	"posse/internal/usage"
	"posse/locales"
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
		editorSize:   defaultEditorFontSize,
		loaded:       true,
		daemon:       daemonStatus{FootprintKB: 17 << 10, Running: 4, Awake: &awakeStatus{Mode: "auto", Active: true, Working: 2}},
		selfKB:       150 << 10,
	}
	// Apps 指向本机真实的目录：不去找下面的仓库，截图不随本机磁盘变
	a.projects = []project{
		{ID: "p1", Name: "Apps", SingleRepo: true}, {ID: "p2", Name: "Chat"}, {ID: "p3", Name: "react-native-syan-image-picker"},
		{ID: "p4", Name: "Server"}, {ID: "p5", Name: "Skill", Kind: "git"}, {ID: "p6", Name: "Works"},
	}
	a.workspaces = []workspace{
		{ID: "w1", ProjectID: "p1", Name: "main", Kind: "main", Project: "Apps", Label: "Apps/main", Path: os.Getenv("HOME") + "/Desktop/AI/Apps",
			Terminals: []terminalInfo{
				{ID: "t1", Agent: "claude", Title: "✳ 重写 posse 界面", Running: true, AgentState: state("waiting")},
				{ID: "t2", Agent: "codex", Title: "codex", Running: true, AgentState: state("working")},
				{ID: "t3", Title: "zsh", Running: true},
			}},
		{ID: "w2", ProjectID: "p2", Name: "main", Kind: "main", Project: "Chat", Label: "Chat/main", Path: "/Users/x/Chat"},
		{ID: "w3", ProjectID: "p3", Name: "main", Kind: "main", Project: "react-native-syan-image-picker", Label: "r/main", Path: "/Users/x/rn",
			Terminals: []terminalInfo{{ID: "t4", Agent: "claude", Running: true, AgentState: state("idle")}}},
		{ID: "w4", ProjectID: "p4", Name: "main", Kind: "main", Project: "Server", Label: "Server/main", Path: "/Users/x/Server"},
		{ID: "w5", ProjectID: "p5", Name: "main", Kind: "main", Project: "Skill", Label: "Skill/main", Branch: "main", Path: "/Users/x/Skill"},
		{ID: "w6", ProjectID: "p5", Name: "feat-login", Kind: "worktree", Project: "Skill", Label: "Skill/feat-login", Branch: "feat-login", Path: "/Users/x/.posse/worktrees/Skill/feat-login",
			Terminals: []terminalInfo{{ID: "t5", Agent: "codex", Running: true, AgentState: state("working")}}},
		{ID: "w9", ProjectID: "p5", Name: "ios-crash", Kind: "worktree", Project: "Skill", Label: "Skill/ios-crash", Branch: "hotfix/ios-crash-212", Path: "/Users/x/.posse/worktrees/Skill/ios-crash"},
		{ID: "w7", ProjectID: "p6", Name: "app", Kind: "main", Project: "Works", Label: "Works/app", Path: "/Users/x/Works"},
		{ID: "w8", ProjectID: "p6", Name: "web", Kind: "folder", Project: "Works", Label: "Works/web", Path: "/Users/x/Works"},
	}
	for _, p := range a.projects {
		open := true
		a.sectionsOpen[p.ID] = &open
	}
	now := time.Now()
	a.daemon.Usage = previewPlan(now)
	a.workspaces[0].Terminals = append(a.workspaces[0].Terminals, terminalInfo{ID: "t6", Agent: "grok", Title: "⠹ - Running tool - grok", Running: true, AgentState: state("working")})
	ended := now.Add(-3 * time.Minute)
	a.tasks = []taskInfo{
		{ID: "k1", Title: "真机验证动图帧回调空指针修复", Agent: "grok", Workspace: "Apps/main", WorkspaceID: "w1", Worker: "t6", Status: "running",
			Coordinator: "t1", CreatedAt: now.Add(-12 * time.Minute), Terminal: &terminalInfo{ID: "t6", Agent: "grok", Running: true, AgentState: state("working")}},
		{ID: "k2", Title: "给登录页加上记住密码", Agent: "codex", Workspace: "Skill/feat-login", WorkspaceID: "w6", WorkspaceKind: "worktree", Branch: "feat-login", BaseRef: "main",
			Worker: "t5", Status: "running", Questions: 1, CreatedAt: now.Add(-25 * time.Minute), Terminal: &terminalInfo{ID: "t5", Agent: "codex", Running: true, AgentState: state("working")},
			Spec: "# 登录页记住密码\n\n在登录页加一个“记住密码”开关，密码存进系统钥匙串。\n\n## 约束\n- 不要改注册流程\n- 只改 `LoginView.swift` 和 `Keychain.swift`\n\n## 完成标准\n1. 单测通过：`swift test`\n2. 手动登录两次，第二次免输密码"},
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
	a.panes[0] = &termView{id: "t1", term: term}
	return a
}

// previewShell 是跑着开发服务器的 zsh，给分屏的右栏。
func previewShell(t *testing.T) *terminal.Terminal {
	light, dark := terminalThemes()
	term, err := terminal.New(terminal.Options{Conn: nopConn{make(chan struct{})}, Font: terminal.Font{Family: termFontFamily, Size: defaultFontSize}, Theme: light, DarkTheme: dark})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	term.Feed([]byte("~/Desktop/AI/Apps \x1b[32m❯\x1b[0m npm run dev\r\n\r\n" +
		"  \x1b[32m\x1b[1mVITE\x1b[0m v6.2.0  ready in 312 ms\r\n\r\n" +
		"  ➜  \x1b[1mLocal\x1b[0m:   \x1b[36mhttp://localhost:5173/\x1b[0m\r\n" +
		"  ➜  Network: use --host to expose\r\n\r\n" +
		"\x1b[2m10:42:07\x1b[0m \x1b[36m[vite]\x1b[0m hmr update /src/App.tsx\r\n"))
	return term
}

// previewHistory 填一份历史页的数据：四家 agent 的会话、一次搜索的命中和一个打开的会话。
func previewHistory(a *app, query string) {
	now := time.Now()
	home := os.Getenv("HOME")
	// 分组按日历日算：凌晨跑的时候"几小时前"会落到昨天，所以今天的两条不早于今天零点，昨天那条放在昨天中午
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	sinceMidnight := now.Sub(today)
	sessions := []history.Session{
		{Agent: "claude", ID: "s1", Title: "Orca 到 mygo 迁移方案", Cwd: home + "/Desktop/AI/Apps", UpdatedAt: now.Add(-min(20*time.Minute, sinceMidnight/4)), Messages: 412, Model: "claude-opus-5-5"},
		{Agent: "grok", ID: "s2", Title: "清理磁盘", Cwd: home + "/Desktop/AI/Grok", Branch: "main", UpdatedAt: now.Add(-min(3*time.Hour, sinceMidnight/2)), Messages: 630, Model: "grok-4.7-build"},
		{Agent: "codex", ID: "s3", Title: "0927｜文档｜统一去除章节标题序号", Cwd: home + "/Desktop/AI/Claude", UpdatedAt: today.AddDate(0, 0, -1).Add(12 * time.Hour), Messages: 15, Model: "gpt-6-astra"},
		{Agent: "cursor", ID: "s4", Title: "TikTok Ark Video", Cwd: home + "/Desktop/AI/Video/Halloween-3", UpdatedAt: now.Add(-50 * time.Hour), Messages: 58},
		{Agent: "claude", ID: "s5", Title: "0918｜修复｜im_sdk 拉黑后消息页空转鉴权问题", Cwd: home + "/Desktop/AI/Works", UpdatedAt: now.Add(-12 * 24 * time.Hour), Messages: 96},
		{Agent: "grok", ID: "s6", Title: "Ghostty 快捷键与浅色主题轮播", Cwd: home + "/Desktop/AI/Grok", UpdatedAt: now.Add(-46 * 24 * time.Hour), Messages: 72},
	}
	h := &a.history
	h.loaded, h.status = true, historyStatus{Sessions: 424, Messages: 18342, ByAgent: map[string]int{"claude": 251, "codex": 120, "cursor": 9, "grok": 44}}
	a.selected = historyBoardID
	h.query, h.resQuery = query, query
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
	h.sel, h.hitSeq, h.placed = "claude:s1", 3, false
	h.detail = &historyDetail{Session: sessions[0], Messages: []history.Message{
		{Seq: 0, Role: "user", Text: "我的 Mac 磁盘快满了，帮我看看能清理什么", At: now.Add(-40 * time.Minute)},
		{Seq: 1, Role: "assistant", Text: "先看各目录占用，再按能否安全删除分类。", At: now.Add(-39 * time.Minute)},
		{Seq: 2, Role: "tool", Text: "Bash du -sh ~/Library/Caches ~/Library/Developer", At: now.Add(-39 * time.Minute)},
		{Seq: 3, Role: "assistant", Text: "Caches 有 12G，Xcode 的 DerivedData 有 31G。先看看 Library/Caches 有多大，再决定清理磁盘上的哪些缓存：\n\n- DerivedData 可以整个删，下次编译会重建\n- 模拟器运行时用 xcrun simctl delete unavailable 清理", At: now.Add(-38 * time.Minute)},
		{Seq: 4, Role: "user", Text: "DerivedData 删掉吧，模拟器先别动", At: now.Add(-30 * time.Minute)},
	}}
	h.detail.context, h.detail.contextOK = history.Context{Used: 372_480, Window: 1_000_000}, true
}

// previewMarkdown 打开一个回复里有标题、表格、列表和代码的会话。
func previewMarkdown(a *app) {
	h := &a.history
	now := time.Now()
	h.sel, h.hitSeq, h.placed = "claude:s1", -1, false
	h.detail = &historyDetail{Session: h.sessions[0], Messages: []history.Message{
		{Seq: 0, Role: "user", Text: "评估一下用 mygo 重写 Orca", At: now.Add(-40 * time.Minute)},
		{Seq: 1, Role: "assistant", At: now.Add(-39 * time.Minute), Text: "我按 [egoist/mygo](https://pkg.go.dev/github.com/egoist/mygo) 来理解\"mygo\"，先实测了 Orca。\n\n" +
			"## 结论\n\n可以重写，但性能提升主要来自两点：**只做这三个功能**，后台换成 Go 常驻进程。\n\n" +
			"## 实测现状（已核实）\n\n| 项目 | 数据 |\n|---|---|\n| Orca 版本 | 1.4.224，Electron 加一个 Node 后台进程 |\n| 自身内存 | 约 1.1 GB：主进程 235 MB，界面进程 442 MB |\n| 每个 agent 终端 | 540 到 700 MB，**重写后不会变** |\n\n" +
			"下一步：\n\n1. 先做终端和 `posse daemon`\n2. 再做编排\n   - worktree 隔离\n   - 任务汇报\n\n- [x] 调研 Orca\n- [ ] 手机遥控\n\n" +
			"> 换框架本身带来的收益有限。\n\n```go\nfunc main() {\n\tmygo.Run(app)\n}\n```"},
	}}
}

// previewMermaid 打开一个回复里有 Mermaid 流程图、时序图和一张写错的图的会话。
func previewMermaid(a *app) {
	h := &a.history
	now := time.Now()
	h.sel, h.hitSeq, h.placed = "claude:s1", -1, false
	h.detail = &historyDetail{Session: h.sessions[0], Messages: []history.Message{
		{Seq: 0, Role: "user", Text: "画一下常驻进程和界面的关系", At: now.Add(-40 * time.Minute)},
		{Seq: 1, Role: "assistant", At: now.Add(-39 * time.Minute), Text: "## 结构\n\n" +
			"```mermaid\nflowchart LR\n  UI[窗口] -- terminal.attach --> D[常驻进程]\n  CLI[posse CLI] --> D\n  D --> T1[终端 1]\n  D --> T2{还活着?}\n  T2 -- 是 --> K[保留副本]\n```\n\n" +
			"## 接入\n\n```mermaid\nsequenceDiagram\n  participant U as 窗口\n  participant D as 常驻进程\n  U->>D: attach\n  D-->>U: 快照\n  D-->>U: 实时输出\n```\n\n" +
			"```mermaid\nsequenceDiagram\n  A->>B hi\n```"},
	}}
}

// mermaidTester 等后台把图画完再截图。
func mermaidTester(t *testing.T, a *app, dark bool) *ui.Tester {
	tt := newPreviewTester(a, dark)
	until(t, tt, "mermaid", func() bool { return !diagramsPending() })
	tt.Frame()
	return tt
}

// previewFiles 在 w1 的文件标签里依次打开 rels（最后一个是当前文件），等它们读进来；之后不再轮询磁盘。
func previewFiles(t *testing.T, a *app, rels ...string) *filesView {
	root := filesWorkspace(t, a)
	// 文件树里的符号链接（链到别处的目录、文件）换带箭头的图标
	shared := t.TempDir()
	os.WriteFile(filepath.Join(shared, "util.go"), []byte("package shared\n"), 0o644)
	os.Symlink(shared, filepath.Join(root, "shared"))
	os.Symlink("README.md", filepath.Join(root, "CLAUDE.md"))
	// Xcode 的包目录换成各自的图标
	for _, dir := range []string{"ios/Runner.xcodeproj", "ios/Runner.xcworkspace", "ios/Runner/Assets.xcassets/AppIcon.appiconset", "ios/Runner/Assets.xcassets/LaunchImage.imageset", "ios/Runner/Base.lproj"} {
		os.MkdirAll(filepath.Join(root, dir), 0o755)
	}
	// 各类文件按类型换图标和颜色
	for _, name := range []string{"package.json", "app.ts", "index.js", "logo.png", "build.sh", "go.sum", "styles.css", "index.html", "Dockerfile", "data.csv", "demo.mp4", "notes.txt", "docs.pdf", "dist.zip", "schema.sql", "main.dart", "pubspec.yaml", "AppDelegate.swift", "Main.storyboard", "MainActivity.kt", "build.gradle.kts", "AndroidManifest.xml", "App.java", "App.vue", "upload.jks", "Bridging-Header.h", "AppDelegate.m"} {
		os.WriteFile(filepath.Join(root, name), nil, 0o644)
	}
	w := &a.workspaces[0]
	a.setProjectPanel(&a.state.FilesPanels, w.ProjectID, true)
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	gitRun(t, v.root, "stash", "push", "-q", "-m", "试验：换一种日志格式", "--", "README.md")
	until(t, tt, "stash", func() bool { return len(v.git.stashes) == 1 })
	for _, r := range v.git.rows {
		if !r.header && r.entry.Path == "main.go" {
			a.openDiff(&a.workspaces[0], changeSpec(v.git.repo, r.entry), nil, true)
		}
	}
	until(t, tt, "diff", func() bool { return v.cur != nil && v.cur.kind == docDiff && v.cur.diff.jumped })
	v.ch.message = "feat(cmd): 启动时记录配置"
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

// previewWorktree 把 w1 换成和基准分支有冲突的 worktree，打开改动页的 tab 分页。
func previewWorktree(t *testing.T, a *app, tab int) {
	worktreeWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	until(t, tt, "base", func() bool { return trackOf(a, "w1") != nil && trackOf(a, "w1").baseOK })
	v.ch.tab = tab
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
	trackOf(a, "w1").poll.at = time.Now().Add(time.Hour)
}

// previewMultiRepo 是工作区下面有几个仓库（子目录里的、软链过去的）：改动页按仓库分组，打开 ext 的 diff。
// branches 为真时再打开分支弹窗，最上面是选仓库。
func previewMultiRepo(t *testing.T, a *app, branches bool) {
	root := multiRepoWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "status", func() bool {
		if a.filesPanelShown != 1 || !v.repos.scanned {
			return false
		}
		for _, g := range v.repos.list {
			if g.status == nil {
				return false
			}
		}
		return true
	})
	ext := v.repoAt(filepath.Join(root, "ext"))
	a.openDiff(w, changeSpec(ext.repo, ext.status.Entries[0]), nil, true)
	until(t, tt, "diff", func() bool { return v.cur != nil && v.cur.kind == docDiff && v.cur.diff.jumped && v.git == ext })
	v.ch.message = "feat: 两个仓库一起提交"
	if branches {
		a.openBranches(w)
		until(t, tt, "branches", func() bool { return ext.branches.list != nil })
	}
	v.poll.at = time.Now().Add(time.Hour)
	for _, g := range v.repos.list {
		g.poll.at = time.Now().Add(time.Hour)
	}
	v.repos.scanAt, v.repos.statAt = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
	if tr := trackOf(a, w.ID); tr != nil {
		tr.poll.at = time.Now().Add(time.Hour)
	}
}

// previewRollback 在几层目录里各改几个文件，打开批量回滚对话框，收起其中一个目录。
func previewRollback(t *testing.T, a *app) {
	root := gitWorkspace(t, a)
	for _, rel := range []string{"internal/app/view.go", "internal/app/rollback.go", "internal/gitx/status.go", "docs/plans/git-ops.html", "design/notes.md"} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte("package x\n"), 0o644)
	}
	gitRun(t, root, "add", "internal", "docs")
	gitRun(t, root, "commit", "-q", "-m", "tree")
	os.WriteFile(filepath.Join(root, "internal/app/view.go"), []byte("package x\n\nfunc view() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "internal/gitx/status.go"), []byte("package y\n"), 0o644)
	os.Remove(filepath.Join(root, "docs/plans/git-ops.html"))
	tt := newTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	until(t, tt, "status", func() bool { return v.git.changeCount() == 7 })
	a.openRollback(v)
	a.rollback.folded["design"] = true
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

// previewChangesByDir 用回滚对话框那组改动，把改动页切成按目录分组。
func previewChangesByDir(t *testing.T, a *app) {
	previewRollback(t, a)
	a.rollback.open = false
	a.state.ChangesByDir = true
}

func previewChangePop(t *testing.T, a *app) {
	root := gitWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openFile(&a.workspaces[0], filepath.Join(root, "main.go"), 0, [2]int{})
	d := v.cur
	until(t, tt, "diff base", func() bool { return d.kind == docText && len(d.ed.Changes()) == 2 })
	d.ed.OnChangeClick(0, 0)
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

func previewRollbackDetail(t *testing.T, a *app) {
	gitWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openRollback(v)
	e, _ := a.rollback.entry("main.go")
	a.rollbackShow(e, true)
	until(t, tt, "detail", func() bool { return a.rollback.detail.kind == docDiff && a.rollback.detail.diff.jumped })
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

func previewBranches(t *testing.T, a *app) {
	branchWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], diffSpec{rel: "logo.png", oldRev: "HEAD"}, nil, true)
	until(t, tt, "image diff", func() bool { return v.cur.kind == docImageDiff })
	a.state.RightPanel = ""
}

// previewImageDoc 在文件标签里打开一张带透明的竖长图，zoom 不为 0 时放大到这个比例。
func previewImageDoc(t *testing.T, a *app, zoom float32) {
	root := filesWorkspace(t, a)
	img := image.NewNRGBA(image.Rect(0, 0, 600, 1300))
	for y := 0; y < 1300; y++ {
		for x := 0; x < 600; x++ {
			if (x/50+y/50)%3 != 0 {
				img.SetNRGBA(x, y, color.NRGBA{217, 119, 87, uint8(80 + y*175/1300)})
			}
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	os.WriteFile(filepath.Join(root, "tall.png"), b.Bytes(), 0o644)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "tall.png"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "image", func() bool { return v.cur.kind == docImage })
	tt.Frame()
	if zoom != 0 {
		v.cur.setZoom(zoom, v.cur.iv.vw/2, v.cur.iv.vh/3)
	}
	v.poll.at = time.Now().Add(time.Hour)
}

// previewHugeImageDoc 在文件标签里打开一张原尺寸 8000×7000 的图（只有文件头是这么写的）。
func previewHugeImageDoc(t *testing.T, a *app) {
	root := filesWorkspace(t, a)
	path := filepath.Join(root, "huge.png")
	os.WriteFile(path, pngClaiming(8000, 7000), 0o644)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, path, 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "too large", func() bool { return v.cur.kind == docTooLarge })
	v.poll.at = time.Now().Add(time.Hour)
}

// previewCompare 打开 feat 和 main 的比较，选中 feat 独有的那个提交。
func previewCompare(t *testing.T, a *app) {
	branchWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	w := &a.workspaces[0]
	v := a.filesFor(w)
	a.loadBranches(v.git) // 顺带查出仓库根目录
	until(t, tt, "repo", func() bool { return v.git.repo != "" })
	a.openCompare(w, "feat", "main", false)
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare })
	cd := v.cur.comp
	a.selectCommit(v.git.repo, &cd.commit, cd.onlyOther[0])
	until(t, tt, "commit files", func() bool { return cd.commit.files != nil })
}

func previewPush(t *testing.T, a *app) {
	branchWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	a.openPush(locales.In("zh-Hans"), &a.workspaces[0])
	until(t, tt, "push plan", func() bool { return a.push != nil && a.push.loaded })
	a.selectCommit(a.files["w1"].git.repo, &a.push.commit, a.push.plan.Commits[0])
	until(t, tt, "commit files", func() bool { return a.push.commit.files != nil })
}

func previewPushDetail(t *testing.T, a *app) {
	previewPush(t, a)
	tt := newTester(a.build, 1200, 760)
	a.pushShow(a.files["w1"], a.push, 0, true)
	until(t, tt, "push diff", func() bool { return a.push.detail.kind == docDiff })
}

// previewLog 把 feat 合并进 main，打开日志并选中合并提交。
func previewLog(t *testing.T, a *app) {
	root := branchWorkspace(t, a)
	gitRun(t, root, "merge", "-q", "--no-ff", "-m", "Merge branch 'feat'", "feat")
	gitRun(t, root, "tag", "v0.2.0")
	tt := newTester(a.build, 1200, 760)
	openLogWait(t, a, tt, &a.workspaces[0], "")
	v := a.files["w1"]
	until(t, tt, "log", func() bool { return v.logCur != nil && v.logCur.kind == docLog })
	a.selectCommit(v.git.repo, &v.logCur.log.commit, v.logCur.log.commits[0])
	until(t, tt, "detail", func() bool { return v.logCur.log.commit.files != nil })
}

func previewConsole(t *testing.T, a *app) {
	previewLog(t, a)
	dir := a.workspaces[0].Path
	at := time.Date(2026, 10, 18, 14, 3, 0, 0, time.Local)
	id := int64(0)
	add := func(op string, opID int64, dir string, sec int, r gitx.Run) {
		id++
		r.ID, r.Op, r.OpID, r.Dir, r.Start, r.Done = id, op, opID, dir, at.Add(time.Duration(sec)*time.Second), true
		a.gitRuns = append(a.gitRuns, r)
	}
	// 连着几次没拉到东西的自动 Fetch 合成一条
	for i := range 3 {
		add("自动 Fetch", int64(100+i), dir, -900+i*300, gitx.Run{Args: []string{"fetch", "--all"}, Took: 900 * time.Millisecond})
	}
	add("自动 Fetch", 1, "/Users/x/Skill", 0, gitx.Run{Args: []string{"fetch", "--all"}, Took: 1240 * time.Millisecond,
		Out: "From github.com:dengbo/skill\n   7117cfb..acc6bc5  main       -> origin/main"})
	add("提交", 2, dir, 40, gitx.Run{Args: []string{"add", "-A", "--", ":(literal)internal/app/view.go"}})
	add("提交", 2, dir, 40, gitx.Run{Args: []string{"commit", "-q", "--only", "-F", "-", "--", ":(literal)internal/app/view.go"}, Took: 310 * time.Millisecond})
	add("Push", 3, dir, 80, gitx.Run{Args: []string{"push", "--force-with-lease", "--force-if-includes", "origin", "main:main"}, Took: 2100 * time.Millisecond,
		Err: "To github.com:dengbo/posse.git\n ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs to 'github.com:dengbo/posse.git'"})
	add("更新项目", 4, dir, 120, gitx.Run{Args: []string{"pull", "--rebase", "--autostash"}, Took: 1800 * time.Millisecond, Out: "Successfully rebased and updated refs/heads/main."})
	add("更新项目", 4, dir, 122, gitx.Run{Args: []string{"diff", "--name-only", "--diff-filter=U"}})
	add("Push", 5, dir, 240, gitx.Run{Args: []string{"push", "origin", "main:main"}})
	a.gitRuns[len(a.gitRuns)-1].Done = false
	// 终端里 agent 执行的 git 命令
	a.addTermRuns([]termGitRun{
		{Seq: 1, Agent: "claude", Dir: dir, Args: []string{"commit", "-m", "fix(app): 修复标签拖动"}, Start: at.Add(60 * time.Second), TookMs: 180},
		{Seq: 2, Agent: "codex", Dir: dir, Args: []string{"pull", "--rebase"}, Start: at.Add(150 * time.Second), TookMs: 1300, Code: 1},
	}, true)
	a.files["w1"].console = true
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
	add(13, history.UsageEvent{Server: "androidmirror", Tool: "screen_act", Failed: true})
	add(14, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	add(15, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	add(16, history.UsageEvent{CLI: "go", Source: history.SourceHomebrew, Command: "go vet ./... && go test ./internal/history"})
	add(17, history.UsageEvent{CLI: "bsk", Command: "bsk snapshot --session abcd"})
	add(18, history.UsageEvent{CLI: "git", Source: history.SourceSystem, Command: "git status --short"})
	add(19, history.UsageEvent{URL: "https://github.com/egoist/mygo/issues/12", Via: history.ViaUser})
	add(20, history.UsageEvent{Query: "ghostty vt scrollback compression", Via: "WebSearch"})
	add(21, history.UsageEvent{URL: "https://docs.rs/merman/0.6.2", Via: "WebFetch"})
	add(22, history.UsageEvent{URL: "https://posse.shaoyan.uk/android/latest.json", Via: "curl"})
	return evs
}

// previewUsagePanel 打开右侧"技能与工具"面板：刚读过一次，预览期间不会再去找会话文件。
func previewUsagePanel(a *app) {
	a.state.RightPanel = panelUsage
	a.usage = usageView{term: "t1", found: true, poll: panelPoll{at: time.Now()}, data: history.Usage{
		Skills: []history.UsageItem{{Name: "computer-use", Count: 3}, {Name: "chatgpt-imagegen", Count: 1}},
		MCP: []history.MCPUsage{
			{Server: "codex-cu", Count: 55, Tools: []history.UsageItem{{Name: "js", Count: 54}, {Name: "js_reset", Count: 1}}},
			{Server: "androidmirror", Count: 4, Tools: []history.UsageItem{{Name: "screen_observe", Count: 3}, {Name: "screen_act", Count: 1}}},
		},
		CLI: []history.CLIUsage{
			{Name: "go", Source: history.SourceHomebrew, Count: 12}, {Name: "git", Source: history.SourceSystem, Count: 9},
			{Name: "rg", Source: history.SourceHomebrew, Count: 6}, {Name: "bsk", Count: 4}, {Name: "adb", Count: 2},
			{Name: "fvm", Source: history.SourceHomebrew, Count: 1}, {Name: "xcrun", Source: history.SourceSystem, Count: 1},
		},
		Web:      []history.WebUsage{{Host: "github.com", Count: 3}, {Host: "posse.shaoyan.uk", Count: 2}, {Host: "docs.rs", Count: 1}},
		Searches: 1,
		Events:   previewUsageEvents(),
	}}
}

// previewTaskDetail 选中一个跑着的任务，详情里有一条备注和一个等回复的问题。
func previewTaskDetail(a *app) {
	a.selected, a.taskSel = taskBoardID, "k2"
	a.detail = &taskDetail{taskInfo: *a.task("k2"), Messages: []taskMessage{
		{ID: "m1", Kind: "note", Body: "钥匙串的 service 名用 com.example.skill。", CreatedAt: time.Now().Add(-20 * time.Minute)},
		{ID: "m2", Kind: "question", Body: "现在的单测用的是内存版钥匙串，要不要给它补一个“记住密码”的用例？还是只测登录流程？", CreatedAt: time.Now().Add(-2 * time.Minute)},
	}}
}

func savePNG(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("POSSE_PREVIEW_DIR")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
	return newPreviewTesterSize(a, dark, 1200, 760)
}

// newPreviewTesterSize 用来看窄窗口：侧栏开着时设置页、讨论页三栏放不下的样子。
func newPreviewTesterSize(a *app, dark bool, w, h int) *ui.Tester {
	tt := newTester(a.build, w, h)
	tt.SetScale(2)
	tt.SetDark(dark)
	tt.SetTitleBar(ui.TitleBar{Height: 40, Left: 70}) // TrafficLightPosition{16, 13} 时红绿灯占的位置
	// 背景色有 120ms 的过渡，等它走完再取图
	for range 3 {
		tt.Frame()
		pass(tt, 80*time.Millisecond)
	}
	tt.Frame()
	return tt
}

// setUnread 把这些会话记成本轮完成了还没看（常驻进程报来的）。
func setUnread(a *app, ids ...string) {
	for i := range a.workspaces {
		for j := range a.workspaces[i].Terminals {
			if slices.Contains(ids, a.workspaces[i].Terminals[j].ID) {
				a.workspaces[i].Terminals[j].Unread = true
			}
		}
	}
}

// TestPreview 把主要界面渲染成 PNG，供改界面时对照；设置 POSSE_PREVIEW_DIR 才运行。
func TestPreview(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		a := previewApp(t)
		setUnread(a, "t4")                                // 完成了还没看：已完成、铃铛
		a.daemon.MobileOnline = []string{"iPhone 12 Pro"} // 侧栏「手机」行尾的绿点
		savePNG(t, newPreviewTester(a, dark), "main-"+suffix+".png")

		// 设置里图标调成"大"：按钮、侧栏和标签页的图标一起变大
		a = previewApp(t)
		iconSize = 18
		savePNG(t, newPreviewTester(a, dark), "icons-large-"+suffix+".png")
		iconSize = defaultIconSize

		// 界面字号调到最小、最大，字重调重：各档跟着变，行不挤
		for _, ty := range []struct {
			name         string
			size         float32
			weight, bold int
		}{{"ui-small", minUIFontSize, 0, 0}, {"ui-large", maxUIFontSize, 0, 0}, {"ui-heavy", 0, 500, 700}} {
			setUIType(ty.size, ty.weight, ty.bold)
			savePNG(t, newPreviewTester(previewApp(t), dark), ty.name+"-"+suffix+".png")
		}
		setUIType(0, 0, 0)

		// 窗口缩到最小
		a = previewApp(t)
		tt := newPreviewTester(a, dark)
		tt.SetSize(minWindowWidth, minWindowHeight)
		tt.Frame()
		savePNG(t, tt, "min-size-"+suffix+".png")

		// 标签放不下、鼠标停在标签栏上：横向滚动条不压在标签上
		for i := range 4 {
			a.workspaces[0].Terminals = append(a.workspaces[0].Terminals, terminalInfo{ID: "tx" + strconv.Itoa(i), Agent: "claude", Title: "✳ 朵朵兔三规则首版及健康分规则图", Running: true})
		}
		a.indexWorkspaces()
		tt.Frame()
		tt.Move(440, 20) // 标签栏里 Codex 标签的位置
		tt.Frame()
		savePNG(t, tt, "tabs-overflow-hover-"+suffix+".png")

		// 标题比标签的最大宽度还长：标题截断加省略号
		a = previewApp(t)
		a.workspaces[0].Terminals[0].AgentState = state("idle")
		a.workspaces[0].Terminals[0].Name = "1009｜feat｜番茄女频长篇调研与连载写作，还要再长一点才超出标签"
		tt = newPreviewTester(a, dark)
		savePNG(t, tt, "tab-long-title-"+suffix+".png")
		// 悬停时右端浮出关闭按钮，左边一段渐隐进标题，不凹出暗块
		tt.Move(400, 20)
		tt.Frame()
		savePNG(t, tt, "tab-long-title-hover-"+suffix+".png")

		// 协调者阻塞在 posse task wait 上：侧栏是"等待任务"的波形，worker 排在它下面
		a = previewApp(t)
		a.workspaces[0].Terminals[0].AgentState = &agentState{Status: "working"}
		a.workspaces[0].Terminals[0].WaitingTasks = true
		savePNG(t, newPreviewTester(a, dark), "tasks-waiting-"+suffix+".png")

		// 派发方下面几个 worker、一层嵌套和一个派到别的工作区的：连接线照文件树画，中间 ├、最后 ╰；空闲（不画状态）的 worker 也一样连着
		a = previewApp(t)
		a.workspaces[0].Terminals[3].AgentState = state("idle")
		a.workspaces[0].Terminals = append(a.workspaces[0].Terminals,
			terminalInfo{ID: "t7", Agent: "claude", Name: "整理图片方案", Running: true, AgentState: state("idle")},
			terminalInfo{ID: "t8", Agent: "codex", Name: "压一遍动图帧", Running: true, AgentState: state("waiting")},
			terminalInfo{ID: "t10", Agent: "cursor", Name: "补单元测试", Running: true, AgentState: state("working")})
		a.tasks = append(a.tasks,
			taskInfo{ID: "k5", Title: "整理图片方案", Agent: "claude", WorkspaceID: "w1", Worker: "t7", Status: "running", Coordinator: "t1", CreatedAt: time.Now()},
			taskInfo{ID: "k6", Title: "压一遍动图帧", Agent: "codex", WorkspaceID: "w1", Worker: "t8", Status: "running", Coordinator: "t7", CreatedAt: time.Now()},
			taskInfo{ID: "k7", Title: "补单元测试", Agent: "cursor", WorkspaceID: "w1", Worker: "t10", Status: "running", Coordinator: "t7", CreatedAt: time.Now()})
		// 派到 worktree 的 k2 在派发方下面有一行影子，写着它在哪个工作区
		a.tasks[1].Coordinator = "t1"
		a.indexWorkspaces()
		a.indexTasks()
		savePNG(t, newPreviewTester(a, dark), "sidebar-worker-lines-"+suffix+".png")

		// 派到同一项目别的工作区的 worker 不画影子行，从派发方沿项目图标那一列直接连到它真正那一行；派到别的项目的照旧是影子行
		a = previewApp(t)
		a.workspaces = slices.Insert(a.workspaces, 1, workspace{ID: "w10", ProjectID: "p1", Name: "desk-password", Kind: "worktree", Project: "Apps",
			Label: "Apps/desk-password", Branch: "desk-password", Path: "/Users/x/.posse/worktrees/Apps/desk-password",
			Terminals: []terminalInfo{{ID: "t12", Agent: "claude", Name: "看桌面解锁", Running: true, AgentState: state("working")}}})
		a.tasks = append(a.tasks, taskInfo{ID: "k8", Title: "看桌面解锁", Agent: "claude", WorkspaceID: "w10", Worker: "t12", Status: "running", Coordinator: "t1", CreatedAt: time.Now()})
		a.tasks[1].Coordinator = "t1"
		a.indexWorkspaces()
		a.indexTasks()
		savePNG(t, newPreviewTester(a, dark), "sidebar-worker-link-"+suffix+".png")

		// 任务都做完了、派发方空闲没有状态图形：worker 的竖线顶端拐过来接到派发方的 agent 图标
		a = previewApp(t)
		a.workspaces[0].Terminals[0].AgentState = state("idle")
		a.workspaces[0].Terminals[3].AgentState = state("idle")
		a.workspaces[0].Terminals = append(a.workspaces[0].Terminals,
			terminalInfo{ID: "t7", Agent: "codex", Name: "整理图片方案", Running: true, AgentState: state("idle")})
		a.tasks[0].Status = "done"
		a.tasks = append(a.tasks, taskInfo{ID: "k5", Title: "整理图片方案", Agent: "codex", WorkspaceID: "w1", Worker: "t7", Status: "done", Coordinator: "t1", CreatedAt: time.Now()})
		a.indexWorkspaces()
		a.indexTasks()
		savePNG(t, newPreviewTester(a, dark), "sidebar-worker-head-"+suffix+".png")

		// 会话按钮收成右边缘的把手
		a = previewApp(t)
		a.state.SessionButtonsHidden = true
		savePNG(t, newPreviewTester(a, dark), "session-buttons-folded-"+suffix+".png")

		// 终端的查找栏：命中铺蜂蜜色淡底，当前那个深一点；Claude 终端里搜不到时提示去历史里搜
		a = previewApp(t)
		a.panes[0].find = termFind{open: true, query: "view"}
		savePNG(t, newPreviewTester(a, dark), "terminal-find-"+suffix+".png")
		a = previewApp(t)
		a.panes[0].find = termFind{open: true, query: "release"}
		savePNG(t, newPreviewTester(a, dark), "terminal-find-none-"+suffix+".png")

		// 两栏：左边 Claude 和 Codex，右边跑着开发服务器的 zsh，焦点在左栏
		a = previewApp(t)
		a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
		a.panes[1] = &termView{id: "t3", term: previewShell(t)}
		savePNG(t, newPreviewTester(a, dark), "terminal-split-"+suffix+".png")

		// 刚开的 Claude 还没画第一屏：终端区居中是蜂巢加载动画和"正在启动"
		a = previewApp(t)
		blank, err := terminal.New(terminal.Options{Conn: nopConn{make(chan struct{})}, Font: terminal.Font{Family: termFontFamily, Size: defaultFontSize}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { blank.Close() })
		a.panes[0].term.Close()
		a.panes[0] = &termView{id: "t1", term: blank, attached: time.Now().Add(-time.Second)}
		savePNG(t, newPreviewTester(a, dark), "terminal-starting-"+suffix+".png")

		// 侧栏收起；加宽的侧栏
		a = previewApp(t)
		a.state.SidebarHidden = true
		savePNG(t, newPreviewTester(a, dark), "sidebar-hidden-"+suffix+".png")
		a = previewApp(t)
		a.state.SidebarWidth = 340
		savePNG(t, newPreviewTester(a, dark), "sidebar-wide-"+suffix+".png")
		a = previewApp(t)
		a.state.SidebarWidth = 1 // 拉到最窄：顶上的磁贴是正方形
		savePNG(t, newPreviewTester(a, dark), "sidebar-narrow-"+suffix+".png")
		a = previewApp(t)
		a.state.ProjectMetaHidden, a.state.WorkspaceMetaHidden = true, true
		savePNG(t, newPreviewTester(a, dark), "sidebar-no-meta-"+suffix+".png")
		// 固定在顶上的工作区：主工作区用项目名，worktree 用工作区名，长名字截断
		a = previewApp(t)
		a.state.TopTiles = []string{"w1", taskBoardID, historyBoardID, "w6", skillsBoardID, mobileBoardID, "w3"}
		savePNG(t, newPreviewTester(a, dark), "sidebar-pinned-"+suffix+".png")
		// 从项目树把工作区拖到顶上：最后多一块强调色描边的空位
		a = previewApp(t)
		tt = newPreviewTester(a, dark)
		if r, ok := tt.Find("feat-login"); ok {
			h, _ := tt.Find("历史")
			tt.Press(r.X+r.W/2, r.Y+r.H/2)
			tt.Frame()
			for i := 1; i <= 5; i++ {
				f := float32(i) / 5
				tt.Move(r.X+r.W/2+(h.X-r.X)*f, r.Y+r.H/2+(h.Y+h.H/2-r.Y-r.H/2)*f)
				tt.Frame()
			}
			pass(tt, 400*time.Millisecond)
			tt.Frame()
			savePNG(t, tt, "sidebar-pin-drag-"+suffix+".png")
			tt.Release(h.X, h.Y)
		}
		// 拖顶上已有的磁贴：「手机」拖到「任务」那格，它原来的格子收拢，后面的往后让
		a = previewApp(t)
		tt = newPreviewTester(a, dark)
		if m, ok := tt.Find("手机"); ok {
			k, _ := tt.Find("任务")
			tt.Press(m.X+m.W/2, m.Y+m.H/2)
			tt.Frame()
			for i := 1; i <= 5; i++ {
				f := float32(i) / 5
				tt.Move(m.X+m.W/2+(k.X+k.W/2-m.X-m.W/2)*f, m.Y+m.H/2)
				tt.Frame()
			}
			tt.Advance(100 * time.Millisecond)
			tt.Frame()
			savePNG(t, tt, "sidebar-tile-move-mid-"+suffix+".png")
			pass(tt, 400*time.Millisecond)
			tt.Frame()
			savePNG(t, tt, "sidebar-tile-move-"+suffix+".png")
			tt.Release(k.X, k.Y)
		}
		// 侧栏「运行中」：所有项目的 agent 会话平铺，第二行写项目和工作区
		a = previewApp(t)
		a.state.SidebarGroup = sidebarByStatus
		a.workspaces[0].Terminals[1].AgentState = &agentState{Status: "idle"}
		setUnread(a, "t2")
		savePNG(t, newPreviewTester(a, dark), "sidebar-running-"+suffix+".png")

		// 项目自定义图标：emoji、一两个字、位图、SVG（多工作区的标题行）；再打开更换图标的对话框
		a = previewApp(t)
		a.home = t.TempDir()
		a.projIcons = map[string]projectIcon{
			"p1": {Text: "🚀"}, "p4": {Text: "后"}, "p6": {Text: "AB"},
			"p2": {Image: previewIconFile(t, a.home, "p2", ".png")}, "p5": {Image: previewIconFile(t, a.home, "p5", ".svg")},
		}
		tt = newPreviewTester(a, dark)
		savePNG(t, tt, "project-icons-"+suffix+".png")
		a.editProjectIcon(a.projects[1])
		for range 4 {
			tt.Frame()
			pass(tt, 80*time.Millisecond)
		}
		tt.Frame()
		savePNG(t, tt, "project-icon-dialog-"+suffix+".png")

		// 不在看的会话本轮完成：标签和侧栏是铃铛，收起的项目也汇总成铃铛
		a = previewApp(t)
		a.workspaces[0].Terminals[1].AgentState = &agentState{Status: "idle"}
		setUnread(a, "t2", "t4")
		*a.sectionsOpen["p3"] = false
		savePNG(t, newPreviewTester(a, dark), "unread-"+suffix+".png")

		// 窗口的 Posse.app 被换掉：侧栏底部提示重开，下面还有一条操作报错
		a = previewApp(t)
		a.staleApp = true
		a.err = "当前分支没有跟踪的远端分支，没法更新"
		savePNG(t, newPreviewTester(a, dark), "stale-app-"+suffix+".png")
		// 收起了重开提示：底栏齿轮旁留圆点
		a.staleHidden, a.err = true, ""
		savePNG(t, newPreviewTester(a, dark), "stale-app-hidden-"+suffix+".png")

		// Posse 自带的技能还没装：侧栏底部提示安装
		a = previewApp(t)
		a.posseSkills = posseSkillsView{read: true, st: skills.PosseState{Missing: skills.PosseNames()}}
		savePNG(t, newPreviewTester(a, dark), "posse-skills-"+suffix+".png")

		// 右侧"技能与工具"面板：刚读过一次，预览期间不会再去找会话文件
		a = previewApp(t)
		previewUsagePanel(a)
		savePNG(t, newPreviewTester(a, dark), "usage-"+suffix+".png")
		// 筛选成只看技能和 MCP：命令行程序的分组和时间线里的命令都不列
		a.state.UsageSkillsOnly = true
		savePNG(t, newPreviewTester(a, dark), "usage-skills-only-"+suffix+".png")
		// 筛选成只看网址：只有网址一组，时间线里是访问的网址和搜索
		a.state.UsageSkillsOnly, a.state.UsageWebOnly = false, true
		savePNG(t, newPreviewTester(a, dark), "usage-web-"+suffix+".png")

		// 右侧"命令速查"面板：本机版本比表新，带两个技能；刚读过一次，预览期间不会再去扫
		a = previewApp(t)
		a.state.RightPanel = panelCheat
		a.cheat = cheatView{term: "t1", agent: "claude", loaded: true, version: "9.9.9", poll: panelPoll{at: time.Now()},
			skills: []cheatSkill{{name: "computer-use", desc: "Operate native macOS desktop apps"}, {name: "review-local", desc: "本地代码审查"}}}
		// 四个页签：内置技能（默认）、技能、内置命令、快捷键
		for i, name := range []string{"builtin-skills", "skills", "commands", "keys"} {
			a.state.CheatTab = i
			savePNG(t, newPreviewTester(a, dark), "cheat-"+name+"-"+suffix+".png")
		}
		// 英文：搜索框占位放不放得下
		a.state.CheatTab = 0
		lt := newPreviewTester(a, dark)
		lt.SetLocale("en")
		lt.Frame()
		savePNG(t, lt, "cheat-builtin-skills-en-"+suffix+".png")
		// 搜索：不分页签，只列匹配的条目，没有命中的组不画
		a.cheat.query = "review"
		savePNG(t, newPreviewTester(a, dark), "cheat-search-"+suffix+".png")

		// 右侧图片面板：顶部分段切换生成的和粘贴的，刚读过一次
		a = previewApp(t)
		a.state.RightPanel = panelImages
		a.images = previewImages()
		savePNG(t, newPreviewTester(a, dark), "images-"+suffix+".png")

		// 悬停竖的长截图：大图按高缩小，整张在窗口里；右键弹出菜单
		a = previewApp(t)
		a.state.RightPanel = panelImages
		a.images = previewImages()
		a.images.big, a.images.bigKey = a.images.made[0].thumb, a.images.made[0].Key
		tt = newPreviewTester(a, dark)
		if r, ok := tt.Find("phone_screenshot"); ok {
			tt.Move(r.X+r.W/2, r.Y-60)
			settleMenu(tt)
			savePNG(t, tt, "images-hover-"+suffix+".png")
			tt.RightClickAt(r.X+r.W/2, r.Y-60)
			settleMenu(tt)
			savePNG(t, tt, "images-menu-"+suffix+".png")
		} else {
			t.Error("images: phone_screenshot not found")
		}

		// 文件栏的历史页：当前项目的会话已经查过一次
		a = previewApp(t)
		previewHistory(a, "")
		a.selected = "w1" // previewHistory 会切到历史页
		a.state.FilesPanels = map[string]bool{"p1": true}
		a.state.FilesPanelTab = tabHistory
		a.recent = recentView{key: "p1", loaded: true, poll: panelPoll{at: time.Now()}, sessions: a.history.sessions,
			previews: map[string]recentPreview{}}
		for i, s := range a.history.sessions {
			p := recentPreview{at: s.UpdatedAt, text: "迁移按三步走：先把终端和常驻进程换成 Go，再做多 agent 编排，最后补手机遥控。内存目标是窗口关掉后只剩 20 MB。"}
			if i == 1 {
				p = recentPreview{at: s.UpdatedAt, user: true, text: "清理一下 ~/Library/Caches 里超过 1 GB 的目录"}
			}
			a.recent.previews[sessionKey(s)] = p
		}
		savePNG(t, newPreviewTester(a, dark), "recent-"+suffix+".png")

		// 技能页：全局技能按来源分组，行尾四家的状态；右边是选中技能的详情。体检结果另一张
		a = previewApp(t)
		previewSkillsBoard(a, "")
		savePNG(t, newPreviewTester(a, dark), "skills-"+suffix+".png")
		lt = newPreviewTester(a, dark)
		lt.SetLocale("en")
		lt.Frame()
		savePNG(t, lt, "skills-en-"+suffix+".png")
		a = previewApp(t)
		previewSkillsBoard(a, homeDir+"/.agents/skills/lark-calendar")
		savePNG(t, newPreviewTester(a, dark), "skills-off-"+suffix+".png")
		// 读不到的一家写明原因，能建链接的给开关
		a = previewApp(t)
		previewSkillsBoard(a, homeDir+"/.agents/skills/hypit")
		savePNG(t, newPreviewTester(a, dark), "skills-unseen-"+suffix+".png")
		a = previewApp(t)
		previewSkillsBoard(a, skillIssuesKey)
		savePNG(t, newPreviewTester(a, dark), "skills-issues-"+suffix+".png")
		// 开关技能的确认框：改哪个文件、diff
		a = previewApp(t)
		previewSkillsBoard(a, homeDir+"/.agents/skills/lark-calendar")
		before := "{\n  \"model\": \"opus\",\n  \"statusLine\": {\n    \"type\": \"command\"\n  },\n  \"skillOverrides\": {\n    \"lark-calendar\": \"off\"\n  },\n  \"theme\": \"dark\"\n}\n"
		after := "{\n  \"model\": \"opus\",\n  \"statusLine\": {\n    \"type\": \"command\"\n  },\n  \"theme\": \"dark\"\n}\n"
		a.skillChange = skillChangeView{open: true, ch: skills.Change{Agent: skills.Claude, Skill: "lark-calendar", Enable: true, File: homeDir + "/.claude/settings.json", Before: before, After: after}}
		savePNG(t, newPreviewTester(a, dark), "skills-confirm-"+suffix+".png")
		// 确认框：打开时选中取消，→ 换到确认
		a = previewApp(t)
		a.confirm("把 ~/Desktop/demo 加入项目？", "文件夹留在原处，Posse 只把它列进侧栏。", "加入", func() {})
		ct := newPreviewTester(a, dark)
		savePNG(t, ct, "confirm-"+suffix+".png")
		ct.Key(0, ui.KeyRight)
		ct.Frame()
		ct.Frame()
		savePNG(t, ct, "confirm-ok-"+suffix+".png")
		// 删工作区时列出 agent 在工作区外留下的痕迹
		a = previewApp(t)
		at := time.Date(2026, 10, 30, 14, 20, 0, 0, time.Local)
		a.traces = tracesView{open: true, title: "删除工作区 posse/anim-image？", verb: "删除",
			body: "会关闭终端并删除 git worktree 目录（分支保留）。有未提交的改动时会拒绝删除。\n\nagent 在工作区外留下了下面这些，勾选的会一起移到废纸篓：",
			rows: []traceRow{
				{traceItem: traceItem{Path: homeDir + "/Downloads/ddt-ios-sim-test", Kind: "created", Agent: "claude", At: at, IsDir: true}, checked: true, size: 1700 << 20},
				{traceItem: traceItem{Path: homeDir + "/Desktop/Works/cl-snap-wt", Kind: "worktree", Agent: "codex", At: at.Add(-time.Hour), IsDir: true}, size: 1300 << 20},
				{traceItem: traceItem{Path: homeDir + "/Downloads/fs-2267.png", Kind: "created", Agent: "claude", At: at.Add(-2 * time.Hour), Running: true}, checked: true, size: 516 << 10},
				{traceItem: traceItem{Path: homeDir + "/.ssh/config", Kind: "config", Agent: "grok", At: at.Add(-3 * time.Hour)}, size: -1},
			}}
		savePNG(t, newPreviewTester(a, dark), "traces-"+suffix+".png")
		// 安装对话框：读出了来源里的技能，勾掉一个，下面是要跑的命令
		a = previewApp(t)
		previewSkillsBoard(a, "")
		a.skillInstall = skillInstallView{open: true, ws: "w1", global: true, claude: true, source: "anthropics/skills", listSrc: "anthropics/skills",
			listed: []skills.Listed{
				{Name: "docx", Description: "Create, read, edit, or manipulate Word documents.", Group: "Document Skills"},
				{Name: "pdf", Description: "Read, merge, split and fill PDF files.", Group: "Document Skills"},
				{Name: "frontend-design", Description: "Create distinctive, production-grade frontend interfaces with high design quality.", Group: "Design"},
			},
			picked: []bool{true, false, true}, installed: []bool{true, false, false}}
		savePNG(t, newPreviewTester(a, dark), "skills-install-"+suffix+".png")
		// 读不到列表：报错，退回手填名字
		a = previewApp(t)
		previewSkillsBoard(a, "")
		a.skillInstall = skillInstallView{open: true, ws: "w1", global: true, claude: true, source: "nobody/nope", listSrc: "nobody/nope",
			listErr: "Failed to clone repository：Authentication failed for https://github.com/nobody/nope.git."}
		savePNG(t, newPreviewTester(a, dark), "skills-install-failed-"+suffix+".png")
		// 更新技能：框里写明命令，点「运行」后在框里的终端跑
		a = previewApp(t)
		previewSkillsBoard(a, "")
		a.skillRun = skillRunView{open: true, title: "更新 archify？", about: "按锁文件里记的来源（tt-a1i/archify）拉最新的版本，覆盖本地的改动。", line: "DISABLE_TELEMETRY=1 npx -y skills@1.7.4 update archify -g -y", ws: "w1"}
		savePNG(t, newPreviewTester(a, dark), "skills-run-"+suffix+".png")
		// 没装 Node.js：提示去装
		a = previewApp(t)
		previewSkillsBoard(a, "")
		a.skillInstall = skillInstallView{open: true, ws: "w1", global: true, claude: true, source: "anthropics/skills", listSrc: "anthropics/skills",
			listErr: skills.ErrNoNPX.Error(), noNPX: true}
		savePNG(t, newPreviewTester(a, dark), "skills-install-nonpx-"+suffix+".png")
		// MCP 分页：四家的服务和状态，详情是各家从哪读到、每份配置
		a = previewApp(t)
		previewSkillsBoard(a, "")
		a.skills.page, a.skills.mcpSel = 1, "iphone-use"
		savePNG(t, newPreviewTester(a, dark), "skills-mcp-"+suffix+".png")
		// 文件栏的技能页签：当前工作区的项目级技能
		a = previewApp(t)
		previewSkillsTab(a)
		savePNG(t, newPreviewTester(a, dark), "skills-panel-"+suffix+".png")

		a = previewApp(t)
		a.selected = "w2"
		savePNG(t, newPreviewTester(a, dark), "empty-"+suffix+".png")

		// 窄窗口里开着右侧文件栏：启动格放不下一行，分两行
		a = previewApp(t)
		a.selected = "w2"
		a.state.FilesPanels = map[string]bool{"p2": true}
		tt = newPreviewTester(a, dark)
		tt.SetSize(1000, 760)
		for range 3 {
			tt.Frame()
		}
		savePNG(t, tt, "empty-narrow-"+suffix+".png")

		a = previewApp(t)
		a.projects, a.workspaces, a.selected = nil, nil, ""
		a.indexWorkspaces()
		savePNG(t, newPreviewTester(a, dark), "first-run-"+suffix+".png")

		a = previewApp(t)
		a.openNewWorkspace()
		a.newProject = "Skill"
		savePNG(t, newPreviewTester(a, dark), "new-workspace-"+suffix+".png")

		a = previewApp(t)
		previewTaskDetail(a)
		savePNG(t, newPreviewTester(a, dark), "tasks-"+suffix+".png")

		a = previewApp(t)
		a.selected, a.taskSel = taskBoardID, "k3"
		a.detail = &taskDetail{taskInfo: *a.task("k3")}
		savePNG(t, newPreviewTester(a, dark), "task-done-"+suffix+".png")

		a = previewApp(t)
		a.selected, a.tasks = taskBoardID, nil
		a.indexTasks()
		savePNG(t, newPreviewTester(a, dark), "tasks-empty-"+suffix+".png")

		// 讨论页：列表、消息、执行卡、输入区、右边一栏（含摘要），窄窗口、回答和搜索、带选项的问题、长消息、归档、对话框（preview_disc_test.go）
		renderDiscussionPreviews(t, dark, suffix)
		renderCollabPreviews(t, dark, suffix)

		a = previewApp(t)
		previewHistory(a, "")
		savePNG(t, newPreviewTester(a, dark), "history-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "")
		a.state.HistoryByFolder = true
		a.setHistoryGrouping()
		savePNG(t, newPreviewTester(a, dark), "history-by-folder-"+suffix+".png")

		// 窗口窄、侧栏宽：顶栏放不下时搜索框让出宽度，标题不折行
		a = previewApp(t)
		previewHistory(a, "")
		a.state.SidebarWidth = 240
		tt = newPreviewTester(a, dark)
		tt.SetSize(1000, 720)
		tt.Frame()
		savePNG(t, tt, "history-narrow-"+suffix+".png")

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
		// 调大编辑器字号，Markdown 跟着：标题、表格、列表、代码跟着放大，版心等比放宽
		a.editorSize = 17
		savePNG(t, newPreviewTester(a, dark), "history-markdown-large-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "")
		previewMermaid(a)
		savePNG(t, mermaidTester(t, a, dark), "history-mermaid-"+suffix+".png")

		a = previewApp(t)
		a.openDispatch()
		a.dWorkspace, a.dMode = "Skill/main", 1
		a.dAgents = map[string]bool{"claude": true, "codex": true}
		a.dTitle, a.dSpec = "给登录页加上记住密码", "在登录页加一个“记住密码”开关，密码存进系统钥匙串。"
		savePNG(t, newPreviewTester(a, dark), "dispatch-"+suffix+".png")

		// 嵌套分栏：左边 Claude，右边上下分成 zsh 和打开的文件
		// 打开文件时只有一栏、选中的是文件，终端会断开：文件在另一个 app 里开好再搬过来
		b := previewApp(t)
		v3 := previewFiles(t, b, "README.md", "main.go")
		a = previewApp(t)
		a.files, a.state.Files, a.workspaces[0].Path = b.files, b.state.Files, b.workspaces[0].Path
		// 左边 Claude，右边上面 zsh、下面打开的文件，焦点在 Claude
		a.state.Panes = map[string]layoutState{"w1": {Focus: "p1", Root: paneNode{Split: splitH, Kids: []paneNode{
			{ID: "p1", Tabs: []string{"t1", "t2"}, Active: "t1", Share: 1.2},
			{Split: splitV, Share: 1, Kids: []paneNode{
				{ID: "p2", Tabs: []string{"t3"}, Active: "t3"},
				{ID: "p3", Tabs: []string{docKey(v3.docs[0]), docKey(v3.docs[1])}},
			}},
		}}}}
		a.panes[1] = &termView{id: "t3", term: previewShell(t)}
		savePNG(t, newPreviewTester(a, dark), "columns-nested-"+suffix+".png")

		// 文件标签：树展开到打开的文件，README 有未保存的修改
		a = previewApp(t)
		v := previewFiles(t, a, "internal/app/view.go", "README.md", "main.go")
		v.doc(filepath.Join(v.root, "README.md")).ed.Insert("<!-- draft -->\n")
		savePNG(t, newPreviewTester(a, dark), "files-"+suffix+".png")
		// 按住 ⌘ 指到 loadConfig 上：词下面一条线，指针变手形，点一下跳到定义
		tt = newPreviewTester(a, dark)
		tt.MoveWith(ui.Cmd, 471, 255)
		tt.Frame()
		savePNG(t, tt, "files-cmd-hover-"+suffix+".png")

		// 编辑器的右键菜单：mygo 交来的菜单也是 Posse 自绘，英文换成中文
		a = previewApp(t)
		previewFiles(t, a, "internal/app/view.go", "README.md", "main.go")
		tt = newPreviewTester(a, dark)
		tt.RightClickAt(700, 260)
		settleMenu(tt)
		if r, ok := tt.Find("转换"); ok {
			tt.Move(r.X+r.W/2, r.Y+r.H/2)
		}
		settleMenu(tt)
		savePNG(t, tt, "menu-editor-"+suffix+".png")

		a = previewApp(t)
		v = previewFiles(t, a, "README.md")
		v.cur.preview = true
		savePNG(t, newPreviewTester(a, dark), "files-markdown-"+suffix+".png")
		// Markdown 预览的查找栏：当前命中的底色深一点
		v.cur.find = docFind{open: true, query: "posse", cur: 1, scroll: true}
		savePNG(t, newPreviewTester(a, dark), "files-markdown-find-"+suffix+".png")

		// 记忆、技能这类带 YAML 头部的文档：头部画成属性列表，行内代码是包住字的圆角底
		a = previewApp(t)
		v = previewFiles(t, a, "README.md")
		v.cur.preview = true
		v.cur.ed.Insert("---\nname: project-posse-status\ndescription: Posse 的背景、测试约束、真机测试分工、待办\nmetadata:\n  type: project\n  modified: 2026-10-30T16:10:39.494Z\n---\n\n" +
			"背景：用户嫌 Orca（Electron）内存高，用 Go + MyGo 重写为 Posse，降内存是首要目标。\n\n测试约束：\n" +
			"- 常驻进程能交接（升级不断终端），但重启会断开所有终端，必须用户明确同意。\n" +
			"- 凡是 `daemon stop`、`terminal close` 这类有破坏性的命令，必须在同一条命令里写 `POSSE_HOME=/tmp/...`，不靠后面的 export。\n" +
			"- e2e 测“用户”身份的 task 命令要 `env -u POSSE_TERMINAL_ID`（还有 `POSSE_WORKSPACE_ID`、`POSSE_SOCKET`、`POSSE_TASK_ID`）。\n\n" +
			"**Why:** 用户问进度或接着做时要能直接接上。\n**How to apply:** 界面偏好见 [[feedback-posse-ui-taste]]，打包见 [[feedback-repackage]]。\n\n1. 先做终端\n2. 再做编排\n   - worktree 隔离\n- 命令（都支持 `--json`）：\n  ```\n  posse web open <url>\n  posse web list\n  ```\n- 实现：注入一段 JS\n- console：页面载入时注入\n\n| 项目 | 数据 |\n|---|---|\n| 内存 | 21 MB |\n\n")
		savePNG(t, newPreviewTester(a, dark), "files-markdown-meta-"+suffix+".png")

		a = previewApp(t)
		v = previewFiles(t, a, "main.go")
		v.side, v.search.query.Text = sideFiles, "config"
		a.startSearch(v, 0)
		until(t, newTester(a.build, 1200, 760), "search", func() bool { return !v.search.running && len(v.search.hits) > 0 })
		savePNG(t, newPreviewTester(a, dark), "files-search-"+suffix+".png")
		// 限定在某个目录里检索：检索框下面一行是目录标签
		v.search.query.Dir = "internal"
		a.startSearch(v, 0)
		until(t, newTester(a.build, 1200, 760), "scoped", func() bool { return !v.search.running })
		savePNG(t, newPreviewTester(a, dark), "files-search-scoped-"+suffix+".png")

		a = previewApp(t)
		previewFiles(t, a, "main.go")
		a.openQuick(&a.workspaces[0], quickFiles)
		a.quick.query = "view"
		savePNG(t, newPreviewTester(a, dark), "quick-open-"+suffix+".png")
		// 随处搜索的"所有"页：文件、操作、文本各一组
		a.openQuick(&a.workspaces[0], quickAll)
		a.quick.query = "f"
		until(t, newTester(a.build, 1200, 760), "quick text", func() bool { return a.quick.text.key != "" && !a.quick.text.running })
		savePNG(t, newPreviewTester(a, dark), "quick-all-"+suffix+".png")
		// 文本页：命中的那一行在左，文件名和行号在右，底下是"在文件栏中查看全部"
		a.quick.tab, a.quick.query = quickText, "config"
		until(t, newTester(a.build, 1200, 760), "quick text page", func() bool { return a.quick.text.key != "" && !a.quick.text.running && len(a.quick.text.hits) > 0 })
		savePNG(t, newPreviewTester(a, dark), "quick-text-"+suffix+".png")
		// 符号页：名字、种类、文件名和行号
		a.quick.tab, a.quick.query = quickSymbols, "config"
		until(t, newTester(a.build, 1200, 760), "quick symbols", func() bool { return a.quick.sym.key != "" && !a.quick.sym.running && len(a.quick.sym.syms) > 0 })
		savePNG(t, newPreviewTester(a, dark), "quick-symbols-"+suffix+".png")

		// 跳到定义时几处同名的声明：选择列表
		a = previewApp(t)
		previewFiles(t, a, "main.go")
		a.defPick = defPick{open: true, ws: "w1", word: "openAlbum", defs: []files.Definition{
			{Path: "lib/MediaPickerHelper.java", Line: 41, Text: "    public static void openAlbum(Activity activity, PickOptions options, int requestCode) {", Span: [2]int{23, 32}, Kind: "method", Decl: true},
			{Path: "lib/legacy/MediaPickerHelper.java", Line: 17, Text: "    public static void openAlbum(Activity activity, int requestCode) {", Span: [2]int{23, 32}, Kind: "method", Decl: true},
			{Path: "app/ChatActivity.java", Line: 88, Text: "    private void openAlbum(int code) {", Span: [2]int{17, 26}, Kind: "method", Decl: true},
		}}
		savePNG(t, newPreviewTester(a, dark), "definition-pick-"+suffix+".png")

		// Git 各界面另出英文和伪语言（en-XA 把英文加长、加重音）各一张：看英文有没有截断、重叠
		gitLangs := func(a *app, name string) {
			for _, lang := range []string{"en", "en-XA"} {
				lt := newPreviewTester(a, dark)
				lt.SetLocale(lang)
				lt.Frame()
				savePNG(t, lt, name+"-"+lang+"-"+suffix+".png")
			}
		}

		// 左栏"改动"和 main.go 的 diff
		a = previewApp(t)
		previewGit(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-changes-"+suffix+".png")
		gitLangs(a, "git-changes")

		// worktree 的改动页：相对基准分支的提交数、会冲突的文件，同步和合并；储藏分页里别的分支存的标出分支
		a = previewApp(t)
		previewWorktree(t, a, 0)
		savePNG(t, newPreviewTester(a, dark), "git-worktree-"+suffix+".png")
		gitLangs(a, "git-worktree")
		a = previewApp(t)
		previewWorktree(t, a, 1)
		savePNG(t, newPreviewTester(a, dark), "git-worktree-stash-"+suffix+".png")
		gitLangs(a, "git-worktree-stash")

		// 工作区下面有几个仓库：改动页按仓库分组；分支弹窗最上面选仓库
		a = previewApp(t)
		previewMultiRepo(t, a, false)
		savePNG(t, newPreviewTester(a, dark), "git-multi-repo-"+suffix+".png")
		gitLangs(a, "git-multi-repo")
		a = previewApp(t)
		previewMultiRepo(t, a, true)
		savePNG(t, newPreviewTester(a, dark), "git-multi-repo-branches-"+suffix+".png")
		// 多仓库目录的项目新建工作区：勾仓库
		a = previewApp(t)
		previewMultiRepo(t, a, false)
		a.projects[0].Kind = "folder"
		a.openNewWorkspace()
		a.newName = "feat-login"
		savePNG(t, newPreviewTester(a, dark), "new-workspace-repos-"+suffix+".png")
		gitLangs(a, "new-workspace-repos")

		// 批量回滚对话框：按目录排的文件树
		a = previewApp(t)
		previewRollback(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-rollback-"+suffix+".png")
		gitLangs(a, "git-rollback")

		// 改动页按目录分组
		a = previewApp(t)
		previewChangesByDir(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-changes-dir-"+suffix+".png")
		gitLangs(a, "git-changes-dir")

		// 编辑器行号旁的改动卡片
		a = previewApp(t)
		previewChangePop(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-change-pop-"+suffix+".png")
		gitLangs(a, "git-change-pop")

		// 回滚对话框里点开文件：右边是 diff，可以逐处回滚
		a = previewApp(t)
		previewRollbackDetail(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-rollback-detail-"+suffix+".png")
		gitLangs(a, "git-rollback-detail")

		// 分支弹窗：在终端标签上点分支名打开
		a = previewApp(t)
		previewBranches(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-branches-"+suffix+".png")
		gitLangs(a, "git-branches")
		// 设置里把分支和 Git 日志放回标签栏：弹窗贴着右上角
		a = previewApp(t)
		a.state.GitInTabBar = true
		previewBranches(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-branches-tabbar-"+suffix+".png")

		a = previewApp(t)
		previewPush(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-push-"+suffix+".png")
		gitLangs(a, "git-push")

		// Push 对话框里点开文件：右边是 diff
		a = previewApp(t)
		previewPushDetail(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-push-detail-"+suffix+".png")
		gitLangs(a, "git-push-detail")

		a = previewApp(t)
		previewCompare(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-compare-"+suffix+".png")
		gitLangs(a, "git-compare")

		a = previewApp(t)
		previewImageDiff(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-image-diff-"+suffix+".png")
		gitLangs(a, "git-image-diff")

		a = previewApp(t)
		previewImageDoc(t, a, 0)
		savePNG(t, newPreviewTester(a, dark), "files-image-"+suffix+".png")

		a = previewApp(t)
		previewImageDoc(t, a, 2)
		savePNG(t, newPreviewTester(a, dark), "files-image-zoom-"+suffix+".png")

		// 原图超过 5000 万像素：不解码，写明尺寸
		a = previewApp(t)
		previewHugeImageDoc(t, a)
		savePNG(t, newPreviewTester(a, dark), "files-image-too-large-"+suffix+".png")

		a = previewApp(t)
		previewLog(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-log-"+suffix+".png")
		gitLangs(a, "git-log")

		// 一次合并八个子仓：通往子仓的长线两头各留一段、画箭头，中间不占列
		a = previewApp(t)
		previewLog(t, a)
		if ld := a.files["w1"].logCur.log; ld != nil {
			ld.commits, ld.commit, ld.more = octopusCommits(8, 40), nil, false
			ld.graph = buildGraph(ld.commits)
		}
		savePNG(t, newPreviewTester(a, dark), "git-log-octopus-"+suffix+".png")

		// 日志浮层的控制台：Posse 执行的 git 命令，失败的是红的
		a = previewApp(t)
		previewConsole(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-console-"+suffix+".png")
		gitLangs(a, "git-console")

		a = previewApp(t)
		url := "orca://pair?code=eyJ2IjoyLCJlbmRwb2ludCI6IndzOi8vMTcyLjMxLjAuMjMwOjY3ODgiLCJkZXZpY2VUb2tlbiI6IjAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwIn0"
		img, err := qrImage(url)
		if err != nil {
			t.Fatal(err)
		}
		a.state.MobileAddress = "https://orca.example.uk/v11v7w0"
		a.state.MobileAddresses = []string{a.state.MobileAddress}
		now := time.Now()
		a.selected = mobileBoardID
		a.mobile = mobileView{shown: true, loadedAt: now, url: url, endpoint: "wss://orca.example.uk/v11v7w0", qr: ui.NewBitmap(img),
			othersOpen: true,
			devices: []mobileDevice{
				{ID: "d1", Name: "iPhone 16 Pro", Phone: "p1", PairedAt: now.Add(-72 * time.Hour), LastSeen: now, Links: 2, Online: true},
				{ID: "d2", Name: "Pixel 8", Phone: "p2", PairedAt: now.Add(-50 * time.Hour), LastSeen: now.Add(-10 * time.Hour), Links: 1},
				{ID: "d3", Name: "手机 10-17 20:40", PairedAt: now.Add(-60 * time.Hour), LastSeen: now.Add(-60 * time.Hour), Links: 1},
				{ID: "d4", Name: "手机 10-17 21:50", PairedAt: now.Add(-59 * time.Hour), LastSeen: now.Add(-40 * time.Hour), Links: 1},
			}}
		// 有手机在看桌面、还没开辅助功能：手机页最上面的看桌面卡片
		a.daemon.DeskViewer, a.daemon.DeskMissing = "iPhone 16 Pro", []string{"accessibility"}
		savePNG(t, newPreviewTester(a, dark), "mobile-"+suffix+".png")
		a.daemon.DeskViewer, a.daemon.DeskMissing = "", nil

		// 设置页：通用分类；关于里常驻进程还在跑旧的包
		a.selected, a.settingsTab = settingsBoardID, ""
		savePNG(t, newPreviewTester(a, dark), "settings-"+suffix+".png")
		// Agent 分类里套餐用量整个关掉：底栏不显示用量，卡片只剩总开关
		a.settingsTab = "agent"
		usageOn := a.daemon.Usage
		a.daemon.Usage = &planStatus{Off: true}
		savePNG(t, newPreviewTester(a, dark), "settings-usage-off-"+suffix+".png")
		a.daemon.Usage = usageOn
		a.settingsTab = "about"
		a.daemon.Version, a.daemon.Build, a.daemon.Pid, a.daemon.StartedAt = "0.1.0", "663c0c7+ 2026-10-18T05:18:04Z", 53330, now.Add(-2*time.Hour)
		savePNG(t, newPreviewTester(a, dark), "settings-about-"+suffix+".png")
		// 个人资料：选了一张图还没保存（预览、裁剪提示），一个加入的讨论还没同步到
		a.profile = profileState{info: profileData{Name: "Mo", Pending: []struct{ Room, Title, Host, Reason string }{{Title: "登录页改版", Host: "Hana", Reason: "offline"}}},
			name: "Mo", loaded: true, at: now.Add(time.Hour), pick: "/tmp/a.png"}
		a.profile.preview = a.avatarImage(previewAvatar(a))
		a.settingsTab = "profile"
		savePNG(t, newPreviewTester(a, dark), "settings-profile-"+suffix+".png")
		// 窄窗口：卡片跟着变窄、不被分类栏盖住；选图失败的报错写在卡片里
		a.profile.err = locales.In("zh-Hans").SettingsProfileImageError()
		savePNG(t, newPreviewTesterSize(a, dark, 860, 760), "settings-profile-narrow-"+suffix+".png")
		a.profile.err = ""
		// 设置页的英文和伪语言（en-XA 把英文加长、加重音）：看排版有没有截断
		for _, lang := range []string{"en", "en-XA"} {
			for _, tab := range []string{"general", "profile", "appearance", "about", "agent", "keys"} {
				a.settingsTab = tab
				lt := newPreviewTester(a, dark)
				lt.SetLocale(lang)
				lt.Frame()
				savePNG(t, lt, "settings-"+tab+"-"+lang+"-"+suffix+".png")
			}
		}
		a.daemon.Pid = 0

		// 设置页的快捷键：改过一项（有"恢复默认"），正在录另一项
		a.settingsTab = "keys"
		a.state.Keys = map[string][]string{"toggleSidebar": {"Super+j"}}
		a.keyRec = &keyRecording{id: "newWorkspace", idx: 0}
		savePNG(t, newPreviewTester(a, dark), "settings-keys-"+suffix+".png")
		a.state.Keys, a.keyRec = nil, nil

		// 设置页的外观：点左边的分类进来，点开终端的字体列表
		systemFonts.once.Do(func() {})
		systemFonts.list, systemFonts.done = []ui.FontFamily{{Name: "Courier New", Monospaced: true}, {Name: "Helvetica"},
			{Name: "Menlo", Monospaced: true}, {Name: "Monaco", Monospaced: true}, {Name: "PingFang SC"}, {Name: "Times New Roman"}}, true
		a.settingsTab = ""
		ft := newPreviewTester(a, dark)
		if err := ft.Click("外观"); err != nil {
			t.Fatal(err)
		}
		pass(ft, 300*time.Millisecond)
		ft.Frame()
		if a.settingsTab != "appearance" {
			t.Fatalf("点了左边的外观，分类是 %q", a.settingsTab)
		}
		savePNG(t, ft, "settings-appearance-"+suffix+".png")
		// 主题和界面两张卡片在上面，字体卡片要往下滚才看得见
		ft.Scroll(900, 500, 0, 400)
		pass(ft, 300*time.Millisecond)
		ft.Frame()
		if err := ft.Click("终端字体"); err != nil {
			t.Fatal(err)
		}
		pass(ft, 300*time.Millisecond)
		ft.Frame()
		if !a.termPick.open {
			t.Fatal("点了字体按钮，列表没有打开")
		}
		savePNG(t, ft, "settings-fonts-"+suffix+".png")
		a.termPick = fontPick{}

		// 设置页的外观：编辑器换了字体和字号，滚到下面看预览跟着变
		a.settingsTab, a.editorFamily, a.editorSize = "appearance", "Menlo", 15
		et := newPreviewTester(a, dark)
		et.Scroll(800, 500, 0, 2000)
		pass(et, 300*time.Millisecond)
		et.Frame()
		savePNG(t, et, "settings-editor-"+suffix+".png")
		a.editorFamily, a.editorSize, a.settingsTab = "", defaultEditorFontSize, ""

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
		// 列表放不下、指针停在上面：滚动条在右边的留白上，不压着数字
		for i := range 4 {
			a.mem.rows = append(a.mem.rows, memWorkspace{id: fmt.Sprint("wx", i), label: fmt.Sprint("Project", i), kb: 40 << 10,
				terms: []memTerminal{{a.workspaces[2].Terminals[0], 40 << 10, nil}}})
		}
		tt = newPreviewTester(a, dark)
		tt.Move(960, 480)
		tt.Frame()
		savePNG(t, tt, "memory-scroll-"+suffix+".png")

		// 标签栏"+"的新建菜单，指针停在 Cursor 上（标签栏里也有 Codex，按快捷键提示找行）
		a = previewApp(t)
		tt = newPreviewTester(a, dark)
		if err := tt.Click("新建终端"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if r, ok := tt.Find("⌘T"); ok {
			tt.Move(r.X-60, r.Y+r.H/2-30)
		}
		for range 3 {
			tt.Frame()
			pass(tt, 80*time.Millisecond)
		}
		tt.Frame()
		savePNG(t, tt, "new-menu-"+suffix+".png")

		// 自绘菜单：标签的右键菜单（快捷键、禁用项）、防休眠档位（勾选）、提交的右键菜单展开子菜单
		a = previewApp(t)
		tt = newPreviewTester(a, dark)
		if err := tt.RightClick("zsh"); err != nil {
			t.Fatal(err)
		}
		settleMenu(tt)
		savePNG(t, tt, "menu-tab-"+suffix+".png")
		a = previewApp(t)
		tt = newPreviewTester(a, dark)
		if err := tt.Click("防休眠：运行"); err != nil {
			t.Fatal(err)
		}
		settleMenu(tt)
		savePNG(t, tt, "menu-check-"+suffix+".png")
		a = previewApp(t)
		previewLog(t, a)
		tt = newPreviewTester(a, dark)
		if err := tt.RightClick("Merge branch 'feat'"); err != nil {
			t.Fatal(err)
		}
		settleMenu(tt)
		if r, ok := tt.Find("把当前分支重置到这里"); ok {
			tt.Move(r.X+r.W/2, r.Y+r.H/2)
		}
		settleMenu(tt)
		savePNG(t, tt, "menu-submenu-"+suffix+".png")

		// 双击标签改名
		a = previewApp(t)
		if cur := a.currentTerminal(a.workspace(a.selected)); cur != nil {
			a.startRename(locales.In("zh-Hans"), *cur)
			a.renameText = "登录页重构"
		}
		savePNG(t, newPreviewTester(a, dark), "tab-rename-"+suffix+".png")

		// 状态栏的套餐用量详情
		a = previewApp(t)
		a.planOpen = true
		savePNG(t, newPreviewTester(a, dark), "plan-"+suffix+".png")
		// 切到剩余
		a = previewApp(t)
		a.planOpen = true
		a.state.UsageRemaining = true
		savePNG(t, newPreviewTester(a, dark), "plan-left-"+suffix+".png")
	}
}

// previewPlan 是四家都有读数的样子：Claude 的 5 小时快用完了，Cursor 带金额。
func previewPlan(now time.Time) *planStatus {
	at := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	return &planStatus{Usages: []usage.Usage{
		{Agent: "claude", UpdatedAt: at(-2 * time.Minute), Windows: []usage.Window{
			{Percent: 86, Minutes: 300, ResetsAt: at(95 * time.Minute)}, {Percent: 41, Minutes: 7 * 24 * 60, ResetsAt: at(73 * time.Hour)},
			{Percent: 12, Minutes: 7 * 24 * 60, ResetsAt: at(73 * time.Hour), Name: "Fable", At: at(-3 * time.Hour)}},
			Shares: &usage.Shares{Items: []usage.Share{{Name: "Claude Code", Percent: 92}, {Name: "Chats", Percent: 8}}, At: at(-3 * time.Hour)}},
		{Agent: "codex", Plan: "plus", UpdatedAt: at(-25 * time.Minute), Windows: []usage.Window{
			{Percent: 7, Minutes: 300, ResetsAt: at(4 * time.Hour)}, {Percent: 18, Minutes: 7 * 24 * 60, ResetsAt: at(120 * time.Hour)}},
			Resets: &usage.Resets{Count: 3, ExpiresAt: at(80 * time.Hour)}},
		{Agent: "grok", Plan: "SuperGrok Heavy", UpdatedAt: at(-3 * time.Hour), Windows: []usage.Window{
			{Percent: 53, Minutes: 7 * 24 * 60, ResetsAt: at(90 * time.Hour)}}},
		{Agent: "cursor", Plan: "Ultra $200/月", UpdatedAt: at(-40 * time.Minute), Windows: []usage.Window{
			{Percent: 10.3, Minutes: 31 * 24 * 60, ResetsAt: at(20 * 24 * time.Hour)},
			{Percent: 2.5, Minutes: 31 * 24 * 60, ResetsAt: at(20 * 24 * time.Hour), Name: "Auto"},
			{Percent: 18.2, Minutes: 31 * 24 * 60, ResetsAt: at(20 * 24 * time.Hour), Name: "API"}},
			Spend: &usage.Spend{Used: 4110, Limit: 40000}, OnDemand: &usage.Spend{Used: 250, Limit: 2000}},
	}}
}

// previewImages 是图片面板读过一次之后的样子，缩略图是合成的色块：一张手机截图、一张横的预览图、一张粘贴的。
func previewImages() imagesView {
	block := func(w, h int, top, bottom color.RGBA) *ui.Bitmap {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			k := float64(y) / float64(h)
			c := color.RGBA{uint8(float64(top.R)*(1-k) + float64(bottom.R)*k), uint8(float64(top.G)*(1-k) + float64(bottom.G)*k), uint8(float64(top.B)*(1-k) + float64(bottom.B)*k), 255}
			for x := range w {
				img.SetRGBA(x, y, c)
			}
		}
		return ui.NewBitmap(img)
	}
	now := time.Now()
	return imagesView{term: "t1", poll: panelPoll{at: now}, madeAll: 3,
		made: []imageItem{
			{Item: media.Item{Key: "s:3", Label: "phone_screenshot", At: now.Add(-2 * time.Minute), Tool: &history.ToolImage{Tool: "mcp__iphone-use__phone_screenshot"}}, thumb: block(236, 512, color.RGBA{40, 44, 52, 255}, color.RGBA{217, 119, 87, 255})},
			{Item: media.Item{Key: "s:2", Label: "git-compare-dark.png", At: now.Add(-9 * time.Minute), Tool: &history.ToolImage{Tool: "Read", Path: "/tmp/posse-preview/git-compare-dark.png"}}, thumb: block(512, 324, color.RGBA{38, 38, 36, 255}, color.RGBA{91, 141, 239, 255})},
		},
		pasted: []imageItem{
			{Item: media.Item{Key: "p:1", N: 1, Label: "#1", Path: "/tmp/1.png"}, thumb: block(512, 300, color.RGBA{243, 242, 239, 255}, color.RGBA{141, 187, 146, 255})},
		},
	}
}

// settleMenu 走几帧，等悬停和弹出的过渡走完再取图。
func settleMenu(tt *ui.Tester) {
	for range 3 {
		tt.Frame()
		pass(tt, 80*time.Millisecond)
	}
	tt.Frame()
}

// 点进另一栏的内容：焦点栏换过去；点标签栏的标签、空处按标签和不动焦点。
func TestColumnsClickAndDrag(t *testing.T) {
	a := previewApp(t)
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	a.panes[1] = &termView{id: "t3", term: previewShell(t)}
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	if a.pane != 0 {
		t.Fatalf("pane %d", a.pane)
	}
	// 右栏内容的中间
	tt.ClickAt(1000, 400)
	tt.Frame()
	if a.pane != 1 || a.activeTerm["w1"] != "t3" {
		t.Fatalf("click right: pane %d active %q", a.pane, a.activeTerm["w1"])
	}
	// 标签栏里点左栏选中的标签：焦点回左栏（这里没有 client，点没接入的标签会去接入）
	r, ok := tt.Find("待处理")
	if !ok {
		t.Fatal("no tab")
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if a.pane != 0 || a.activeTerm["w1"] != "t1" {
		t.Fatalf("click tab: pane %d active %q", a.pane, a.activeTerm["w1"])
	}
	// 点标签栏不算点进哪一栏的内容：焦点不变
	tt.ClickAt(1100, 20)
	tt.Frame()
	if a.pane != 0 {
		t.Fatalf("click strip moved focus to %d", a.pane)
	}
}

// 真拖一次：把右栏唯一的 zsh 标签拖到左栏内容的左边缘，松手后它成为最左一栏、原来的右栏收起，焦点在它那栏。
func TestColumnsDragTabToEdge(t *testing.T) {
	a := previewApp(t)
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	a.panes[1] = &termView{id: "t3", term: previewShell(t)}
	// 收起侧栏：左格要宽过两倍最窄宽度才拆得开
	a.state.SidebarHidden = true
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	r, ok := tt.Find("zsh")
	if !ok {
		t.Fatal("no zsh tab")
	}
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	for _, x := range []float32{r.X, 400, 200, 60} {
		tt.Move(x, 400)
		tt.Frame()
	}
	// 拖着时左栏的左半边盖上淡底
	if dir := os.Getenv("POSSE_PREVIEW_DIR"); dir != "" {
		savePNG(t, tt, "columns-drag-edge.png")
	}
	tt.Release(60, 400)
	tt.Frame()
	tt.Frame()
	if len(a.cols) != 2 || a.cols[0].tabs[0] != "t3" || a.pane != 0 || a.activeTerm["w1"] != "t3" {
		t.Fatalf("after drag: cols %+v pane %d active %q", a.cols, a.pane, a.activeTerm["w1"])
	}
}

// 两栏的离屏界面：左栏 Claude、Codex（选中 Claude），右栏 zsh。
func twoColumnTester(t *testing.T) (*app, *ui.Tester) {
	a := previewApp(t)
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	a.panes[1] = &termView{id: "t3", term: previewShell(t)}
	// 收起侧栏：侧栏里有同名的会话，按文字找的会是它
	a.state.SidebarHidden = true
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	return a, tt
}

func dragTo(tt *ui.Tester, from, to ui.Point) {
	tt.Press(from.X, from.Y)
	tt.Frame()
	for i := 1; i <= 4; i++ {
		f := float32(i) / 4
		tt.Move(from.X+(to.X-from.X)*f, from.Y+(to.Y-from.Y)*f)
		tt.Frame()
	}
	tt.Release(to.X, to.Y)
	tt.Frame()
	tt.Frame()
}

func center(t *testing.T, tt *ui.Tester, text string) ui.Point {
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("no %q", text)
	}
	return ui.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}
}

// 拖到另一栏标签后面的空处：移进那一栏；原来那栏空了就收起。
func TestColumnsDragToStripTail(t *testing.T) {
	a, tt := twoColumnTester(t)
	// 左栏放宽到四分之三，标签栏里"+"后面留出空处
	st := a.state.Panes["w1"]
	st.Root.Kids[0].Share, st.Root.Kids[1].Share = 3, 1
	a.state.Panes["w1"] = st
	tt.Frame()
	zsh := center(t, tt, "zsh")
	dragTo(tt, zsh, ui.Point{X: 800, Y: zsh.Y})
	if _, ok := a.state.Panes["w1"]; ok || len(a.cols) != 1 || !slices.Contains(a.cols[0].tabs, "t3") {
		t.Fatalf("after drop on tail: cols %+v state %+v", a.cols, a.state.Panes["w1"])
	}
}

// 同一栏里拖标签排序：Codex 拖到 Claude 前面。
func TestColumnsReorderInColumn(t *testing.T) {
	a, tt := twoColumnTester(t)
	dragTo(tt, center(t, tt, "Codex"), center(t, tt, "待处理"))
	if got := a.cols[0].tabs; len(got) < 2 || got[0] != "t2" || got[1] != "t1" || a.pane != 0 {
		t.Fatalf("order %v pane %d", got, a.pane)
	}
}

// 拖分栏线：拖动途中各栏不变，松手才改；拖过头停在最小宽度；双击平分。
func TestColumnsDividerDrag(t *testing.T) {
	a, tt := twoColumnTester(t)
	// 侧栏收起了，主区域从窗口左边开始，分栏线在左栏的右边
	left := float32(0)
	inner := 1200 - left - 1
	line := func() float32 { return left + a.tree.weight[0]*inner }
	gx := line()
	before := a.tree.weight[0]
	tt.Press(gx, 400)
	tt.Frame()
	tt.Move(gx-60, 400)
	tt.Frame()
	if a.tree.weight[0] != before || !strings.HasPrefix(a.resizingSplit, "split") {
		t.Fatalf("while dragging: weight %v → %v resizing %q", before, a.tree.weight[0], a.resizingSplit)
	}
	tt.Release(gx-60, 400)
	tt.Frame()
	tt.Frame()
	if a.tree.weight[0] >= before {
		t.Fatalf("after release: weight %v, was %v", a.tree.weight[0], before)
	}
	// 往左拖过头：停在最小宽度
	gx = line()
	tt.Press(gx, 400)
	tt.Frame()
	tt.Move(gx-800, 400)
	tt.Frame()
	tt.Release(gx-800, 400)
	tt.Frame()
	tt.Frame()
	if w := a.tree.weight[0] * inner; w < colMinWidth()-2 || w > colMinWidth()+2 {
		t.Fatalf("min width: %v, want %v", w, colMinWidth())
	}
	// 双击平分
	gx = line()
	tt.ClickAt(gx, 400)
	tt.ClickAt(gx, 400)
	tt.Frame()
	tt.Frame()
	if d := a.tree.weight[0] - a.tree.weight[1]; d > 0.01 || d < -0.01 {
		t.Fatalf("equalize: %v %v", a.tree.weight[0], a.tree.weight[1])
	}
}

// 一栏唯一的标签拖到紧挨着它那一侧的边上：不新开一栏（放下去还是原样、只改了宽度），当成移进那一栏。
func TestColumnsDragAloneToOwnSide(t *testing.T) {
	a, tt := twoColumnTester(t)
	inner := float32(1200 - 1)
	dragTo(tt, center(t, tt, "zsh"), ui.Point{X: a.tree.weight[0]*inner - 20, Y: 400})
	if _, ok := a.state.Panes["w1"]; ok || len(a.cols) != 1 || !slices.Contains(a.cols[0].tabs, "t3") {
		t.Fatalf("cols %+v state %+v", a.cols, a.state.Panes["w1"])
	}
}

// 真拖到下边缘：右格唯一的 zsh 拖到左格内容的下边，左格上下分成 Claude 和 zsh，原来的右格收起；拖着时下半边盖淡底。
func TestColumnsDragTabToBottom(t *testing.T) {
	a, tt := twoColumnTester(t)
	zsh := center(t, tt, "zsh")
	to := ui.Point{X: 300, Y: 720}
	tt.Press(zsh.X, zsh.Y)
	tt.Frame()
	for i := 1; i <= 4; i++ {
		f := float32(i) / 4
		tt.Move(zsh.X+(to.X-zsh.X)*f, zsh.Y+(to.Y-zsh.Y)*f)
		tt.Frame()
	}
	if os.Getenv("POSSE_PREVIEW_DIR") != "" {
		savePNG(t, tt, "columns-drag-bottom.png")
	}
	tt.Release(to.X, to.Y)
	tt.Frame()
	tt.Frame()
	st := a.state.Panes["w1"]
	if st.Root.Split != splitV || len(a.cols) != 2 || a.cols[1].tabs[0] != "t3" || a.pane != 1 {
		t.Fatalf("after drag: %+v cols %+v pane %d", st.Root, a.cols, a.pane)
	}
}

// 正在拖的分隔线没了（那一层收起、变了样）：拖的状态放掉，不留到下一条同名的线上。
func TestStaleGripDragReleased(t *testing.T) {
	a, tt := twoColumnTester(t)
	a.resizingSplit, a.splitDrag = "split/9:0", 50
	tt.Frame()
	tt.Frame()
	if a.resizingSplit != "" || a.splitDrag != 0 {
		t.Fatalf("resizing %q drag %v", a.resizingSplit, a.splitDrag)
	}
}

// TestPreviewSettingsI18n 单独展示卡片，避免长设置页下半部分没进入截图。
func TestPreviewSettingsI18n(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		for _, lang := range []string{"zh-Hans", "en", "en-XA"} {
			a := previewApp(t)
			views := []struct {
				name string
				view func(*ui.Context)
			}{
				{"chat-awake", func(c *ui.Context) { a.chatSettings(c); a.awakeSettings(c) }},
				{"usage-settings", a.planSettings},
				{"usage-panel", func(c *ui.Context) { a.state.UsageRemaining = false; a.planPanel(c, a.daemon.Usage.Usages) }},
				{"usage-panel-remaining", func(c *ui.Context) { a.state.UsageRemaining = true; a.planPanel(c, a.daemon.Usage.Usages) }},
				{"update-ready", func(c *ui.Context) {
					a.updateCard(c, "", updateView{phase: updateReady, version: "0.9.0", notes: "更新说明正文保持原样。"})
				}},
				{"update-progress", func(c *ui.Context) {
					a.updateCard(c, "", updateView{phase: updateDownloading, version: "0.9.0", percent: 42})
				}},
				{"keys-recording", func(c *ui.Context) { a.keyRec = &keyRecording{id: "newWorkspace", idx: 0}; a.keySettings(c) }},
			}
			for _, v := range views {
				tt := newTester(func(c *ui.Context) {
					c.SetTheme(posseTheme(c.Theme().Dark))
					ui.Scroll(c).Grow(1).Children(func() { ui.Column(c).Padding(24).Gap(16).Children(func() { v.view(c) }) })
				}, 760, 900)
				tt.SetDark(dark)
				tt.SetLocale(lang)
				tt.SetScale(2)
				tt.Frame()
				savePNG(t, tt, "i18n-"+v.name+"-"+lang+"-"+suffix+".png")
			}
			a.upd.phase, a.upd.version = updateReady, "0.9.0"
			tt := newPreviewTester(a, dark)
			tt.SetLocale(lang)
			tt.Frame()
			savePNG(t, tt, "i18n-update-bar-"+lang+"-"+suffix+".png")
		}
	}
}

// TestPreviewHour12 出 12 小时制的一套（中英文、浅色）：钟点多出 AM/PM、「下午」，看各处的时间列放不放得下。
func TestPreviewHour12(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	screens := []struct {
		name  string
		setup func(a *app)
	}{
		{"git-log", func(a *app) { previewLog(t, a) }},
		{"git-console", func(a *app) { previewConsole(t, a) }},
		{"git-compare", func(a *app) { previewCompare(t, a) }},
		{"tasks", previewTaskDetail},
		{"discussions", previewDiscussion},
		{"history", func(a *app) { previewHistory(a, ""); previewMarkdown(a) }},
		{"plan", func(a *app) { a.planOpen = true }},
		{"usage", previewUsagePanel},
	}
	for _, sc := range screens {
		for _, lang := range []string{"en", "zh-Hans"} {
			a := previewApp(t)
			sc.setup(a)
			tt := newPreviewTester(a, false)
			tt.SetLocale(lang)
			tt.SetRegion(i18n.Region{HourCycle: 12})
			for range 3 {
				tt.Frame()
			}
			savePNG(t, tt, "hour12-"+sc.name+"-"+lang+".png")
		}
	}
}

func TestPreviewProjectMenu(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		// 项目菜单混排图标和勾选项，检查行首与文字对齐。
		for _, checked := range []bool{true, false} {
			a := previewApp(t)
			a.projects[0].SingleRepo = !checked
			a.sysMenu.st = menuState{open: true, x: 320, y: 80}
			tt := newTester(func(c *ui.Context) {
				c.SetTheme(posseTheme(c.Theme().Dark))
				contextMenuAt(c, &a.sysMenu.st, func(m *popMenu) {
					a.workspaceMenu(c, m, a.workspaces[0])
				})
			}, 720, 640)
			tt.SetDark(dark)
			tt.SetScale(2)
			settleMenu(tt)
			savePNG(t, tt, fmt.Sprintf("menu-project-%t-%s.png", checked, suffix))
		}
	}
}
