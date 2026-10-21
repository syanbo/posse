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

	"hive/internal/gitx"
	"hive/internal/history"
	"hive/internal/media"
	"hive/internal/usage"
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
		daemon:       daemonStatus{FootprintKB: 17 << 10, Running: 4, Awake: &awakeStatus{Mode: "auto", Active: true, Working: 2}},
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
	a.daemon.Usage = previewPlan(now)
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
			"下一步：\n\n1. 先做终端和 `hive daemon`\n2. 再做编排\n   - worktree 隔离\n   - 任务汇报\n\n- [x] 调研 Orca\n- [ ] 手机遥控\n\n" +
			"> 换框架本身带来的收益有限。\n\n```go\nfunc main() {\n\tmygo.Run(app)\n}\n```"},
	}}
}

// previewFiles 在 w1 的文件标签里依次打开 rels（最后一个是当前文件），等它们读进来；之后不再轮询磁盘。
func previewFiles(t *testing.T, a *app, rels ...string) *filesView {
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.setProjectPanel(&a.state.FilesPanels, w.ProjectID, true)
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

// previewWorktree 把 w1 换成和基准分支有冲突的 worktree，打开改动页的 tab 分页。
func previewWorktree(t *testing.T, a *app, tab int) {
	worktreeWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	until(t, tt, "base", func() bool { return a.track["w1"] != nil && a.track["w1"].baseOK })
	v.git.tab = tab
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
	a.track["w1"].poll.at = time.Now().Add(time.Hour)
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
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	until(t, tt, "status", func() bool { return v.git.changeCount() == 7 })
	a.openRollback(v)
	a.rollback.folded["design"] = true
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

func previewChangePop(t *testing.T, a *app) {
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openFile(&a.workspaces[0], filepath.Join(root, "main.go"), 0, [2]int{})
	d := v.cur
	until(t, tt, "diff base", func() bool { return d.kind == docText && len(d.ed.Changes()) == 2 })
	d.ed.OnChangeClick(0, 0)
	v.poll.at, v.git.poll.at = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
}

func previewRollbackDetail(t *testing.T, a *app) {
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openRollback(v)
	e, _ := a.rollback.entry("main.go")
	a.rollbackShow(e, true)
	until(t, tt, "detail", func() bool { return a.rollback.detail.kind == docDiff && a.rollback.detail.diff.jumped })
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

func previewPushDetail(t *testing.T, a *app) {
	previewPush(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	a.pushShow(a.files["w1"], a.push, 0, true)
	until(t, tt, "push diff", func() bool { return a.push.detail.kind == docDiff })
}

// previewLog 把 feat 合并进 main，打开日志并选中合并提交。
func previewLog(t *testing.T, a *app) {
	root := branchWorkspace(t, a)
	gitRun(t, root, "merge", "-q", "--no-ff", "-m", "Merge branch 'feat'", "feat")
	gitRun(t, root, "tag", "v0.2.0")
	tt := ui.NewTester(a.build, 1200, 760)
	a.openLog(&a.workspaces[0], "", "")
	v := a.files["w1"]
	until(t, tt, "log", func() bool { return v.logCur.kind == docLog })
	a.selectCommit(v, &v.logCur.log.commit, v.logCur.log.commits[0])
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
	add("自动 Fetch", 1, "/Users/x/Skill", 0, gitx.Run{Args: []string{"fetch", "--all"}, Took: 1240 * time.Millisecond,
		Out: "From github.com:dengbo/skill\n   7117cfb..acc6bc5  main       -> origin/main"})
	add("提交", 2, dir, 40, gitx.Run{Args: []string{"add", "-A", "--", ":(literal)internal/app/view.go"}})
	add("提交", 2, dir, 40, gitx.Run{Args: []string{"commit", "-q", "--only", "-F", "-", "--", ":(literal)internal/app/view.go"}, Took: 310 * time.Millisecond})
	add("Push", 3, dir, 80, gitx.Run{Args: []string{"push", "--force-with-lease", "--force-if-includes", "origin", "main:main"}, Took: 2100 * time.Millisecond,
		Err: "To github.com:dengbo/hive.git\n ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs to 'github.com:dengbo/hive.git'"})
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
	add(13, history.UsageEvent{Server: "androidmirror", Tool: "screen_act"})
	add(14, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	add(15, history.UsageEvent{Server: "androidmirror", Tool: "screen_observe"})
	add(16, history.UsageEvent{CLI: "go", Source: history.SourceHomebrew, Command: "go vet ./... && go test ./internal/history"})
	add(17, history.UsageEvent{CLI: "bsk", Command: "bsk snapshot --session abcd"})
	add(18, history.UsageEvent{CLI: "git", Source: history.SourceSystem, Command: "git status --short"})
	return evs
}

func savePNG(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("HIVE_PREVIEW_DIR")
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
	tt := ui.NewTester(a.build, 1200, 760)
	tt.SetScale(2)
	tt.SetDark(dark)
	tt.SetTitleBar(ui.TitleBar{Height: 40, Left: 70}) // TrafficLightPosition{16, 13} 时红绿灯占的位置
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

		// 窗口缩到最小
		a = previewApp(t)
		tt := newPreviewTester(a, dark)
		tt.SetSize(minWindowWidth, minWindowHeight)
		tt.Frame()
		savePNG(t, tt, "min-size-"+suffix+".png")

		// 协调者阻塞在 hive task wait 上：侧栏是"等待任务"的波形，worker 排在它下面
		a = previewApp(t)
		a.workspaces[0].Terminals[0].AgentState = &agentState{Status: "working"}
		a.workspaces[0].Terminals[0].WaitingTasks = true
		savePNG(t, newPreviewTester(a, dark), "tasks-waiting-"+suffix+".png")

		// 会话按钮收成右边缘的把手
		a = previewApp(t)
		a.state.SessionButtonsHidden = true
		savePNG(t, newPreviewTester(a, dark), "session-buttons-folded-"+suffix+".png")

		// 侧栏收起；加宽的侧栏
		a = previewApp(t)
		a.state.SidebarHidden = true
		savePNG(t, newPreviewTester(a, dark), "sidebar-hidden-"+suffix+".png")
		a = previewApp(t)
		a.state.SidebarWidth = 340
		savePNG(t, newPreviewTester(a, dark), "sidebar-wide-"+suffix+".png")

		// 不在看的会话本轮完成：标签和侧栏是铃铛，收起的项目也汇总成铃铛
		a = previewApp(t)
		a.workspaces[0].Terminals[1].AgentState = &agentState{Status: "idle"}
		a.unread = map[string]bool{"t2": true, "t4": true}
		*a.sectionsOpen["p3"] = false
		savePNG(t, newPreviewTester(a, dark), "unread-"+suffix+".png")

		// 窗口的 Hive.app 被换掉：侧栏底部提示重开
		a = previewApp(t)
		a.staleApp = true
		savePNG(t, newPreviewTester(a, dark), "stale-app-"+suffix+".png")

		// 右侧"技能与工具"面板：刚读过一次，预览期间不会再去找会话文件
		a = previewApp(t)
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
			Events: previewUsageEvents(),
		}}
		savePNG(t, newPreviewTester(a, dark), "usage-"+suffix+".png")

		// 右侧图片面板：agent 生成的在上、粘贴的在下，刚读过一次
		a = previewApp(t)
		a.state.RightPanel = panelImages
		a.images = previewImages()
		savePNG(t, newPreviewTester(a, dark), "images-"+suffix+".png")

		// 文件栏的历史页：当前项目的会话已经查过一次
		a = previewApp(t)
		previewHistory(a, "")
		a.selected = "w1" // previewHistory 会切到历史页
		a.state.FilesPanels = map[string]bool{"p1": true}
		a.state.FilesPanelHistory = true
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

		// 编辑器的右键菜单：mygo 交来的菜单也是 Hive 自绘，英文换成中文
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

		// worktree 的改动页：相对基准分支的提交数、会冲突的文件，同步和合并；储藏分页里别的分支存的标出分支
		a = previewApp(t)
		previewWorktree(t, a, 0)
		savePNG(t, newPreviewTester(a, dark), "git-worktree-"+suffix+".png")
		a = previewApp(t)
		previewWorktree(t, a, 1)
		savePNG(t, newPreviewTester(a, dark), "git-worktree-stash-"+suffix+".png")

		// 批量回滚对话框：按目录排的文件树
		a = previewApp(t)
		previewRollback(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-rollback-"+suffix+".png")

		// 编辑器行号旁的改动卡片
		a = previewApp(t)
		previewChangePop(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-change-pop-"+suffix+".png")

		// 回滚对话框里点开文件：右边是 diff，可以逐处回滚
		a = previewApp(t)
		previewRollbackDetail(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-rollback-detail-"+suffix+".png")

		// 分支弹窗：在终端标签上点分支名打开
		a = previewApp(t)
		previewBranches(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-branches-"+suffix+".png")

		a = previewApp(t)
		previewPush(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-push-"+suffix+".png")

		// Push 对话框里点开文件：右边是 diff
		a = previewApp(t)
		previewPushDetail(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-push-detail-"+suffix+".png")

		a = previewApp(t)
		previewCompare(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-compare-"+suffix+".png")

		a = previewApp(t)
		previewImageDiff(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-image-diff-"+suffix+".png")

		a = previewApp(t)
		previewLog(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-log-"+suffix+".png")

		// 日志浮层的控制台：Hive 执行的 git 命令，失败的是红的
		a = previewApp(t)
		previewConsole(t, a)
		savePNG(t, newPreviewTester(a, dark), "git-console-"+suffix+".png")

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
		savePNG(t, newPreviewTester(a, dark), "mobile-"+suffix+".png")

		// 设置页：常驻进程还在跑旧的包
		a.selected = settingsBoardID
		a.daemon.Version, a.daemon.Build, a.daemon.Pid, a.daemon.StartedAt = "0.1.0", "663c0c7+ 2026-10-18T05:18:04Z", 53330, now.Add(-2*time.Hour)
		savePNG(t, newPreviewTester(a, dark), "settings-"+suffix+".png")
		a.daemon.Pid = 0

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
			time.Sleep(80 * time.Millisecond)
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
		if err := tt.Click("防休眠：自动"); err != nil {
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
			a.startRename(*cur)
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
			{Item: media.Item{Key: "s:2", Label: "git-compare-dark.png", At: now.Add(-9 * time.Minute), Tool: &history.ToolImage{Tool: "Read", Path: "/tmp/hive-preview/git-compare-dark.png"}}, thumb: block(512, 324, color.RGBA{38, 38, 36, 255}, color.RGBA{91, 141, 239, 255})},
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
		time.Sleep(80 * time.Millisecond)
	}
	tt.Frame()
}
