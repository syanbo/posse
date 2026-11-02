package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/locales"
)

// 聊天磁贴：侧栏顶上的「聊天」切到聊天目录（默认 ~/Posse）的工作区，第一次点时建目录、加成项目。
// 在里面开的 agent 由常驻进程各自放进一个新的子文件夹（internal/agent/chat.go），普通终端在聊天目录本身。

const chatBoardID = "chat"

// chatPath 是聊天目录：启动时从 agents.json 读进 chatDir，设置里改了跟着换。
func (a *app) chatPath() string {
	if a.chatDir == "" {
		a.chatDir = agent.ChatDir(a.home)
	}
	return a.chatDir
}

// chatWorkspace 是聊天目录对应的工作区，还没加成项目时是 nil。
func (a *app) chatWorkspace() *workspace {
	dir := filepath.Clean(a.chatPath())
	for i := range a.workspaces {
		if w := &a.workspaces[i]; w.Kind == "main" && filepath.Clean(w.Path) == dir {
			return w
		}
	}
	return nil
}

func (a *app) isChat(id string) bool {
	w := a.chatWorkspace()
	return w != nil && w.ID == id
}

// chatSidebarItem 是顶上的「聊天」磁贴。
func (a *app) chatSidebarItem(c *ui.Context) ui.Element {
	l := L(c)
	icon := func() { ui.Icon(c, iconChat).FontSize(iconSize).TextColor(c.Theme().Accent) }
	tile := a.workspaceTile(c, chatBoardID, a.chatWorkspace(), icon, l.ChatTitle())
	if tile.Clicked() {
		a.openChat(L(c), false)
	}
	return tile
}

// openChat 切到聊天工作区（停在上次看的标签），fresh 时再开一个新聊天：沿用最近一次聊天的 agent，
// 还没聊过时只切过去，空工作区里有选 agent 的卡片。聊天目录还不是项目时先建目录、加成项目。
func (a *app) openChat(l locales.Messages, fresh bool) {
	if w := a.chatWorkspace(); w != nil {
		a.selected = w.ID
		if fresh {
			if ag := lastChatAgent(w); ag != "" {
				a.startTerminal(w.ID, ag)
			}
		}
		return
	}
	// 还没拿到项目列表时不知道它是不是已经加过；正在加的不重复加
	if !a.loaded || a.chatAdding {
		return
	}
	a.chatAdding = true
	dir := a.chatPath()
	go func() {
		defer a.update(func() { a.chatAdding = false })
		if err := os.MkdirAll(dir, 0o755); err != nil {
			a.update(func() { a.err = l.ChatCreateFolderError(errtext.Text(err)) })
			return
		}
		// 已经有叫「聊天」的项目时用目录名；目录已经是项目（常驻进程认的路径写法不同）时两次都失败，刷新后照样找得到
		var err error
		for _, params := range []map[string]any{{"path": dir, "name": l.ChatTitle()}, {"path": dir}} {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = a.c.Call(ctx, "project.add", params, nil)
			cancel()
			if err == nil {
				break
			}
		}
		a.refresh()
		a.update(func() {
			if w := a.chatWorkspace(); w != nil {
				a.selected = w.ID
			} else if err != nil {
				a.err = l.ChatAddProjectError(errtext.Text(err))
			}
		})
	}()
}

// lastChatAgent 是聊天工作区里最近开的 agent 会话用的 agent。
func lastChatAgent(w *workspace) string {
	for _, t := range slices.Backward(w.Terminals) {
		if t.Agent != "" {
			return t.Agent
		}
	}
	return ""
}

// chatFolderMenu 是聊天会话的右键菜单项：打开它的文件夹，或者把聊天里做出来的东西转成一个项目（原地登记，不挪目录）。
func (a *app) chatFolderMenu(l locales.Messages, m *popMenu, term terminalInfo) {
	if !agent.IsChatFolder(a.chatPath(), term.Dir) {
		return
	}
	m.Separator()
	if m.Item(l.ChatOpenFolder()).Icon(iconFolder).Chosen() {
		if err := mygo.Shell.OpenPath(term.Dir); err != nil {
			a.err = l.ChatOpenFolderError(errtext.Text(err))
		}
	}
	if m.Item(l.ChatConvertProject()).Chosen() {
		dir := term.Dir
		go func() {
			if a.call("project.add", map[string]any{"path": dir}, nil, 10*time.Second) == nil {
				a.refresh()
			}
		}()
	}
}

// chatSettings 是设置页的聊天目录：显示在用的目录，可以换成别的或恢复默认。
// 只在这里改（存在 agents.json），手机和 CLI 改不了，免得被远程指到任意目录。
func (a *app) chatSettings(c *ui.Context) {
	l := L(c)
	settingsCard(c, l.ChatTitle(), l.ChatSettingsNote(), func() {
		t := c.Theme()
		settingsRow(c, shortPath(a.chatPath()), func() {
			if a.chatPath() != agent.DefaultChatDir() && textButton(c, l.CommonResetToDefault()).Clicked() {
				a.setChatDir("")
			}
			if textButton(c, l.ChatChangeFolderButton()).Clicked() {
				go a.pickChatDir(L(c))
			}
		})
		ui.Text(c, l.ChatGrokNote()).FontSize(fsSmall).TextColor(t.TextMuted)
	})
}

func (a *app) pickChatDir(l locales.Messages) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: l.ChatChooseFolderTitle(), ButtonLabel: l.ChatChooseButton(),
		DefaultPath: nearestDir(a.chatPath()), Directory: true})
	if err != nil || len(paths) == 0 {
		return
	}
	a.update(func() { a.setChatDir(paths[0]) })
}

func (a *app) setChatDir(dir string) {
	if a.home != "" {
		if err := agent.SetChatDir(a.home, dir); err != nil {
			a.err = errtext.Text(err)
			return
		}
	}
	a.chatDir = agent.DefaultChatDir()
	if dir != "" {
		a.chatDir = filepath.Clean(dir)
	}
}
