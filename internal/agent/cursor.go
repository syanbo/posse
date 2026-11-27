package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Cursor 的 hooks 原本只能写进 ~/.cursor/hooks.json 或项目的 .cursor/hooks.json；
// 插件也能带 hooks，cursor-agent --plugin-dir 按会话加载插件，posse 就把 hooks 放进自己的插件。

// cursorEvents 是 posse 订阅的 Cursor 事件，都在交互模式下核实过会触发。hook 同步调用、每次拉起一次 posse，
// Cursor 又没有等待批准的事件，工具事件只用来给不经 beforeSubmitPrompt 开始的一轮记工作中：
// 每个工具做完时发一次的 postToolUse、postToolUseFailure 就够，preToolUse 和 afterShellExecution（shell 命令会多发它）不订阅。
var cursorEvents = []string{"sessionStart", "beforeSubmitPrompt", "postToolUse", "postToolUseFailure", "stop", "sessionEnd"}

// cursorPlugin 写出 posse 的 Cursor 插件，里面只有 hooks；内容没变时不重写。
func (l Launcher) cursorPlugin() (string, error) {
	dir := filepath.Join(l.Home, "cursor-plugin")
	manifest, err := json.MarshalIndent(map[string]string{
		"name":        "posse",
		"version":     "1.0.0",
		"description": "Posse: report agent state to Posse",
		"hooks":       "./hooks/hooks.json",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	type handler struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	hooks := map[string][]handler{}
	for _, ev := range cursorEvents {
		hooks[ev] = []handler{{Command: l.hookCommand("cursor", ev), Timeout: 5}}
	}
	hooksJSON, err := json.MarshalIndent(map[string]any{"version": 1, "hooks": hooks}, "", "  ")
	if err != nil {
		return "", err
	}
	files := map[string][]byte{
		filepath.Join(dir, ".cursor-plugin", "plugin.json"): manifest,
		filepath.Join(dir, "hooks", "hooks.json"):           hooksJSON,
	}
	for path, b := range files {
		if err := writeIfChanged(path, b); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func cursorStatus(event string) string {
	switch event {
	// preToolUse、afterShellExecution 只有交接前启动、按旧插件订阅的会话还会发
	case "beforeSubmitPrompt", "postToolUse", "postToolUseFailure", "preToolUse", "afterShellExecution":
		return StatusWorking
	// sessionStart 不改状态：旧版 Cursor 命令行带初始提示时第一轮不发 beforeSubmitPrompt，报空闲就错了（2026.10.22 会发）
	case "stop":
		return StatusIdle
	case "sessionEnd":
		return StatusExited
	}
	return ""
}

// cursorPromptLines 是 Cursor 底部输入区与状态栏的扫描范围。
const cursorPromptLines = 12

// cursorBusyHints 是 Cursor 仍在生成或等待交互决策时底部会出现的关键提示（小写匹配）。
var cursorBusyHints = []string{
	"ctrl+c to stop",
	"waiting for decision",
	"approve mode switch",
	"answer questions",
	"ready to proceed?",
	"y to confirm",
	"esc to cancel",
}

// CursorIdlePrompt 判断 Cursor 终端屏幕底部是否已稳定回到空闲输入框。
// cursor-agent 2026.11.22 的 stop hook 前置守卫漏判 pluginHooks，未配置用户/项目 hooks.json 时
// 不会发 stop；Posse 结合 task done 与输出静默，用这里的屏幕信号兜底判定空闲。
func CursorIdlePrompt(screen []string) bool {
	end := len(screen)
	for end > 0 && strings.TrimSpace(screen[end-1]) == "" {
		end--
	}
	if end == 0 {
		return false
	}
	start := max(end-cursorPromptLines, 0)
	hasPrompt := false
	for i := start; i < end; i++ {
		lower := strings.ToLower(screen[i])
		for _, hint := range cursorBusyHints {
			if strings.Contains(lower, hint) {
				return false
			}
		}
		if isCursorIdlePromptLine(screen[i]) {
			hasPrompt = true
		}
	}
	return hasPrompt
}

func isCursorIdlePromptLine(line string) bool {
	s := strings.TrimSpace(line)
	// 兼容带竖线边框的输入区写法（│ → Add a follow-up │）
	s = strings.TrimSpace(strings.Trim(s, "│"))
	var rest string
	switch {
	case strings.HasPrefix(s, "→"):
		rest = strings.TrimSpace(strings.TrimPrefix(s, "→"))
	case strings.HasPrefix(s, ">"):
		rest = strings.TrimSpace(strings.TrimPrefix(s, ">"))
	default:
		return false
	}
	return rest == "Add a follow-up" ||
		strings.HasPrefix(rest, "Add a follow-up — ") ||
		rest == "Plan, search, build anything"
}
