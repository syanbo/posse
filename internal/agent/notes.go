package agent

import (
	"bytes"
	"os"
	"path/filepath"
)

// notesHint 告诉 agent 文件放哪：留给人看的放项目根目录的 .posse/，临时的放系统临时目录，
// 不往下载、桌面和项目旁边随手放东西。只给 Claude（--append-system-prompt 不碰用户的配置）；
// Codex 的 developer_instructions 会整个替换用户的同名设置，Grok 的 --rules 也是附加指令，都不注入。
const notesHint = "Where files go: reports, plans and handoff notes meant for people go in $POSSE_NOTES (a git-ignored .posse folder at the project root), unless the project's own instructions name another place. " +
	"Throwaway files (test builds, screenshots, logs, scratch scripts, comparison copies) go in a fresh directory under $TMPDIR, not in the project, ~/Downloads, ~/Desktop or next to the project. " +
	"For a separate checkout, use `posse workspace create <project> <name> --mode worktree` instead of `git worktree add` or copying the repository."

// RemoveOldPlugins 删掉旧版按会话注入技能时写的文件：Claude 的插件目录、Cursor 插件里的技能。
// 技能现在装在用户的技能目录里（internal/skills 的 posse.go），留着会和装上的同名两份。
func RemoveOldPlugins(home string) error {
	if err := os.RemoveAll(filepath.Join(home, "claude-plugin")); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(home, "cursor-plugin", "skills"))
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
