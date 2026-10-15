package agent

import (
	"encoding/json"
	"path/filepath"
)

// Cursor 的 hooks 原本只能写进 ~/.cursor/hooks.json 或项目的 .cursor/hooks.json；
// 插件也能带 hooks，cursor-agent --plugin-dir 按会话加载插件，hive 就把 hooks 和编排技能放进自己的插件。

// cursorEvents 是 hive 订阅的 Cursor 事件，都在交互模式下核实过会触发。
var cursorEvents = []string{
	"sessionStart", "beforeSubmitPrompt", "preToolUse", "postToolUse", "postToolUseFailure",
	"afterShellExecution", "stop", "sessionEnd",
}

// cursorPlugin 写出 hive 的 Cursor 插件：hooks 和编排技能；内容没变时不重写。
func (l Launcher) cursorPlugin() (string, error) {
	dir := filepath.Join(l.Home, "cursor-plugin")
	manifest, err := json.MarshalIndent(map[string]string{
		"name":        "hive",
		"version":     "1.0.0",
		"description": "Hive: report agent state to Hive and coordinate other agents with hive task",
		"hooks":       "./hooks/hooks.json",
		"skills":      "./skills/",
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
		filepath.Join(dir, "skills", SkillName, "SKILL.md"): []byte(SkillFrontmatter() + skillBody),
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
	case "beforeSubmitPrompt", "preToolUse", "postToolUse", "postToolUseFailure", "afterShellExecution":
		return StatusWorking
	// sessionStart 不改状态：命令行带初始提示时第一轮不发 beforeSubmitPrompt，报空闲就错了
	case "stop":
		return StatusIdle
	case "sessionEnd":
		return StatusExited
	}
	return ""
}
