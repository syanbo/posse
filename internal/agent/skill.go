package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// 编排技能：posse 启动的 agent 要派发、并行、监督其他 agent 时，先读 `posse task guide`。
// 正文由 posse 程序输出，和程序版本一致；这里只按会话注入一个入口，不写用户的全局配置：
// Claude、Cursor 用 --plugin-dir 加载 posse 生成的插件，Codex 和 Grok 没有按会话加载技能的办法，
// 把一句指路放进附加指令（Codex 的 developer_instructions、Grok 的 --rules）。

const (
	SkillName        = "posse-tasks"
	SkillDescription = "Dispatch work to other agents (claude, codex, grok, cursor) in Posse terminals and coordinate them with `posse task`: start workers with a chosen model and reasoning effort, wait for results, answer their questions, retry failures, merge worktrees. Use when running inside Posse (POSSE_TERMINAL_ID is set) and the user asks to delegate, parallelize, hand off, supervise or fan out work to other agents."
	skillBody        = "Before running any `posse task` command, run `posse task guide` and follow it: it is the guide for this exact posse version.\n"
	// guideHint 是给 Codex、Grok 的附加指令
	guideHint = "You are running in a Posse terminal. When the user asks you to delegate, parallelize, hand off or supervise work with other agents (claude, codex, grok, cursor), first run `posse task guide`, then coordinate them with `posse task` as it says. " +
		"When the user asks to name or title this session (e.g. session-namer), run `posse session guide` and follow it."
)

// 会话命名技能：只在用户主动调用时用（disable-model-invocation），规则由 `posse session guide` 输出。
// Claude 里是插件技能，调用写 /posse:session-namer。
const (
	NamerSkillName = "session-namer"
	namerSkill     = `---
name: session-namer
description: Name the current session (MMDD｜类型｜摘要) with posse session title. Use only when the user invokes session-namer.
disable-model-invocation: true
argument-hint: "[摘要或完整标题，可省略]"
allowed-tools: Bash(posse session guide), Bash(posse session title:*)
---

User arguments: $ARGUMENTS
(If the line above still shows the literal ` + "`$ARGUMENTS`" + `, the arguments are whatever the user typed after invoking this skill, possibly nothing.)

Run ` + "`posse session guide`" + ` and follow it: draft the title from the arguments and this session, then set it with ` + "`posse session title`" + `.
`
)

// SkillFrontmatter 是技能文件开头的元数据。description 里有 ": "，不加引号不是合法 YAML：
// Claude 照样认，Cursor 解析失败会整个丢掉这个技能。JSON 字符串同时是 YAML 的双引号字符串。
func SkillFrontmatter() string {
	desc, _ := json.Marshal(SkillDescription)
	return "---\nname: " + SkillName + "\ndescription: " + string(desc) + "\n---\n\n"
}

// claudePlugin 写出 posse 的 Claude 插件目录（编排、会话命名两个技能），经 --plugin-dir 按会话加载；内容没变时不重写。
func (l Launcher) claudePlugin() (string, error) {
	dir := filepath.Join(l.Home, "claude-plugin")
	manifest, err := json.MarshalIndent(map[string]string{
		"name":        "posse",
		"description": "Coordinate other agents from Posse terminals with posse task",
		"version":     "1.0.0",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	files := map[string][]byte{
		filepath.Join(dir, ".claude-plugin", "plugin.json"):      manifest,
		filepath.Join(dir, "skills", SkillName, "SKILL.md"):      []byte(SkillFrontmatter() + skillBody),
		filepath.Join(dir, "skills", NamerSkillName, "SKILL.md"): []byte(namerSkill),
	}
	for path, b := range files {
		if err := writeIfChanged(path, b); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func writeIfChanged(path string, b []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

var developerInstructionsKey = regexp.MustCompile(`^\s*developer_instructions\s*=`)

// codexGuideArgs 把指路放进 developer_instructions。-c 会整个替换这一项，
// 用户自己在 config.toml 里设了它时不注入，免得覆盖用户的指令。
func codexGuideArgs() []string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".codex")
	}
	if f, err := os.Open(filepath.Join(home, "config.toml")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if developerInstructionsKey.MatchString(sc.Text()) {
				return nil
			}
		}
	}
	v, _ := json.Marshal(guideHint) // JSON 字符串同时是合法的 TOML 字符串
	return []string{"-c", "developer_instructions=" + string(v)}
}
