package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"posse/internal/agent"
	"posse/internal/daemon"
	"posse/internal/rpc"
	"posse/internal/spawn"
)

type projectInfo struct {
	ID, Name, Path, Kind, SetupScript string
	Workspaces                        int
}

type agentState struct {
	Status, Event, SessionID, Message string
}

type terminalInfo struct {
	ID, WorkspaceID, Title, Dir, Agent string
	Command                            []string
	AgentState                         *agentState
	Running                            bool
	Lost                               bool // 上一个常驻进程留下、还没恢复，第一次操作它时恢复
	ExitCode                           *int
	Pid, Cols, Rows                    int
	IdleMs                             int64
}

type workspaceInfo struct {
	ID, ProjectID, Name, Path, Kind, Branch, BaseRef, Project, Label string
	Terminals                                                        []terminalInfo
	Setup                                                            *struct {
		OK     bool
		Output string
	}
	Dirty *bool
}

// do 调用 RPC：--json 时原样输出结果，否则解到 v 再交给 human 打印。
func (e *env) do(method string, params, v any, human func()) error {
	var raw json.RawMessage
	if err := e.call(method, params, &raw); err != nil {
		return err
	}
	if v != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, v); err != nil {
			return err
		}
	}
	return e.emit(raw, human)
}

// doWait 是长等待版的 do：常驻进程交接给新进程（或重启）打断等待时，换到新进程上接着等，
// 超时按剩下的时间算。task.ask 打断时带回问题 ID，改成 resume，不再问一遍。
func (e *env) doWait(method string, params map[string]any, v any, human func()) error {
	start := time.Now()
	total, _ := params["timeoutMs"].(int64)
	var raw json.RawMessage
	for attempt := 1; ; attempt++ {
		err := e.call(method, params, &raw)
		if err == nil {
			break
		}
		if !rpc.Retryable(err) || attempt >= 10 {
			return err
		}
		var re *rpc.Error
		if errors.As(err, &re) && len(re.Data) > 0 {
			var data struct{ Resume string }
			if json.Unmarshal(re.Data, &data) == nil && data.Resume != "" {
				params["resume"] = data.Resume
			}
		}
		// 不知道问题有没有记下时不重发，免得重复提问
		if method == "task.ask" && params["resume"] == "" {
			return err
		}
		if total > 0 {
			left := total - time.Since(start).Milliseconds()
			if left <= 0 {
				return err
			}
			params["timeoutMs"] = left
		}
		time.Sleep(300 * time.Millisecond)
	}
	if v != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, v); err != nil {
			return err
		}
	}
	return e.emit(raw, human)
}

func (e *env) table() *tabwriter.Writer { return tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0) }

func termArg(pos []string) string {
	if len(pos) > 0 {
		return pos[0]
	}
	return os.Getenv("POSSE_TERMINAL_ID")
}

// --- daemon

func cmdDaemonRun(e *env, args []string) error {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	scrollback := fs.Int("scrollback-mb", 4, "scrollback kept per terminal, in MB")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-sigs
		log.Printf("received %v, shutting down", sig)
		cancel()
	}()
	err := daemon.Run(ctx, daemon.Config{Home: e.home, Scrollback: *scrollback << 20})
	log.Printf("daemon exited: %v", err)
	return err
}

func cmdDaemonStatus(e *env, args []string) error {
	if !e.daemonUp() {
		return e.emit(map[string]bool{"running": false}, func() { fmt.Fprintln(e.out, "daemon not running") })
	}
	var st struct {
		Version, Home      string
		Pid                int
		StartedAt          time.Time
		Terminals, Running int
		RSSKB, FootprintKB int64
	}
	return e.do("daemon.status", nil, &st, func() {
		fmt.Fprintf(e.out, "posse daemon %s, pid %d, up %s\nhome %s\nterminals %d (%d running), memory %.1f MB (rss %.1f MB)\n",
			st.Version, st.Pid, time.Since(st.StartedAt).Round(time.Second), st.Home, st.Terminals, st.Running,
			float64(st.FootprintKB)/1024, float64(st.RSSKB)/1024)
	})
}

func cmdDaemonStop(e *env, args []string) error {
	if !e.daemonUp() {
		return e.emit(map[string]bool{"running": false}, func() { fmt.Fprintln(e.out, "daemon not running") })
	}
	var st struct{ Pid int }
	if err := e.call("daemon.status", nil, &st); err != nil {
		return err
	}
	if err := e.call("daemon.stop", nil, nil); err != nil {
		return err
	}
	// 等进程真正退出：它在关闭终端期间 socket 已经消失
	for i := 0; i < 150 && syscall.Kill(st.Pid, 0) == nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return e.emit(map[string]bool{"stopped": true}, func() { fmt.Fprintln(e.out, "daemon stopped") })
}

// cmdDaemonHandoff 让正在运行的常驻进程把终端交给用这个 posse 启动的新进程。
func cmdDaemonHandoff(e *env, args []string) error {
	if _, err := parse(flag.NewFlagSet("daemon handoff", flag.ContinueOnError), args); err != nil {
		return err
	}
	if !e.daemonUp() {
		return e.emit(map[string]bool{"running": false}, func() { fmt.Fprintln(e.out, "daemon not running") })
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// 先准备好副本再交过去：旧版本的常驻进程不会自己做副本。准备不了就用原包，交接照常
	if rt, err := spawn.Runtime(exe, e.home); err == nil {
		exe = rt
	}
	var r struct{ Pid, Terminals int }
	return e.do("daemon.handoff", map[string]string{"exe": exe}, &r, func() {
		fmt.Fprintf(e.out, "handed %d terminals to the new daemon (pid %d)\n", r.Terminals, r.Pid)
	})
}

// --- projects

func cmdProjectAdd(e *env, args []string) error {
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	name := fs.String("name", "", "")
	setup := fs.String("setup", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "project add"); err != nil {
		return err
	}
	var p projectInfo
	return e.do("project.add", map[string]any{"path": absPath(pos[0]), "name": *name, "setupScript": *setup}, &p, func() {
		fmt.Fprintf(e.out, "added %s project %s (%s) at %s\n", p.Kind, p.Name, p.ID, p.Path)
	})
}

func cmdProjectList(e *env, args []string) error {
	var ps []projectInfo
	return e.do("project.list", nil, &ps, func() {
		w := e.table()
		fmt.Fprintln(w, "NAME\tKIND\tWORKSPACES\tPATH\tID")
		for _, p := range ps {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", p.Name, p.Kind, p.Workspaces, p.Path, p.ID)
		}
		w.Flush()
	})
}

func cmdProjectSet(e *env, args []string) error {
	fs := flag.NewFlagSet("project set", flag.ContinueOnError)
	name := fs.String("name", "", "")
	setup := fs.String("setup", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "project set"); err != nil {
		return err
	}
	params := map[string]any{"project": absPath(pos[0])}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			params["name"] = *name
		case "setup":
			params["setupScript"] = *setup
		}
	})
	var p projectInfo
	return e.do("project.set", params, &p, func() { fmt.Fprintf(e.out, "updated project %s\n", p.Name) })
}

func cmdProjectRm(e *env, args []string) error {
	fs := flag.NewFlagSet("project rm", flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "project rm"); err != nil {
		return err
	}
	return e.do("project.rm", map[string]any{"project": absPath(pos[0]), "force": *force}, nil, func() {
		fmt.Fprintf(e.out, "removed project %s (files kept)\n", pos[0])
	})
}

// --- workspaces

func cmdWorkspaceCreate(e *env, args []string) error {
	fs := flag.NewFlagSet("workspace create", flag.ContinueOnError)
	mode := fs.String("mode", "auto", "")
	branch := fs.String("branch", "", "")
	base := fs.String("base", "", "")
	path := fs.String("path", "", "")
	noSetup := fs.Bool("no-setup", false, "")
	agentName := fs.String("agent", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	prompt := fs.String("prompt", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "workspace create"); err != nil {
		return err
	}
	if *path != "" {
		if abs, err := filepath.Abs(*path); err == nil {
			*path = abs
		}
	}
	var w workspaceInfo
	params := map[string]any{"project": absPath(pos[0]), "name": pos[1], "mode": *mode, "branch": *branch, "base": *base, "path": *path,
		"noSetup": *noSetup, "agent": *agentName, "model": *model, "effort": *effort, "prompt": *prompt}
	return e.do("workspace.create", params, &w, func() {
		fmt.Fprintf(e.out, "created %s workspace %s (%s) at %s\n", w.Kind, w.Label, w.ID, w.Path)
		switch {
		case w.Branch != "" && w.BaseRef != "":
			fmt.Fprintf(e.out, "branch %s from %s\n", w.Branch, w.BaseRef)
		case w.Branch != "":
			fmt.Fprintf(e.out, "branch %s\n", w.Branch)
		}
		if w.Setup != nil {
			state := "ok"
			if !w.Setup.OK {
				state = "FAILED"
			}
			fmt.Fprintf(e.out, "setup %s\n%s\n", state, strings.TrimSpace(w.Setup.Output))
		}
		for _, t := range w.Terminals {
			fmt.Fprintf(e.out, "started %s in terminal %s\n", t.Agent, t.ID)
		}
	})
}

func printWorkspaces(w io.Writer, ws []workspaceInfo) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKSPACE\tKIND\tBRANCH\tTERMINALS\tPATH\tID")
	for _, x := range ws {
		branch := x.Branch
		if x.Dirty != nil && *x.Dirty {
			branch += "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Label, x.Kind, dash(branch), terminalSummary(x.Terminals), x.Path, x.ID)
	}
	tw.Flush()
}

func terminalSummary(ts []terminalInfo) string {
	if len(ts) == 0 {
		return "-"
	}
	var parts []string
	for _, t := range ts {
		parts = append(parts, t.ID+":"+terminalState(t))
	}
	return strings.Join(parts, " ")
}

func terminalState(t terminalInfo) string {
	if t.Lost {
		return "lost"
	}
	if !t.Running {
		if t.ExitCode != nil {
			return fmt.Sprintf("exited(%d)", *t.ExitCode)
		}
		return "exited"
	}
	if t.AgentState != nil && t.AgentState.Status != "" {
		return t.Agent + "/" + t.AgentState.Status
	}
	if t.Agent != "" {
		return t.Agent
	}
	return "running"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdWorkspaceList(e *env, args []string) error {
	fs := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	project := fs.String("project", "", "")
	dirty := fs.Bool("dirty", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var ws []workspaceInfo
	return e.do("workspace.list", map[string]any{"project": absPath(*project), "dirty": *dirty}, &ws, func() { printWorkspaces(e.out, ws) })
}

func cmdWorkspaceShow(e *env, args []string) error {
	fs := flag.NewFlagSet("workspace show", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "workspace show"); err != nil {
		return err
	}
	var w workspaceInfo
	return e.do("workspace.show", map[string]any{"workspace": pos[0]}, &w, func() { printWorkspaces(e.out, []workspaceInfo{w}) })
}

func cmdWorkspaceCurrent(e *env, args []string) error {
	cwd, _ := os.Getwd()
	var w workspaceInfo
	return e.do("workspace.current", map[string]any{"terminalId": os.Getenv("POSSE_TERMINAL_ID"), "cwd": cwd}, &w, func() {
		printWorkspaces(e.out, []workspaceInfo{w})
	})
}

func cmdWorkspaceRm(e *env, args []string) error {
	fs := flag.NewFlagSet("workspace rm", flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	delBranch := fs.Bool("delete-branch", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "workspace rm"); err != nil {
		return err
	}
	return e.do("workspace.rm", map[string]any{"workspace": pos[0], "force": *force, "deleteBranch": *delBranch}, nil, func() {
		fmt.Fprintf(e.out, "removed workspace %s\n", pos[0])
	})
}

// --- terminals

func cmdTerminalCreate(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal create", flag.ContinueOnError)
	agentName := fs.String("agent", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	prompt := fs.String("prompt", "", "")
	cols := fs.Int("cols", 0, "")
	rows := fs.Int("rows", 0, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "terminal create"); err != nil {
		return err
	}
	var t terminalInfo
	params := map[string]any{"workspace": pos[0], "command": pos[1:], "agent": *agentName, "model": *model, "effort": *effort, "prompt": *prompt, "cols": *cols, "rows": *rows}
	return e.do("terminal.create", params, &t, func() {
		fmt.Fprintf(e.out, "%s\n", t.ID)
	})
}

func cmdTerminalList(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal list", flag.ContinueOnError)
	ws := fs.String("workspace", "", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var ts []terminalInfo
	return e.do("terminal.list", map[string]any{"workspace": *ws}, &ts, func() {
		w := e.table()
		fmt.Fprintln(w, "ID\tSTATE\tIDLE\tSIZE\tTITLE\tDIR")
		for _, t := range ts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%dx%d\t%s\t%s\n", t.ID, terminalState(t),
				(time.Duration(t.IdleMs) * time.Millisecond).Round(time.Second), t.Cols, t.Rows, dash(t.Title), t.Dir)
		}
		w.Flush()
	})
}

func cmdTerminalShow(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal show", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var t struct {
		terminalInfo
		Workspace string
		Screen    []string
	}
	return e.do("terminal.show", map[string]any{"terminal": termArg(pos)}, &t, func() {
		fmt.Fprintf(e.out, "%s  %s  %s  %dx%d  pid %d\n", t.ID, t.Workspace, terminalState(t.terminalInfo), t.Cols, t.Rows, t.Pid)
		if t.Title != "" {
			fmt.Fprintf(e.out, "title: %s\n", t.Title)
		}
		if t.AgentState != nil && t.AgentState.Message != "" {
			fmt.Fprintf(e.out, "agent: %s\n", t.AgentState.Message)
		}
		fmt.Fprintln(e.out, strings.Repeat("─", max(min(t.Cols, 120), 10)))
		fmt.Fprintln(e.out, strings.Join(trimTrailingEmpty(t.Screen), "\n"))
	})
}

func trimTrailingEmpty(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func cmdTerminalRead(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal read", flag.ContinueOnError)
	lines := fs.Int("lines", 60, "")
	screen := fs.Bool("screen", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var r struct{ Lines []string }
	return e.do("terminal.read", map[string]any{"terminal": termArg(pos), "lines": *lines, "screen": *screen}, &r, func() {
		out := r.Lines
		if *screen {
			out = trimTrailingEmpty(out)
		}
		fmt.Fprintln(e.out, strings.Join(out, "\n"))
	})
}

func cmdTerminalSend(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal send", flag.ContinueOnError)
	text := fs.String("text", "", "")
	enter := fs.Bool("enter", false, "")
	var keys multiFlag
	fs.Var(&keys, "key", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *text == "" && !*enter && len(keys) == 0 {
		return fmt.Errorf("usage: posse terminal send <terminal> [--text T] [--enter] [--key K]...")
	}
	return e.do("terminal.send", map[string]any{"terminal": termArg(pos), "text": *text, "enter": *enter, "keys": []string(keys)}, nil, func() {})
}

func cmdTerminalWait(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal wait", flag.ContinueOnError)
	forWhat := fs.String("for", "", "")
	idle := fs.Duration("idle", 3*time.Second, "")
	text := fs.String("text", "", "")
	status := fs.String("status", "", "")
	timeout := fs.Duration("timeout", 10*time.Minute, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	params := map[string]any{"terminal": termArg(pos), "for": *forWhat, "idleMs": idle.Milliseconds(), "text": *text,
		"status": *status, "timeoutMs": timeout.Milliseconds()}
	var r struct{ Terminal terminalInfo }
	return e.doWait("terminal.wait", params, &r, func() {
		fmt.Fprintf(e.out, "%s: %s\n", r.Terminal.ID, terminalState(r.Terminal))
	})
}

func cmdTerminalResize(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal resize", flag.ContinueOnError)
	cols := fs.Int("cols", 0, "")
	rows := fs.Int("rows", 0, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var t terminalInfo
	return e.do("terminal.resize", map[string]any{"terminal": termArg(pos), "cols": *cols, "rows": *rows}, &t, func() {
		fmt.Fprintf(e.out, "%s: %dx%d\n", t.ID, t.Cols, t.Rows)
	})
}

func cmdTerminalClose(e *env, args []string) error {
	fs := flag.NewFlagSet("terminal close", flag.ContinueOnError)
	grace := fs.Duration("grace", 2*time.Second, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "terminal close"); err != nil {
		return err
	}
	return e.do("terminal.close", map[string]any{"terminal": pos[0], "graceMs": grace.Milliseconds()}, nil, func() {
		fmt.Fprintf(e.out, "closed %s\n", pos[0])
	})
}

// cmdHook 由 agent 的 hooks 调用：不能拖慢或打断 agent，所以从不拉起常驻进程，出错也返回 0。
func cmdHook(e *env, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: posse hook <agent> <event>")
	}
	id := os.Getenv("POSSE_TERMINAL_ID")
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	// Cursor 要从 stdout 读一个 JSON 回应，空对象表示不干预（不放行也不拦截）；
	// 其他 agent 不能有随意的输出，Claude 会把它注入模型上下文，只写它认的回应
	cursorReply := "{}"
	if args[0] == "cursor" {
		defer func() { fmt.Fprintln(e.out, cursorReply) }()
	}
	if id == "" {
		return nil
	}
	socket := os.Getenv("POSSE_SOCKET")
	if socket == "" {
		socket = e.socket
	}
	params := map[string]any{"terminalId": id, "agent": args[0], "event": args[1]}
	if json.Valid(payload) {
		params["payload"] = json.RawMessage(payload)
	}
	// Claude 的 PermissionRequest 可以在 stdout 带回决定。其余事件仍是 2 秒，并且不输出。
	if args[0] == "claude" && args[1] == "PermissionRequest" {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		var out struct {
			Behavior string `json:"behavior"`
		}
		err := rpc.Call(ctx, socket, "hook.permission.hold", params, &out)
		// 常驻进程交接时被打断：到新进程上再挂一次，总时间还是 35 秒
		for rpc.Retryable(err) && ctx.Err() == nil {
			time.Sleep(300 * time.Millisecond)
			err = rpc.Call(ctx, socket, "hook.permission.hold", params, &out)
		}
		cancel()
		if err == nil {
			if line := permissionDecision(out.Behavior); line != "" {
				fmt.Fprintln(e.out, line)
			}
			return nil
		}
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeUnknown {
			fmt.Fprintln(e.errOut, "posse hook:", err)
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out struct {
		SessionTitle string `json:"sessionTitle"`
		Deliver      string `json:"deliver"`
	}
	if err := rpc.Call(ctx, socket, "hook.event", params, &out); err != nil {
		fmt.Fprintln(e.errOut, "posse hook:", err)
	}
	// 常驻进程交来的任务提醒：工具做完时进上下文，本轮结束时让 agent 接着处理
	if out.Deliver != "" {
		if args[0] == "cursor" {
			cursorReply = agent.DeliveryOutput(args[0], args[1], out.Deliver)
		} else {
			fmt.Fprintln(e.out, agent.DeliveryOutput(args[0], args[1], out.Deliver))
		}
		return nil
	}
	// posse session title 给 Claude 起的标题：它只从 UserPromptSubmit 的回应里接受改名
	if args[0] == "claude" && args[1] == "UserPromptSubmit" && out.SessionTitle != "" {
		b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "UserPromptSubmit", "sessionTitle": out.SessionTitle,
		}})
		fmt.Fprintln(e.out, string(b))
	}
	return nil
}

// permissionDecision 是交回给 Claude Code 的决定。空字符串表示不干预，让 TUI 自己弹框。
func permissionDecision(behavior string) string {
	if behavior != "allow" && behavior != "deny" {
		return ""
	}
	b, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      map[string]string{"behavior": behavior},
		},
	})
	return string(b)
}

func cmdVersion(e *env, args []string) error {
	return e.emit(map[string]string{"version": daemon.Version}, func() { fmt.Fprintln(e.out, "posse", daemon.Version) })
}

// absPath 把看起来像路径的参数转成绝对路径；常驻进程的工作目录与调用方不同。
func absPath(p string) string {
	if p != "." && p != ".." && !strings.ContainsRune(p, filepath.Separator) {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
