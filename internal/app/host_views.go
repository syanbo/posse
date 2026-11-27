package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"sync/atomic"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/client"
	"posse/internal/remote"
)

const localHostID = "local"

func newHostApp(c *client.Client, home string) *app {
	a := &app{
		c: c, home: home, state: loadUIState(home),
		activeTerm: map[string]string{}, sectionsOpen: map[string]*bool{},
		lastStatus: map[string]string{}, lastTask: map[string]string{},
		diffs: map[string]*worktreeDiff{}, fontSize: defaultFontSize,
		pollWake: make(chan struct{}, 1),
	}
	a.selected, a.revealSel = a.state.Selected, true
	if !c.Remote {
		a.permission, a.disabled = agent.PermissionMode(c.Home), agent.Disabled(c.Home)
		a.restoreAll, a.chatDir = agent.RestoreAll(c.Home), agent.ChatDir(c.Home)
	}
	return a
}

func (v *hostViews) forHost(h *hostSession, c *client.Client) *app {
	if a := v.all[h.id]; a != nil {
		return a
	}
	a := newHostApp(c, hostUIHome(v.local.home, h.id))
	a.host = h
	v.add(h.id, a)
	return a
}

// hostViews 持有稳定的视图对象，不搬运 app 里的锁、编辑器缓冲区和后台队列。
// 只有 active 供轮询线程读取，其余成员及切换都由界面线程操作。
type suspendedRemoteHost struct {
	id   string
	host remote.Host
}

type hostViews struct {
	local           *app
	target          *app // 当前选择的工作区主机，临时进入本机设备页面时保留。
	suspendedRemote *suspendedRemoteHost
	all             map[string]*app
	order           []string
	active          atomic.Pointer[app]
}

func newHostViews(local *app) *hostViews {
	v := &hostViews{local: local, target: local, all: map[string]*app{localHostID: local}, order: []string{localHostID}}
	local.views, local.viewID = v, localHostID
	local.sshAuth = &sshAuthentication{wake: func() { local.update(func() {}) }}
	v.active.Store(local)
	return v
}

// 主机配置 ID 不作为路径片段，避免重命名、分隔符或大小写文件系统合并状态目录。
func hostUIHome(home, id string) string {
	if home == "" || id == localHostID {
		return home
	}
	return filepath.Join(home, "host-ui", fmt.Sprintf("%x", sha256.Sum256([]byte(id))))
}

func (v *hostViews) add(id string, a *app) bool {
	if id == "" || a == nil || a.views != nil || v.all[id] != nil {
		return false
	}
	a.views, a.viewID, a.win = v, id, v.local.win
	a.home = hostUIHome(v.local.home, id)
	v.all[id] = a
	v.order = append(v.order, id)
	v.copyPreferences(a)
	return true
}

func (a *app) windowOwner() *app {
	if a.views != nil {
		return a.views.local
	}
	return a
}

func (a *app) displayed() *app {
	if a.views != nil {
		return a.views.active.Load()
	}
	return a
}

func (v *hostViews) build(c *ui.Context) {
	a := v.active.Load()
	a.build(c.Key(a.viewID))
	v.local.dirtyCount.Store(int32(len(v.dirtyDocs())))
}

// activate 选择工作区主机；进入本机设备页面只调用 show，不改变工作区返回目标。
func (v *hostViews) activate(id string) bool {
	if !v.show(id) {
		return false
	}
	v.target = v.all[id]
	v.suspendedRemote = nil
	return true
}

// show 只切换显示与订阅，连接和显式接管由调用方另外发起。
// 切回原对象也换代，已经排队的旧确认不能因重新显示而恢复有效。
func (v *hostViews) show(id string) bool {
	next := v.all[id]
	if next == nil {
		return false
	}
	old := v.active.Load()
	if next == old {
		return true
	}
	// 从已连接的远端临时跳入本机设备页时记录连接配置基线，返回远端模块且配置未变时无缝重连；
	// 尚未建连成功的连接中状态不记录，主动切换或断开会清空。
	if id == localHostID && old != v.local && v.target == old && old.host != nil && !old.connecting && old.host.snapshot().Connected {
		if saved, ok := v.local.findSavedHost(old.viewID); ok {
			v.suspendedRemote = &suspendedRemoteHost{id: old.viewID, host: saved}
		}
	} else if id != localHostID {
		v.suspendedRemote = nil
	}
	if v.local.hosts != nil {
		v.local.hosts.leave()
	} else {
		v.local.sshAuth.cancelPending(false)
	}
	for _, files := range old.files {
		files.cancelGitReads()
	}
	old.viewGeneration.Add(1)
	old.closeTargetPreviews()
	if old.host != nil {
		lease := old.host.detach()
		if v.local.hosts != nil {
			v.local.hosts.release(lease)
		} else {
			go lease.release(context.Background())
		}
	}
	old.state.Selected = old.selected
	old.saveState()
	old.ensurePanes([maxColumns]string{})
	old.detachSkillRun()
	old.unsavedSeq++
	old.cancelConfirmation()
	old.confirming, old.creating = false, false
	old.confirmDo, old.confirmAltDo = nil, nil
	old.promptDlg, old.unsaved, old.branchPop = nil, nil, nil
	old.renaming = ""
	if old.host == nil {
		old.nameSent = nil
	}
	old.closeQuick()
	old.closeTargetDirectory()
	old.closeImages()
	old.setWantedTask("")
	old.setWantedDisc("", 0)
	old.setHistoryVisible(false)
	old.setMobileVisible(false)
	old.setSkillsVisible(false)
	old.focused.Store(false)
	old.viewing, old.seenFor = "", ""
	next.viewing, next.seenFor = "", ""
	next.viewGeneration.Add(1)
	next.closeTargetPreviews()
	v.copyPreferences(next)
	next.pollDown = true
	next.pollForce.Store(true)
	next.focused.Store(v.focused())
	v.active.Store(next)
	next.updateBadge()
	v.local.wakePoll()
	return true
}

func (v *hostViews) focused() bool {
	return v.local.windowFocused.Load()
}

func (v *hostViews) copyPreferences(a *app) {
	if a != v.local {
		copyWindowPreferences(&a.state, v.local.state)
	}
	a.fontSize = a.state.FontSize
	if a.fontSize == 0 {
		a.fontSize = defaultFontSize
	}
	a.termFamily, a.editorFamily = a.state.TermFont, a.state.EditorFont
	a.editorSize = editorSizeOf(a.state)
	a.locale = v.local.locale
	a.keysVer++
}

func (v *hostViews) dirtyDocs() []*fileDoc {
	var out []*fileDoc
	for _, id := range v.order {
		out = append(out, v.all[id].dirtyDocs()...)
	}
	return out
}

func (a *app) allDirtyDocs() []*fileDoc {
	if a.views != nil {
		return a.views.dirtyDocs()
	}
	return a.dirtyDocs()
}

func (a *app) documentOwner(d *fileDoc) *app {
	if a.views == nil {
		return a
	}
	for _, id := range a.views.order {
		owner := a.views.all[id]
		for _, f := range owner.files {
			if slices.Contains(f.docs, d) {
				return owner
			}
		}
	}
	return nil
}

// 资源 ID、文件路径、讨论阅读位置、设备地址和更新记录留在各自上下文。
func copyWindowPreferences(dst *uiState, src uiState) {
	dst.Theme = src.Theme
	dst.FontSize, dst.TermFont, dst.EditorFont, dst.EditorFontSize = src.FontSize, src.TermFont, src.EditorFont, src.EditorFontSize
	dst.TermWeight, dst.TermBoldWeight, dst.TermLineHeight, dst.TermSmooth = src.TermWeight, src.TermBoldWeight, src.TermLineHeight, src.TermSmooth
	dst.EditorWeight, dst.EditorBoldWeight, dst.EditorLineHeight, dst.EditorSmooth = src.EditorWeight, src.EditorBoldWeight, src.EditorLineHeight, src.EditorSmooth
	dst.UIFontSize, dst.UIWeight, dst.UIBoldWeight, dst.UISmooth, dst.IconSize = src.UIFontSize, src.UIWeight, src.UIBoldWeight, src.UISmooth, src.IconSize
	dst.TermCopyOnSelect, dst.LinkClickOpens, dst.LinkSwapSystem = src.TermCopyOnSelect, src.LinkClickOpens, src.LinkSwapSystem
	dst.NotifyOff, dst.BadgeOff, dst.CloseNoConfirm = src.NotifyOff, src.BadgeOff, src.CloseNoConfirm
	dst.DoubleShift, dst.SessionButtonsHidden = src.DoubleShift, src.SessionButtonsHidden
	dst.UsageRemaining, dst.ImageCheckerOff, dst.UsageSkillsOnly, dst.UsageWebOnly = src.UsageRemaining, src.ImageCheckerOff, src.UsageSkillsOnly, src.UsageWebOnly
	dst.HistoryByFolder, dst.ChangesByDir, dst.GitInTabBar = src.HistoryByFolder, src.ChangesByDir, src.GitInTabBar
	dst.NavigationOrder = slices.Clone(src.NavigationOrder)
	dst.Keys = cloneKeyBindings(src.Keys)
}

func cloneKeyBindings(src map[string][]string) map[string][]string {
	if src == nil {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for id, keys := range src {
		dst[id] = slices.Clone(keys)
	}
	return dst
}
