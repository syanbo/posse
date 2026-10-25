package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// 用户在 posse 终端的 shell 里自己敲 claude、codex、cursor-agent 时，也带上和界面启动一样的 hooks：
// $POSSE_HOME/bin 里放同名脚本，转给 `posse agent exec`，由它补上参数再换成真正的程序。
// 用户的 .zshrc 常把 ~/.local/bin 等目录插到 PATH 最前面，所以照 Ghostty 的 shell 集成，
// 用 ZDOTDIR 让 zsh 先读 posse 的 .zshenv，在每次出提示符前把 $POSSE_HOME/bin 挪回最前。
// agent 自己跑工具时再调这些命令，环境里有 ShimEnv，原样放过。

// ShimEnv 标记已经过 posse 的同名脚本启动：agent 的子进程再调同名命令时不再补 hooks，
// 免得子进程的状态记到这个终端上。
const ShimEnv = "POSSE_AGENT_SHIM"

// Shimmed 是会放同名脚本的命令和它对应的 agent。grok 的 hooks 只能写进 ~/.grok，不放。
var Shimmed = map[string]string{"claude": "claude", "codex": "codex", "cursor-agent": "cursor"}

const zshenv = `# Posse 的 zsh 集成，由常驻进程生成，改了会被覆盖。
# 先还原用户的 ZDOTDIR，照常读用户的启动文件；再在每次出提示符前把 $POSSE_HOME/bin 放回 PATH 最前，
# 手动敲的 claude、codex、cursor-agent 也带上 Posse 的 hooks。
if [[ -n "${POSSE_USER_ZDOTDIR-}" && "$POSSE_USER_ZDOTDIR" != "${POSSE_ZDOTDIR-}" ]]; then
  ZDOTDIR=$POSSE_USER_ZDOTDIR
else
  unset ZDOTDIR
fi
[[ -f "${ZDOTDIR:-$HOME}/.zshenv" ]] && source "${ZDOTDIR:-$HOME}/.zshenv"
if [[ -o interactive && -n "${POSSE_HOME-}" ]]; then
  _posse_path() { path=("$POSSE_HOME/bin" ${path:#$POSSE_HOME/bin}) }
  autoload -Uz add-zsh-hook && add-zsh-hook precmd _posse_path
fi
`

// ZshDir 是 posse 的 zsh 集成目录，终端的 ZDOTDIR 指向它。
func ZshDir(home string) string { return filepath.Join(home, "shell", "zsh") }

// ShimDir 是同名脚本所在的目录。
func ShimDir(home string) string { return filepath.Join(home, "bin") }

// WriteShellFiles 写出 zsh 集成和同名脚本，内容没变不重写。exe 是脚本要调用的 posse。
func WriteShellFiles(home, exe string) error {
	files := map[string]string{filepath.Join(ZshDir(home), ".zshenv"): zshenv}
	for cmd := range Shimmed {
		files[filepath.Join(ShimDir(home), cmd)] = "#!/bin/sh\nexec " + Quote(exe) + " agent exec " + cmd + " -- \"$@\"\n"
	}
	for path, content := range files {
		if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, []byte(content)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// ShellEnv 是终端要多带的环境：只有 zsh 走集成。userZdotdir 是用户原来的 ZDOTDIR。
func ShellEnv(home, shell, userZdotdir string) []string {
	if filepath.Base(shell) != "zsh" {
		return nil
	}
	dir := ZshDir(home)
	return []string{"ZDOTDIR=" + dir, "POSSE_ZDOTDIR=" + dir, "POSSE_USER_ZDOTDIR=" + userZdotdir}
}

// passthrough 是不补 hooks、原样运行的子命令和选项：非交互、管理类的用法。
var passthrough = map[string][]string{
	"claude": {"mcp", "config", "update", "doctor", "install", "setup-token", "migrate-installer", "plugin", "plugins",
		"-p", "--print", "-v", "--version", "-h", "--help"},
	"codex": {"exec", "e", "login", "logout", "mcp", "mcp-server", "app-server", "completion", "sandbox", "debug",
		"apply", "a", "cloud", "features", "help", "proto", "-V", "--version", "-h", "--help"},
	"cursor-agent": {"login", "logout", "status", "whoami", "update", "upgrade", "mcp", "ls", "help",
		"install-shell-integration", "uninstall-shell-integration", "-p", "--print", "-v", "--version", "-h", "--help"},
}

// ManualArgs 给用户手动敲的命令补上 hooks 参数，返回完整参数；不该补时原样返回。
// 不给 Claude 指定 --session-id：用户可能带了 --resume、--continue，会话 ID 由 hooks 报上来。
func (l Launcher) ManualArgs(cmd string, args []string) ([]string, error) {
	skip := passthrough[cmd]
	for _, a := range args {
		if strings.HasPrefix(a, "-") && slices.Contains(skip, a) {
			return args, nil
		}
	}
	// 子命令只看第一个不是选项的参数，别处的是提示词
	if i := firstPositional(args); i >= 0 && slices.Contains(skip, args[i]) {
		return args, nil
	}
	var extra []string
	switch cmd {
	case "claude":
		settings, plugin, err := l.claudeFiles()
		if err != nil {
			return nil, err
		}
		extra = []string{"--settings", settings, "--plugin-dir", plugin}
	case "codex":
		extra = l.codexArgs()
	case "cursor-agent":
		plugin, err := l.cursorPlugin()
		if err != nil {
			return nil, err
		}
		extra = []string{"--plugin-dir", plugin}
	default:
		return args, nil
	}
	extra = append(extra, l.manualPermissionArgs(Shimmed[cmd], args)...)
	return append(extra, args...), nil
}

func firstPositional(args []string) int {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			return i
		}
	}
	return -1
}

// LookPathAfter 在 PATH 里找 cmd，跳过 skip 目录（同名脚本自己所在的地方）。
func LookPathAfter(cmd, path, skip string) string {
	skip = filepath.Clean(skip)
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || filepath.Clean(dir) == skip {
			continue
		}
		p := filepath.Join(dir, cmd)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
