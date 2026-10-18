// Package cli 是 hive 命令行：解析子命令，通过 RPC 调用常驻进程，按人类或 JSON 格式输出。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"hive/internal/app"
	"hive/internal/client"
	"hive/internal/daemon"
	"hive/internal/rpc"
)

type env struct {
	home   string
	socket string
	json   bool
	out    io.Writer
	errOut io.Writer
}

type command struct {
	name    string
	summary string
	args    string // 命令名之后的参数说明，帮助和参数不足的报错共用
	run     func(e *env, args []string) error
}

func (c command) usage() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
}

// commands 按帮助里的展示顺序排列。
var commands []command

func init() {
	commands = []command{
		{"app", "Open the Hive window", "", cmdApp},
		{"daemon run", "Run the daemon in the foreground", "", cmdDaemonRun},
		{"daemon status", "Show daemon status", "", cmdDaemonStatus},
		{"daemon stop", "Stop the daemon and every terminal it hosts", "", cmdDaemonStop},
		{"project add", "Add a folder or git repository", "<path> [--name N] [--setup SCRIPT]", cmdProjectAdd},
		{"project list", "List projects", "", cmdProjectList},
		{"project set", "Update a project", "<project> [--name N] [--setup SCRIPT]", cmdProjectSet},
		{"project rm", "Forget a project (files stay)", "<project> [--force]", cmdProjectRm},
		{"workspace create", "Create a workspace", "<project> <name> [--mode auto|folder|worktree] [--branch B] [--base REF] [--no-setup] [--agent claude|codex|grok|cursor] [--prompt P]", cmdWorkspaceCreate},
		{"workspace list", "List workspaces", "[--project P] [--dirty]", cmdWorkspaceList},
		{"workspace show", "Show a workspace", "<workspace>", cmdWorkspaceShow},
		{"workspace current", "Show the workspace of this terminal or directory", "", cmdWorkspaceCurrent},
		{"workspace rm", "Remove a workspace (and its git worktree)", "<workspace> [--force] [--delete-branch]", cmdWorkspaceRm},
		{"workspace diff", "Show what a worktree changed since its base", "<workspace>", cmdWorkspaceDiff},
		{"workspace merge", "Merge a worktree's branch into the project directory", "<workspace>", cmdWorkspaceMerge},
		{"terminal create", "Start a terminal", "<workspace> [--agent claude|codex|grok|cursor] [--prompt P] [--cols N --rows N] [-- command args...]", cmdTerminalCreate},
		{"terminal list", "List terminals", "[--workspace W]", cmdTerminalList},
		{"terminal show", "Show a terminal and its screen", "<terminal>", cmdTerminalShow},
		{"terminal read", "Read output", "<terminal> [--lines N] [--screen]", cmdTerminalRead},
		{"terminal send", "Send input", "<terminal> [--text T] [--enter] [--key K]...", cmdTerminalSend},
		{"terminal wait", "Wait", "<terminal> --for exit|idle|text|status [--idle 3s] [--text S] [--status idle,waiting] [--timeout 10m]", cmdTerminalWait},
		{"terminal resize", "Resize", "<terminal> --cols N --rows N", cmdTerminalResize},
		{"terminal close", "Close a terminal", "<terminal> [--grace 2s]", cmdTerminalClose},
		{"task guide", "How to coordinate worker agents with hive task (--skill for a SKILL.md)", "", cmdTaskGuide},
		{"task start", "Dispatch a task to a new worker", "--agent claude|codex|grok|cursor [--title T] (--spec S | --spec-file F|- | SPEC) [--workspace W] [--worktree NAME [--base REF]]", cmdTaskStart},
		{"task list", "List the tasks you dispatched", "[--all]", cmdTaskList},
		{"task show", "Show a task with its messages and the worker's screen", "<task>", cmdTaskShow},
		{"task wait", "Wait for a worker to finish, ask or exit", "[--timeout 10m]", cmdTaskWait},
		{"task inbox", "Read new messages from your workers without waiting", "", cmdTaskInbox},
		{"task reply", "Answer a worker's question", "<message> <answer>", cmdTaskReply},
		{"task send", "Send a worker an extra instruction", "<task> <message>", cmdTaskSend},
		{"task retry", "Start a failed, exited or lost task again in a new worker", "<task> [--spec S | --spec-file F|-]", cmdTaskRetry},
		{"task close", "Close a worker terminal (stops the task if still running)", "<task>", cmdTaskClose},
		{"task clear", "Forget finished tasks", "", cmdTaskClear},
		{"task ask", "Worker: ask the coordinator and wait for the answer", "<task> <question> [--resume MSG]", cmdTaskAsk},
		{"task check", "Worker: read new messages from the coordinator", "<task>", cmdTaskCheck},
		{"task done", "Worker: report the outcome once", "<task> --summary S [--report FILE] [--failed]", cmdTaskDone},
		{"history search", "Search past claude/codex/cursor/grok sessions", "<query> [--agent A] [--cwd DIR | --here] [--role user|assistant|tool] [--limit N]", cmdHistorySearch},
		{"history list", "List recent sessions", "[--agent A] [--cwd DIR | --here] [--limit N]", cmdHistoryList},
		{"history show", "Print a session's messages", "<session> [--tail N] [--tools]", cmdHistoryShow},
		{"history resume", "Continue a past session in a new hive terminal", "<session> [--workspace W]", cmdHistoryResume},
		{"history index", "Update the session history index now", "[--rebuild]", cmdHistoryIndex},
		{"mobile pair", "Show a QR code to pair Hive Mobile with this computer", "[--new] [--address HOST[:PORT]|URL]", cmdMobilePair},
		{"mobile devices", "List paired phones", "", cmdMobileDevices},
		{"mobile rm", "Unpair a phone", "<device>", cmdMobileRm},
		{"hook", "Report an agent hook event (used by agent hooks)", "<agent> <event> < payload.json", cmdHook},
		{"agent exec", "Run claude, codex or cursor-agent with Hive's hooks (used by the shims in $HIVE_HOME/bin)", "<command> -- [args...]", cmdAgentExec},
		{"version", "Print the version", "", cmdVersion},
	}
}

func Main(args []string) int {
	e := &env{out: os.Stdout, errOut: os.Stderr}
	e.home = os.Getenv("HIVE_HOME")
	if e.home == "" {
		home, _ := os.UserHomeDir()
		e.home = filepath.Join(home, ".hive")
	}
	e.socket = daemon.SocketPath(e.home)

	var rest []string
	for _, a := range args {
		if a == "--json" {
			e.json = true
			continue
		}
		rest = append(rest, a)
	}
	// 从 Hive.app 启动时没有参数（旧版 macOS 会带 -psn_ 参数），直接打开窗口
	if (len(rest) == 0 || strings.HasPrefix(rest[0], "-psn_")) && inAppBundle() {
		rest = []string{"app"}
	}
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		usage(e.out)
		return 0
	}
	cmd, cargs := lookup(rest)
	if cmd == nil {
		fmt.Fprintf(e.errOut, "hive: unknown command %q\n\n", strings.Join(rest, " "))
		usage(e.errOut)
		return 2
	}
	// agent 常先试 --help 看用法
	if wantsHelp(cargs) {
		fmt.Fprintf(e.out, "Usage: hive %s\n\n%s\n", cmd.usage(), cmd.summary)
		return 0
	}
	if err := cmd.run(e, cargs); err != nil {
		return e.fail(err)
	}
	return 0
}

// wantsHelp 报告参数里（"--" 之前）有没有 -h 或 --help。
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help":
			return true
		}
	}
	return false
}

func lookup(args []string) (*command, []string) {
	if len(args) >= 2 {
		for i := range commands {
			if commands[i].name == args[0]+" "+args[1] {
				return &commands[i], args[2:]
			}
		}
	}
	for i := range commands {
		if commands[i].name == args[0] {
			return &commands[i], args[1:]
		}
	}
	return nil, nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: hive <command> [options] [--json]")
	fmt.Fprintln(w)
	for _, c := range commands {
		if c.args == "" {
			fmt.Fprintf(w, "  %-18s %s\n", c.name, c.summary)
		} else {
			fmt.Fprintf(w, "  %-18s %s: %s\n", c.name, c.summary, c.usage())
		}
	}
	fmt.Fprintln(w, "\nEnvironment: HIVE_HOME (default ~/.hive). Inside hive terminals HIVE_TERMINAL_ID and HIVE_WORKSPACE_ID are set.")
	fmt.Fprintln(w, "Agents coordinating other agents: run `hive task guide` first.")
}

func (e *env) fail(err error) int {
	var re *rpc.Error
	if e.json {
		if !errors.As(err, &re) {
			re = &rpc.Error{Code: "error", Message: err.Error()}
		}
		b, _ := json.Marshal(map[string]any{"ok": false, "error": re})
		fmt.Fprintln(e.out, string(b))
		return 1
	}
	fmt.Fprintln(e.errOut, "hive:", err)
	if errors.As(err, &re) && re.Code == rpc.CodeTimeout {
		return 3
	}
	return 1
}

// call 发起 RPC，必要时先拉起常驻进程；Ctrl-C 会中断等待中的请求。
func (e *env) call(method string, params, out any) error {
	if err := e.ensureDaemon(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return rpc.Call(ctx, e.socket, method, params, out)
}

func (e *env) daemonUp() bool { return e.client().Up() }

func (e *env) ensureDaemon() error { return e.client().EnsureDaemon() }

func (e *env) client() *client.Client { return &client.Client{Home: e.home, Socket: e.socket} }

// emit 在 --json 时原样输出结果，否则调用 human 打印。
func (e *env) emit(v any, human func()) error {
	if e.json {
		b, err := json.Marshal(map[string]any{"ok": true, "result": v})
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, string(b))
		return nil
	}
	human()
	return nil
}

// parse 允许旗标和位置参数交错出现；"--" 之后的全部视为位置参数。
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		name, _, hasValue := strings.Cut(name, "=")
		f := fs.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("unknown flag %s", a)
		}
		flags = append(flags, a)
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); (ok && bf.IsBoolFlag()) || hasValue {
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag %s needs a value", a)
		}
		i++
		flags = append(flags, args[i])
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

// need 校验位置参数个数，不足时按命令表里的用法报错。
func need(pos []string, n int, name string) error {
	if len(pos) >= n {
		return nil
	}
	usage := name
	for _, c := range commands {
		if c.name == name {
			usage = c.usage()
			break
		}
	}
	return fmt.Errorf("usage: hive %s", usage)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func inAppBundle() bool {
	exe, err := os.Executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

func cmdApp(e *env, args []string) error { return app.Run(e.client()) }
