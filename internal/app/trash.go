package app

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"posse/internal/errtext"
	"posse/internal/history"
	"posse/locales"
)

// sessionTrashPaths 是把一个会话移到废纸篓要移走的文件和目录。只认各家会话目录里、
// 路径和会话 ID 对得上的东西，对不上就只移会话文件本身。
func sessionTrashPaths(s history.Session) []string {
	if s.Path == "" || s.ID == "" {
		return nil
	}
	if homeDir == "" {
		return nil
	}
	under := func(path string, root ...string) bool {
		rel, err := filepath.Rel(filepath.Join(append([]string{homeDir}, root...)...), path)
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
		// 分成几段的会话各段一起移走
		parts := s.Parts
		if len(parts) == 0 {
			parts = []string{s.Path}
		}
		for _, p := range parts {
			if !under(p, ".codex") {
				return nil
			}
		}
		return parts
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
func (a *app) askTrashSession(m locales.Messages, d *historyDetail) {
	s := d.Session
	if d.Terminal != "" {
		a.err = m.TrashSessionRunning()
		return
	}
	paths := sessionTrashPaths(s)
	if len(paths) == 0 {
		a.err = m.TrashUnknownPath(s.Path)
		return
	}
	a.confirm(m.TrashSessionConfirm(oneLine(s.Title)),
		m.TrashSessionBody(agentTitle(s.Agent)),
		m.CommonMoveToTrash(), func() {
			for _, p := range paths {
				if err := mygo.Shell.TrashItem(p); err != nil {
					a.win.Update(func() { a.err = m.TrashMoveError(errtext.Text(err)) })
					return
				}
			}
			a.call("history.index", map[string]any{"wait": true}, nil, time.Minute)
			a.win.Update(func() {
				if a.history.sel == sessionKey(s) {
					a.history.sel, a.history.detail = "", nil
				}
				a.recent.poll.at = time.Time{}
				a.searchHistory(0)
			})
		})
}

// trashListedSession 是列表上的删除：先问常驻进程这个会话是不是正在终端里运行，再走 askTrashSession 确认。
func (a *app) trashListedSession(m locales.Messages, s history.Session) {
	go func() {
		var d historyDetail
		if a.call("history.show", map[string]any{"session": s.ID, "path": s.Path, "tail": 1, "tools": false}, &d, 10*time.Second) != nil {
			return
		}
		a.win.Update(func() { a.askTrashSession(m, &d) })
	}()
}
