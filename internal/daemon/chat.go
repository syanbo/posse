package daemon

import (
	"context"
	"encoding/json"
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

// chatLocation 只发现目标配置和已有登记，不因查看聊天而创建目录或项目。
func (d *Daemon) chatLocation() (rpc.ChatLocation, error) {
	location := rpc.ChatLocation{Path: agent.ChatDir(d.cfg.Home)}
	ws, err := d.store.Workspaces("")
	if err != nil {
		return location, err
	}
	for _, w := range ws {
		if w.Kind == "main" && samePath(w.Path, location.Path) {
			location.Path, location.WorkspaceID = w.Path, w.ID
			break
		}
	}
	return location, nil
}

// chatEnsure 的目录只从目标配置取；客户端的路径用于核对，不能指定新的聊天目录。
func (d *Daemon) chatEnsure(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.ChatEnsureParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	d.chatMu.Lock()
	defer d.chatMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	location, err := d.chatLocation()
	if err != nil {
		return nil, err
	}
	if p.ExpectedPath == "" || !samePath(p.ExpectedPath, location.Path) {
		return nil, rpc.Errorf(rpc.CodeConflict, "chat directory changed; refresh before creating a chat workspace")
	}
	if location.WorkspaceID != "" {
		return location, nil
	}
	name := p.Name
	if name == "" {
		name = filepath.Base(location.Path)
	}
	ps, err := d.store.Projects()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Name == name {
			name = filepath.Base(location.Path)
			break
		}
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(location.Path, 0o755); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "create chat folder: %v", err)
	}
	params, _ := json.Marshal(projectAddParams{Path: location.Path, Name: name})
	_, err = d.projectAdd(ctx, params)
	// 其他入口可能刚登记同一路径；只核对已存在的结果，不重发创建。
	current, readErr := d.chatLocation()
	if readErr == nil && current.WorkspaceID != "" && samePath(current.Path, location.Path) {
		return current, nil
	}
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	return nil, rpc.Errorf(rpc.CodeConflict, "chat directory changed; refresh before creating a chat workspace")
}

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
// 不删的：常驻进程退出时（重启后恢复的终端要回到这个目录）、已经「转为项目」的、还有别的终端在里面运行的（派出去的 worker）。
// 删掉以后从历史页恢复这个会话时再建回来（historyResume）。路径一律经 samePath 比：重启后库里记的是解析过符号链接的目录。
func (d *Daemon) dropChatFolder(id, dir string) {
	if d.exiting.Load() || !d.isChatFolder(dir) {
		return
	}
	if ps, err := d.store.Projects(); err != nil || slices.ContainsFunc(ps, func(p store.Project) bool { return samePath(p.Path, dir) }) {
		return
	}
	var others []string
	d.mu.Lock()
	for _, s := range d.terms {
		if s.ID != id && s.Running() {
			others = append(others, s.Dir)
		}
	}
	d.mu.Unlock()
	if !slices.ContainsFunc(others, func(o string) bool { return samePath(o, dir) }) {
		os.Remove(dir)
	}
}
