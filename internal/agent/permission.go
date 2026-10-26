package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// agent 的权限档位，照 Orca 的 Agent Permissions：默认跳过审批，启动时带上各家自己的跳过参数。
// 档位存在 $POSSE_HOME/agents.json，每次启动 agent 时读，改了之后新开的 agent 就生效，不用重启常驻进程。
const (
	PermissionSkip = "skip" // 跳过审批
	PermissionAsk  = "ask"  // 照各家默认逐项确认
)

// skipArgs 是各家跳过审批的参数，和 Orca 的 YOLO_TUI_AGENT_ARGS 一致。
var skipArgs = map[string][]string{
	"claude": {"--dangerously-skip-permissions"},
	"codex":  {"--dangerously-bypass-approvals-and-sandbox"},
	"grok":   {"--permission-mode", "bypassPermissions"},
	"cursor": {"--yolo"},
}

// permissionFlags 是用户手动启动时自己指定权限的参数，带了就不再补：
// Codex 的跳过参数和 -a、-s 同时给会直接报错退出。
var permissionFlags = map[string][]string{
	"claude": {"--dangerously-skip-permissions", "--permission-mode"},
	"codex":  {"--dangerously-bypass-approvals-and-sandbox", "--approve-for-me", "--yolo", "--full-auto", "-a", "--ask-for-approval", "-s", "--sandbox"},
	"cursor": {"--yolo", "-f", "--force"},
	"grok":   {"--permission-mode"},
}

func settingsPath(home string) string { return filepath.Join(home, "agents.json") }

type agentSettings struct {
	Permissions string `json:"permissions,omitempty"`
}

// PermissionMode 读权限档位，没设过或读不出来时是跳过审批。
func PermissionMode(home string) string {
	var s agentSettings
	if b, err := os.ReadFile(settingsPath(home)); err == nil && json.Unmarshal(b, &s) == nil && s.Permissions == PermissionAsk {
		return PermissionAsk
	}
	return PermissionSkip
}

// SetPermissionMode 写权限档位：先写临时文件再改名，正在启动的 agent 不会读到写了一半的文件。
func SetPermissionMode(home, mode string) error {
	if mode != PermissionAsk {
		mode = PermissionSkip
	}
	b, _ := json.Marshal(agentSettings{Permissions: mode})
	tmp := settingsPath(home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath(home))
}

// permissionArgs 是启动 name 时要加的权限参数。
func (l Launcher) permissionArgs(name string) []string {
	if PermissionMode(l.Home) != PermissionSkip {
		return nil
	}
	return skipArgs[name]
}

// manualPermissionArgs 同 permissionArgs，但用户自己带了权限参数时不加。
func (l Launcher) manualPermissionArgs(name string, args []string) []string {
	for _, a := range args {
		if a == "--" {
			break
		}
		flag, _, _ := strings.Cut(a, "=")
		for _, f := range permissionFlags[name] {
			if flag == f {
				return nil
			}
		}
	}
	return l.permissionArgs(name)
}
