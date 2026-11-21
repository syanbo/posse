package history

import (
	"os"
	"path/filepath"
)

// WatchDirs 是历史页开着时要盯的会话目录。只含目录：文件变化由目录上的监视报上来，
// 不给每个会话文件单独占一个句柄。Cursor 的 IDE 对话在 state.vscdb，编辑器自己也在写，不在这里。
func WatchDirs(home string) []string {
	if home == "" {
		return nil
	}
	var out []string
	add := func(p string) {
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return
		}
		out = append(out, p)
	}
	add(filepath.Join(home, ".claude", "projects"))
	add(filepath.Join(home, ".codex", "sessions"))
	add(filepath.Join(home, ".codex", "archived_sessions"))
	add(filepath.Join(home, ".cursor", "projects"))
	add(filepath.Join(home, ".cursor", "chats"))
	add(filepath.Join(home, ".grok", "sessions"))
	for _, app := range antigravityApps {
		add(filepath.Join(home, ".gemini", app, "brain"))
	}
	return out
}
