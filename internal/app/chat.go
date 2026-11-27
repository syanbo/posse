package app

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/locales"
)

// 聊天导航切到目标主机的聊天工作区；第一次创建由目标常驻进程建目录、登记项目。
// 在里面开的 agent 由常驻进程各自放进一个新的子文件夹（internal/agent/chat.go），普通终端在聊天目录本身。

const chatBoardID = "chat"

// 远端目录只来自目标状态，不能把本机 home 或默认目录当作远端配置。
func (a *app) chatPath() string {
	if a.chatDir == "" && !a.remoteHost() {
		a.chatDir = agent.ChatDir(a.home)
	}
	return a.chatDir
}

func (a *app) remoteHost() bool {
	return a.viewID != "" && a.viewID != localHostID || a.c != nil && a.c.Remote || a.host != nil && a.host.id != localHostID
}

// chatWorkspace 是聊天目录对应的工作区，还没加成项目时是 nil。
func (a *app) chatWorkspace() *workspace {
	path := a.chatPath()
	if path == "" {
		return nil
	}
	if a.remoteHost() && a.daemon.Chat != nil {
		return a.workspace(a.daemon.Chat.WorkspaceID)
	}
	dir := filepath.Clean(path)
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

// chatOnly 报告项目是不是只有聊天工作区：它在全局图标栏有自己的入口，不再列进项目树和「运行中」。
// 聊天项目里另加了工作区时照常列出，免得那些工作区找不到。
func (a *app) chatOnly(ws []workspace) bool {
	return len(ws) == 1 && a.isChat(ws[0].ID)
}

// chatSidebarItem 是顶上的「聊天」磁贴。
func (a *app) chatSidebarItem(c *ui.Context) ui.Element {
	l := L(c)
	var badge func()
	w := a.chatWorkspace()
	if w != nil {
		if _, status := a.terminalSummary(w.Terminals); status != "" {
			badge = func() { statusGlyph(c, status, smallIcon()) }
		}
	}
	tile := navigationButton(c, chatBoardID, iconChat, l.ChatTitle(), a.displayed() == a && a.isChat(a.selected), badge)
	if w != nil {
		contextMenu(c, tile, func(m *popMenu) {
			// 收集菜单项不能切页面；执行选中动作时先显示所属主机，确认框和请求才有正确上下文。
			if m.chosen >= 0 && a.views != nil {
				a.views.show(a.viewID)
			}
			a.workspaceMenu(c, m, *w)
		})
	}
	if tile.Clicked() {
		a.openChat(L(c), false)
	}
	return tile
}

// chatHeader 是聊天侧栏的标题行：右边的加号用上一次聊天的 agent 再开一个，右键是聊天工作区的菜单。
func (a *app) chatHeader(c *ui.Context) {
	l := L(c)
	w := a.chatWorkspace()
	header := a.moduleHeader(c, a.moduleLabel(c, chatBoardID, l.ChatTitle()), func() {
		last := ""
		if w != nil {
			last = lastChatAgent(w)
		}
		if iconButton(c, iconPlus, a.withKey(l.KeysNewChatLabel(), "newChat")).Disabled(last == "").Clicked() && last != "" {
			a.openChat(l, true)
		}
	})
	if w != nil {
		contextMenu(c, header, func(m *popMenu) { a.workspaceMenu(c, m, *w) })
	}
}

// openChat 切到聊天工作区（停在上次看的标签），fresh 时再开一个新聊天：沿用最近一次聊天的 agent，
// 还没聊过时只切过去，空工作区里有选 agent 的卡片。聊天目录还不是项目时先建目录、加成项目。
func (a *app) openChat(l locales.Messages, fresh bool) {
	a = a.showModule(chatBoardID)
	if w := a.chatWorkspace(); w != nil {
		a.selected = w.ID
		if fresh {
			if ag := lastChatAgent(w); ag != "" {
				a.startTerminal(w.ID, ag)
			}
		}
		return
	}
	// 还没拿到项目列表时不知道它是不是已经加过；当前身份正在创建时不重复发请求。
	if !a.loaded || a.chatAdding != nil && a.chatAdding.current() {
		return
	}
	r := a.request()
	if !r.mayWrite() {
		return
	}
	params := rpc.ChatEnsureParams{ExpectedPath: a.chatPath(), Name: l.ChatTitle()}
	a.chatAdding = &r
	go func() {
		defer a.update(func() {
			if a.chatAdding == &r {
				a.chatAdding = nil
			}
		})
		ctx, cancel := context.WithTimeout(a.windowContext(), 10*time.Second)
		var location rpc.ChatLocation
		err := r.invoke(ctx, "chat.ensure", params, &location)
		cancel()
		r.refresh()
		r.update(func() {
			if err != nil {
				a.err = l.ChatAddProjectError(errtext.Text(err))
				return
			}
			if w := a.workspace(location.WorkspaceID); w != nil {
				a.selected = w.ID
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
	if a.chatPath() == "" || !agent.IsChatFolder(a.chatPath(), term.Dir) {
		return
	}
	m.Separator()
	if !a.remoteHost() && m.Item(l.ChatOpenFolder()).Icon(iconFolder).Chosen() {
		if err := mygo.Shell.OpenPath(term.Dir); err != nil {
			a.err = l.ChatOpenFolderError(errtext.Text(err))
		}
	}
	if m.Item(l.ChatConvertProject()).Icon(iconFolderLink).Chosen() {
		r := a.request()
		if !r.mayWrite() {
			return
		}
		dir := term.Dir
		go func() {
			if r.call("project.add", map[string]any{"path": dir}, nil, 10*time.Second) == nil {
				r.refresh()
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
