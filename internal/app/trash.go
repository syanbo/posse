package app

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"hive/internal/history"
)

// sessionTrashPaths 是把一个会话移到废纸篓要移走的文件和目录。只认各家会话目录里、
// 路径和会话 ID 对得上的东西，对不上就只移会话文件本身。
func sessionTrashPaths(s history.Session) []string {
	if s.Path == "" || s.ID == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	under := func(path string, root ...string) bool {
		rel, err := filepath.Rel(filepath.Join(append([]string{home}, root...)...), path)
		return err == nil && !strings.HasPrefix(rel, "..")
	}
	isDir := func(path string) bool {
		fi, err := os.Stat(path)
		return err == nil && fi.IsDir()
	}
	switch s.Agent {
	case "claude":
		// <会话>.jsonl 旁边同名的目录放着子 agent 和工具输出
		if !under(s.Path, ".claude", "projects") {
			return nil
		}
		out := []string{s.Path}
		if dir := strings.TrimSuffix(s.Path, ".jsonl"); filepath.Base(dir) == s.ID && isDir(dir) {
			out = append(out, dir)
		}
		return out
	case "codex":
		if under(s.Path, ".codex") {
			return []string{s.Path}
		}
	case "grok":
		// 一个会话一个目录：chat_history、updates、summary 等都在里面
		if dir := filepath.Dir(s.Path); filepath.Base(dir) == s.ID && under(dir, ".grok", "sessions") {
			return []string{dir}
		}
	case "cursor":
		if dir := filepath.Dir(s.Path); filepath.Base(dir) == s.ID && under(dir, ".cursor", "projects") {
			return []string{dir}
		}
	}
	return nil
}

// askTrashSession 确认后把会话移到废纸篓，再让常驻进程更新索引，会话就从历史里去掉。
func (a *app) askTrashSession(d *historyDetail) {
	s := d.Session
	if d.Terminal != "" {
		a.err = "这个会话正在 Hive 的终端里运行，先关掉那个终端再移到废纸篓。"
		return
	}
	paths := sessionTrashPaths(s)
	if len(paths) == 0 {
		a.err = "认不出这个会话的文件位置，没有移动：" + s.Path
		return
	}
	a.confirm("把会话「"+oneLine(s.Title)+"」移到废纸篓？",
		"会话文件会移到废纸篓，可以从废纸篓找回。"+agentTitle(s.Agent)+" 自己的恢复列表里也会看不到它。",
		"移到废纸篓", func() {
			for _, p := range paths {
				if err := mygo.Shell.TrashItem(p); err != nil {
					a.win.Update(func() { a.err = "移到废纸篓失败：" + err.Error() })
					return
				}
			}
			a.call("history.index", map[string]any{"wait": true}, nil, time.Minute)
			a.win.Update(func() {
				if a.history.sel == s.ID {
					a.history.sel, a.history.detail = "", nil
				}
				a.recent.fetched = time.Time{}
			})
			a.searchHistory(0)
		})
}
