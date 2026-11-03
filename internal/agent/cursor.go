package agent

import (
	"encoding/json"
	"path/filepath"
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
