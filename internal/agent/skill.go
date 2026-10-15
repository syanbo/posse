package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// 编排技能：hive 启动的 agent 要派发、并行、监督其他 agent 时，先读 `hive task guide`。
// 正文由 hive 程序输出，和程序版本一致；这里只按会话注入一个入口，不写用户的全局配置：
// Claude、Cursor 用 --plugin-dir 加载 hive 生成的插件，Codex 和 Grok 没有按会话加载技能的办法，
// 把一句指路放进附加指令（Codex 的 developer_instructions、Grok 的 --rules）。

const (
	SkillName        = "hive-tasks"
	SkillDescription = "Dispatch work to other agents (claude, codex, grok, cursor) in Hive terminals and coordinate them with `hive task`: start workers, wait for results, answer their questions, retry failures, merge worktrees. Use when running inside Hive (HIVE_TERMINAL_ID is set) and the user asks to delegate, parallelize, hand off, supervise or fan out work to other agents."
	skillBody        = "Before running any `hive task` command, run `hive task guide` and follow it: it is the guide for this exact hive version.\n"
	// guideHint 是给 Codex、Grok 的附加指令
	guideHint = "You are running in a Hive terminal. When the user asks you to delegate, parallelize, hand off or supervise work with other agents (claude, codex, grok, cursor), first run `hive task guide`, then coordinate them with `hive task` as it says."
)

// SkillFrontmatter 是技能文件开头的元数据。
func SkillFrontmatter() string {
	return "---\nname: " + SkillName + "\ndescription: " + SkillDescription + "\n---\n\n"
}

// claudePlugin 写出只含编排技能的 Claude 插件目录，经 --plugin-dir 按会话加载；内容没变时不重写。
func (l Launcher) claudePlugin() (string, error) {
	dir := filepath.Join(l.Home, "claude-plugin")
	manifest, err := json.MarshalIndent(map[string]string{
		"name":        "hive",
		"description": "Coordinate other agents from Hive terminals with hive task",
		"version":     "1.0.0",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	files := map[string][]byte{
		filepath.Join(dir, ".claude-plugin", "plugin.json"): manifest,
		filepath.Join(dir, "skills", SkillName, "SKILL.md"): []byte(SkillFrontmatter() + skillBody),
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
