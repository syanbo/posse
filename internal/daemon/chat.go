package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 聊天工作区（聊天目录加成的项目，见 internal/agent/chat.go）：用户开的 agent 各在一个新的子文件夹里，
// 聊天结束时没留下文件的子文件夹删掉。

// startUserAgent 启动用户开的 agent 会话（窗口、手机、CLI 的 terminal create）：在聊天工作区里先建一个子文件夹。
// 任务的 worker 不经这里，见 taskStart。
func (d *Daemon) startUserAgent(w store.Workspace, name string, model agent.Model, prompt string, cols, rows int) (*session, error) {
	dir, err := d.chatFolder(w)
	if err != nil {
		return nil, err
	}
	s, err := d.startAgentIn(w, dir, name, model, prompt, cols, rows)
	if err != nil && dir != "" {
		// 没启动起来就不留空文件夹
		os.Remove(dir)
	}
	return s, err
}

// chatFolder 是在聊天工作区里启动 agent 时新建的子文件夹，别的工作区是空串（在工作区目录启动）。
func (d *Daemon) chatFolder(w store.Workspace) (string, error) {
	if w.Kind != "main" || !samePath(w.Path, agent.ChatDir(d.cfg.Home)) {
		return "", nil
	}
	dir, err := agent.NewChatFolder(w.Path, time.Now())
	if err != nil {
		return "", rpc.Errorf(rpc.CodeInternal, "create chat folder: %v", err)
	}
	return dir, nil
}

// isChatFolder 判断 dir 是不是某个项目目录下面一次聊天的文件夹。按工作区的路径认、不按现在的聊天目录：
// 改了聊天目录以后，原来那些聊天的文件夹照样清理、恢复。
func (d *Daemon) isChatFolder(dir string) bool {
	if dir == "" || !agent.IsChatFolderName(filepath.Base(dir)) {
		return false
	}
	ws, err := d.store.Workspaces("")
	if err != nil {
		return false
	}
	parent := filepath.Dir(dir)
	return slices.ContainsFunc(ws, func(w store.Workspace) bool { return w.Kind == "main" && samePath(w.Path, parent) })
}

// dropChatFolder 在聊天结束（终端关掉或自己退出）后删掉没留下文件的聊天文件夹（os.Remove 只删空目录）。
// 不删的：常驻进程退出时（重启后恢复的终端要回到这个目录）、已经「转为项目」的、还有别的终端在里面启动的（派出去的 worker）。
// 删掉以后从历史页恢复这个会话时再建回来（historyResume）。
func (d *Daemon) dropChatFolder(id, dir string) {
	if d.exiting.Load() || !d.isChatFolder(dir) {
		return
	}
	if _, err := d.store.FindProject(dir); err == nil {
		return
	}
	d.mu.Lock()
	inUse := false
	for _, s := range d.terms {
		if s.ID != id && samePath(s.Dir, dir) {
			inUse = true
			break
		}
	}
	d.mu.Unlock()
	if !inUse {
		os.Remove(dir)
	}
}
