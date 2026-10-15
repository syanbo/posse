// Package agent 生成各 agent 的启动命令（在启动参数里注入 hooks，不改用户的全局配置），
// 并把 hooks 上报事件映射成状态。
package agent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Known 是支持一键启动的 agent。
var Known = []string{"claude", "codex", "grok"}

// Launcher 生成 agent 的启动命令；Exe 是 hooks 回调的 hive 可执行文件。
type Launcher struct {
	Exe  string
	Home string
}

// Launch 是一次 agent 启动：在交互式登录 shell 里运行的 argv，以及预先确定的会话 ID。
type Launch struct {
	Argv      []string
	SessionID string
}

func (l Launcher) Launch(name, prompt string) (Launch, error) {
	var parts []string
	var sessionID string
	switch name {
	case "claude":
		settings, plugin, err := l.claudeFiles()
		if err != nil {
			return Launch{}, err
		}
		sessionID = newUUID()
		parts = []string{"claude", "--settings", Quote(settings), "--plugin-dir", Quote(plugin), "--session-id", sessionID}
	case "codex":
		parts = []string{"codex"}
		for _, a := range l.codexArgs() {
			parts = append(parts, Quote(a))
		}
	case "grok":
		// grok 的 hooks 只能写进 ~/.grok，不改用户配置，状态靠终端标题判断
		sessionID = newUUID()
		parts = []string{"grok", "--session-id", sessionID, "--rules", Quote(guideHint)}
		if prompt != "" {
			parts = append(parts, "--verbatim")
		}
	default:
		return Launch{}, fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(Known, ", "))
	}
	if prompt != "" {
		parts = append(parts, Quote(prompt))
	}
	return Launch{Argv: ShellArgv(strings.Join(parts, " ")), SessionID: sessionID}, nil
}

// Resume 生成继续某个历史会话的启动命令，hooks 的注入方式与 Launch 相同。
// dir 是会话原来的目录：claude 按目录查找会话，必须回到原目录才能恢复。
// cursor 只能恢复不能新开：hive 不管理它的状态，只是方便从历史里接着聊。
func (l Launcher) Resume(name, sessionID, dir string) (Launch, error) {
	if sessionID == "" {
		return Launch{}, fmt.Errorf("session id required")
	}
	var parts []string
	switch name {
	case "claude":
		settings, plugin, err := l.claudeFiles()
		if err != nil {
			return Launch{}, err
		}
		// 恢复后沿用原会话 ID，hooks 上报的也是它
		parts = []string{"claude", "--settings", Quote(settings), "--plugin-dir", Quote(plugin), "--resume", Quote(sessionID)}
	case "codex":
		parts = []string{"codex"}
		for _, a := range l.codexArgs() {
			parts = append(parts, Quote(a))
		}
		parts = append(parts, "resume", Quote(sessionID))
	case "grok":
		parts = []string{"grok", "--resume", Quote(sessionID), "--rules", Quote(guideHint)}
	case "cursor":
		parts = []string{"cursor-agent", "--resume", Quote(sessionID)}
	default:
		return Launch{}, fmt.Errorf("cannot resume %q sessions", name)
	}
	cmd := strings.Join(parts, " ")
	if dir != "" {
		cmd = "cd " + Quote(dir) + " && " + cmd
	}
	return Launch{Argv: ShellArgv(cmd), SessionID: sessionID}, nil
}

// claudeFiles 写出 Claude 启动时要用的 hooks 设置和编排技能插件。
func (l Launcher) claudeFiles() (settings, plugin string, err error) {
	if settings, err = l.claudeSettings(); err != nil {
		return "", "", err
	}
	plugin, err = l.claudePlugin()
	return settings, plugin, err
}

func (l Launcher) hookCommand(agentName, event string) string {
	return Quote(l.Exe) + " hook " + agentName + " " + event
}

var claudeEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "PostToolUseFailure",
	"Notification", "Stop", "StopFailure", "SubagentStart", "SubagentStop", "PostCompact", "SessionEnd",
}

// claudeSettings 写出只含 hive hooks 的 settings 文件，经 --settings 与用户配置合并。
// 不设 async：异步 hook 可能乱序到达。
func (l Launcher) claudeSettings() (string, error) {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Matcher string    `json:"matcher,omitempty"`
		Hooks   []handler `json:"hooks"`
	}
	hooks := map[string][]group{}
	for _, ev := range claudeEvents {
		g := group{Hooks: []handler{{Type: "command", Command: l.hookCommand("claude", ev), Timeout: 5}}}
		switch ev {
		case "PreToolUse", "PermissionRequest", "PostToolUse", "PostToolUseFailure":
			g.Matcher = "*"
		}
		hooks[ev] = []group{g}
	}
	b, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(l.Home, "claude-hooks.json")
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return path, nil
	}
	return path, os.WriteFile(path, b, 0o600)
}

var codexEvents = []struct {
	name    string
	key     string
	timeout int
}{
	{"SessionStart", "session_start", 5},
	{"UserPromptSubmit", "user_prompt_submit", 5},
	{"PreToolUse", "pre_tool_use", 5},
	{"PermissionRequest", "permission_request", 5},
	{"PostToolUse", "post_tool_use", 5},
	{"Stop", "stop", 5},
	// Codex 把这两个事件的超时限制在 1–3 秒
	{"Interrupt", "interrupt", 3},
	{"SessionEnd", "session_end", 3},
}

// codexArgs 用 -c 注入 hooks，并附上预先算好的信任 hash；
// 未被信任的 hook 会让 Codex 启动时弹出全屏审核。
func (l Launcher) codexArgs() []string {
	var args, state []string
	for _, ev := range codexEvents {
		cmd := l.hookCommand("codex", ev.name)
		cmdJSON, _ := json.Marshal(cmd)
		args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`, ev.name, cmdJSON, ev.timeout))
		key, _ := json.Marshal("/<session-flags>/config.toml:" + ev.key + ":0:0")
		state = append(state, fmt.Sprintf(`%s={trusted_hash="%s"}`, key, codexHookHash(ev.key, cmd, ev.timeout)))
	}
	args = append(args, "-c", "hooks.state={"+strings.Join(state, ",")+"}")
	return append(args, codexGuideArgs()...)
}

// codexHookHash 复刻 Codex 对 hook 定义的指纹：键排序的紧凑 JSON 取 sha256。
// 与 Rust serde_json 一致，不转义 HTML 字符和非 ASCII 字符。
func codexHookHash(eventKey, command string, timeout int) string {
	ident := map[string]any{
		"event_name": eventKey,
		"hooks":      []any{map[string]any{"type": "command", "command": command, "timeout": timeout, "async": false}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(ident)
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ShellArgv 在交互式登录 shell 里执行 cmd（加载用户的 rc，如 nvm），结束后换成新的交互式 shell。
func ShellArgv(cmd string) []string {
	sh := UserShell()
	if cmd == "" {
		return []string{sh, "-l"}
	}
	return []string{sh, "-l", "-i", "-c", cmd + "; exec " + Quote(sh) + " -l"}
}

func UserShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/zsh"
}

// Quote 用单引号转义，适用于 sh/bash/zsh。
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
