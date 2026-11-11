package agent

import (
	"path/filepath"
	"regexp"
	"strings"

	"posse/internal/procmem"
)

// OfProcess 认出进程 pid 是哪个 agent，同时返回它的 argv（取会话 ID 用）。
func OfProcess(pid int) (string, []string) {
	exe, args := procmem.ExeArgs(pid)
	return fromProcess(exe, args), args
}

// fromProcess 按终端前台进程的 argv 认出是哪个 agent，认不出返回空；exe 是 exec 时给的路径，可以为空。
// 用户在 shell 里自己敲命令启动的 agent 只能这样认出来。
func fromProcess(exe string, args []string) string {
	switch procmem.Name(args) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "grok":
		return "grok"
	case "cursor-agent":
		return "cursor"
	case "agent":
		// Cursor 另装了一个叫 agent 的入口，exec 时保留了 argv[0]，靠后面 index.js 的路径认
		for _, a := range args[1:] {
			if strings.Contains(a, "cursor-agent") {
				return "cursor"
			}
		}
		// grok 也装了一个叫 agent 的入口（~/.grok/bin/agent），是指向 grok 的符号链接，argv 里没有别的线索
		return agentEntry(exe)
	}
	return ""
}

// agentEntry 按 agent 这个入口实际指向的程序认出是 grok 还是 Cursor，都不是时返回空。
func agentEntry(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	switch base := filepath.Base(path); {
	case strings.HasPrefix(base, "grok"):
		return "grok"
	case strings.Contains(path, "cursor-agent"):
		return "cursor"
	}
	return ""
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// sessionFlags 是各家在参数里指定会话的选项：新会话的 ID，或继续的会话。
var sessionFlags = map[string][]string{
	"claude": {"--session-id", "-r", "--resume"},
	"grok":   {"-s", "--session-id", "-r", "--resume"},
	"cursor": {"--resume"},
}

// SessionFromArgs 从 agent 进程的参数里取会话 ID：posse 的同名脚本补的 --session-id，或用户自己带的
// --resume <ID>（codex 是 resume <ID>）。没有 hooks 的 agent 只能这样知道会话，技能与工具面板靠它找会话文件。
// 只认 UUID：--resume 也可以跟标题，或者不跟值。
func SessionFromArgs(name string, args []string) string {
	if len(args) > 0 {
		args = args[1:]
	}
	for i, a := range args {
		if a == "--" {
			break
		}
		if name == "codex" {
			if a == "resume" && i+1 < len(args) && uuidRE.MatchString(args[i+1]) {
				return args[i+1]
			}
			continue
		}
		flag, value, inline := strings.Cut(a, "=")
		for _, f := range sessionFlags[name] {
			if flag != f {
				continue
			}
			if !inline && i+1 < len(args) {
				value = args[i+1]
			}
			if uuidRE.MatchString(value) {
				return value
			}
		}
	}
	return ""
}
