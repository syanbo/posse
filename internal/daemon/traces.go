package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 痕迹账本：agent 在工作区外留下的文件和目录（下载目录里的测试包、项目旁边自己建的副本和 worktree、改过的配置），
// 删工作区时和项目的右键菜单里列出来清理。来源是 PostToolUse 的工具参数（写文件的路径、shell 命令里猜出的写入目标）
// 和终端里 git 的操作记录（worktree add、clone）。只记在工作区外、真的存在的路径；系统会清的临时目录、
// agent 自己的目录和各工具的共享缓存不记。

// traceSlack 是比较创建时间时留的余量：hook 收到时这一轮早已开始，文件系统时间和 time.Now 也不完全对齐
var traceSlack = 2 * time.Second

// traceScope 是给一个终端归类痕迹用的上下文。
type traceScope struct {
	home   string
	inside []string  // 不记的目录：工作区、项目目录、POSSE_HOME
	since  time.Time // 这一轮开始的时间：创建时间不早于它的才算这个会话新建的
}

// homeSkip 是主目录下不记的位置：agent 自己的目录、共享缓存（几个项目共用，删了只会拖慢下次构建）、废纸篓。
var homeSkip = []string{
	".claude", ".codex", ".cursor", ".grok", ".cache", ".npm", ".gradle", ".pub-cache", ".m2", ".cargo", ".rustup",
	".bun", ".dart-tool", ".dartServer", ".fvm", ".cocoapods", ".Trash", "go",
	"Library/Caches", "Library/Developer", "Library/Logs", "Library/Containers", "Library/Group Containers",
	"Library/Application Support/Code", "Library/Application Support/Cursor",
}

// userDirs 是主目录下由用户自己管的顶层目录，往上找新建目录时停在它们下面。
var userDirs = []string{"Desktop", "Downloads", "Documents", "Movies", "Music", "Pictures", "Public"}

// systemConfig 是主目录外会记（只记成改过的配置，不给删）的位置；别处（/Applications、外接盘……）不记。
var systemConfig = []string{"/etc", "/private/etc", "/usr/local", "/opt", "/Library"}

func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// classifyTrace 把一个写入的路径归成一条痕迹：返回要记的路径和类型；不记时 ok 为 false。
// 新建的往上找到这一轮里一起新建的最上层目录（~/Downloads/ddt-apk/a/b.apk 记成 ~/Downloads/ddt-apk），
// 但不越过用户目录、工作区的上级目录。
func classifyTrace(path string, sc traceScope) (string, string, bool) {
	if !filepath.IsAbs(path) {
		return "", "", false
	}
	p := filepath.Clean(path)
	// 只解析上级目录的符号链接（/tmp → /private/tmp），新建的符号链接本身照记
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		p = filepath.Join(dir, filepath.Base(p))
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", "", false
	}
	for _, x := range sc.inside {
		if under(p, x) {
			return "", "", false
		}
	}
	home := sc.home
	if !under(p, home) || p == home {
		// 系统会清的临时目录（/tmp、$TMPDIR 在 /private/var/folders）不记
		if under(p, "/private/tmp") || under(p, "/private/var") || under(p, "/dev") || under(p, "/System") {
			return "", "", false
		}
		for _, x := range systemConfig {
			if under(p, x) {
				return p, "config", true
			}
		}
		return "", "", false
	}
	rel := strings.TrimPrefix(p, home+"/")
	for _, x := range homeSkip {
		if under(rel, x) {
			return "", "", false
		}
	}
	if strings.HasPrefix(rel, ".") || under(rel, "Library") {
		return p, "config", true
	}
	if birthTime(fi).Before(sc.since.Add(-traceSlack)) {
		return p, "modified", true
	}
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == home || traceStop(parent, sc) {
			break
		}
		pfi, err := os.Stat(parent)
		if err != nil || birthTime(pfi).Before(sc.since.Add(-traceSlack)) {
			break
		}
		cur = parent
	}
	return cur, "created", true
}

// traceStop 报告往上找新建目录时不能越过 dir：它是用户目录，或者是工作区、项目的上级目录。
func traceStop(dir string, sc traceScope) bool {
	for _, u := range userDirs {
		if dir == filepath.Join(sc.home, u) {
			return true
		}
	}
	for _, x := range sc.inside {
		if under(x, dir) {
			return true
		}
	}
	return false
}

// expandHome 展开 ~ 和 $HOME；相对路径按 cwd 拼上。
func expandHome(p, home, cwd string) string {
	for _, pre := range []string{"~", "$HOME", "${HOME}"} {
		if p == pre {
			return home
		}
		if strings.HasPrefix(p, pre+"/") {
			return filepath.Join(home, p[len(pre)+1:])
		}
	}
	if strings.ContainsAny(p, "$`*?[") {
		return "" // 没展开的变量、通配符，猜不出
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return ""
		}
		p = filepath.Join(cwd, p)
	}
	return p
}

// traceScopeOf 取终端的归类上下文；终端或工作区已经不在时 ok 为 false。
func (d *Daemon) traceScopeOf(s *session) (traceScope, store.Workspace, bool) {
	w, err := d.store.Workspace(s.WorkspaceID)
	if err != nil {
		return traceScope{}, w, false
	}
	home, _ := os.UserHomeDir()
	sc := traceScope{home: resolvePath(home), since: s.turnStart()}
	add := func(p string) {
		if p != "" {
			sc.inside = append(sc.inside, resolvePath(p))
		}
	}
	add(w.Path)
	add(s.Dir)
	add(d.cfg.Home)
	if p, err := d.store.FindProject(w.ProjectID); err == nil {
		add(p.Path)
	}
	return sc, w, true
}

func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// noteToolWrites 从 PostToolUse 的参数里记下工作区外的写入。在后台做：要 stat、查库，不拖慢 hook。
func (d *Daemon) noteToolWrites(s *session, agentName, event string, payload json.RawMessage) {
	w := agent.ToolWrites(agentName, event, payload)
	if len(w.Files) == 0 && len(w.Commands) == 0 {
		return
	}
	go func() {
		sc, ws, ok := d.traceScopeOf(s)
		if !ok {
			return
		}
		cwd := w.Cwd
		if cwd == "" {
			cwd = s.Dir
		}
		home := sc.home
		for _, f := range w.Files {
			d.recordTrace(s, ws, sc, expandHome(f, home, cwd), "tool")
		}
		for _, c := range w.Commands {
			for _, t := range history.WriteTargets(c) {
				d.recordTrace(s, ws, sc, expandHome(t, home, cwd), "shell")
			}
		}
	}()
}

func (d *Daemon) recordTrace(s *session, w store.Workspace, sc traceScope, path, source string) {
	if path == "" {
		return
	}
	p, kind, ok := classifyTrace(path, sc)
	if !ok || d.store.TraceCovering(p) {
		return
	}
	t := store.Trace{Path: p, Kind: kind, ProjectID: w.ProjectID, WorkspaceID: w.ID, TerminalID: s.ID,
		Agent: s.agentName(), SessionID: s.Agent().SessionID, Source: source, At: time.Now()}
	if err := d.store.AddTrace(t); err != nil {
		log.Printf("trace %s: %v", p, err)
	}
}

// traceGit 记下终端里 git 建出来的 worktree 和 clone。Posse 自己管的 worktree（在 POSSE_HOME 里）和
// 登记成工作区的不记；工作区里面的也记（比如 .claude/worktrees），它们同样是 agent 自己建的副本。
func (d *Daemon) traceGit(r gitTraceRecord) {
	if r.Code != 0 || len(r.Args) < 2 {
		return
	}
	s := d.liveSession(r.Terminal)
	if s == nil || s.agentName() == "" {
		return
	}
	var target string
	isWorktree := false
	switch {
	case r.Args[0] == "worktree" && r.Args[1] == "add":
		target, isWorktree = firstPositional(r.Args[2:], gitWorktreeAddValueFlags), true
	case r.Args[0] == "clone":
		pos := positionals(r.Args[1:], gitCloneValueFlags)
		switch len(pos) {
		case 0:
			return
		case 1:
			target = strings.TrimSuffix(filepath.Base(strings.TrimRight(pos[0], "/")), ".git")
		default:
			target = pos[1]
		}
	default:
		return
	}
	if target == "" {
		return
	}
	sc, w, ok := d.traceScopeOf(s)
	if !ok {
		return
	}
	cands := []string{expandHome(target, sc.home, r.Dir), expandHome(target, sc.home, s.Dir)}
	for _, p := range cands {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		p = resolvePath(p)
		if !isWorktree {
			d.recordTrace(s, w, sc, p, "git")
			return
		}
		if under(p, resolvePath(d.cfg.Home)) || d.isWorkspacePath(p) {
			return
		}
		repo := ""
		if r.Dir != "" {
			repo = resolvePath(r.Dir)
		}
		t := store.Trace{Path: p, Kind: "worktree", Repo: repo, ProjectID: w.ProjectID, WorkspaceID: w.ID, TerminalID: s.ID,
			Agent: s.agentName(), SessionID: s.Agent().SessionID, Source: "git", At: time.Now()}
		if err := d.store.AddTrace(t); err != nil {
			log.Printf("trace %s: %v", p, err)
		}
		return
	}
}

func (d *Daemon) isWorkspacePath(p string) bool {
	ws, err := d.store.Workspaces("")
	if err != nil {
		return false
	}
	for _, w := range ws {
		if resolvePath(w.Path) == p {
			return true
		}
	}
	return false
}

var (
	gitWorktreeAddValueFlags = map[string]bool{"-b": true, "-B": true, "--reason": true}
	gitCloneValueFlags       = map[string]bool{"-b": true, "--branch": true, "-o": true, "--origin": true, "--depth": true,
		"-c": true, "--config": true, "--reference": true, "--separate-git-dir": true, "-u": true, "--upload-pack": true,
		"--template": true, "-j": true, "--jobs": true, "--filter": true, "--shallow-since": true, "--shallow-exclude": true}
)

func positionals(args []string, valueFlags map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(out, args[i+1:]...)
		}
		if strings.HasPrefix(a, "-") {
			if valueFlags[a] {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func firstPositional(args []string, valueFlags map[string]bool) string {
	if p := positionals(args, valueFlags); len(p) > 0 {
		return p[0]
	}
	return ""
}

// TraceItem 是给界面的一条痕迹：记录加上现在的状态。
type TraceItem struct {
	store.Trace
	Exists  bool   `json:"exists"`
	IsDir   bool   `json:"isDir,omitempty"`
	Running bool   `json:"running,omitempty"` // 留下它的终端还开着
	Label   string `json:"label,omitempty"`   // 终端的名字或会话标题
}

type traceListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

// traceList 列出痕迹；已经不在磁盘上的顺手从账本里去掉。
func (d *Daemon) traceList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p traceListParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	var wsID, projID string
	if p.Workspace != "" {
		w, err := d.workspace(p.Workspace)
		if err != nil {
			return nil, err
		}
		wsID = w.ID
	} else if p.Project != "" {
		pr, err := d.store.FindProject(p.Project)
		if err != nil {
			return nil, rpc.Errorf(rpc.CodeNotFound, "project %q not found", p.Project)
		}
		projID = pr.ID
	}
	recs, err := d.store.Traces(wsID, projID)
	if err != nil {
		return nil, err
	}
	items := []TraceItem{}
	var gone []string
	for _, r := range recs {
		fi, err := os.Lstat(r.Path)
		if err != nil {
			gone = append(gone, r.Path)
			continue
		}
		it := TraceItem{Trace: r, Exists: true, IsDir: fi.IsDir()}
		if s := d.liveSession(r.TerminalID); s != nil {
			it.Running = true
			it.Label = s.userName()
		}
		items = append(items, it)
	}
	if len(gone) > 0 {
		d.store.ForgetTraces(gone)
	}
	return items, nil
}

type traceForgetParams struct {
	Paths []string `json:"paths"`
}

func (d *Daemon) traceForget(ctx context.Context, raw json.RawMessage) (any, error) {
	var p traceForgetParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.store.ForgetTraces(p.Paths); err != nil {
		return nil, err
	}
	return map[string]int{"forgotten": len(p.Paths)}, nil
}
