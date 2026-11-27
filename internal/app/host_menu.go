package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"posse/internal/client"
	"posse/internal/errtext"
	"posse/internal/remote"
	"posse/locales"
)

type hostFileStamp struct {
	loaded bool
	exists bool
	mod    time.Time
	size   int64
	ino    uint64
}

type hostMenuState struct {
	hosts         []remote.Host
	names         map[string]string
	releasing     uint64
	openRequested bool
	stampMu       sync.Mutex
	stamp         hostFileStamp
}

func (a *app) openHostMenu() {
	if a.views == nil {
		return
	}
	owner := a.windowOwner()
	a.loadHostMenu()
	// 还没有配置任何远程主机时，直接进入添加弹窗；配置文件损坏导致 a.err 非空时仍打开菜单显示错误条
	if len(owner.hostMenu.hosts) == 0 && a.err == "" && a.viewID == localHostID {
		a.openHostDialog(remote.Host{})
		return
	}
	owner.hostMenu.openRequested = true
}

func statHostFile(path string) hostFileStamp {
	st, err := os.Stat(path)
	if err != nil {
		return hostFileStamp{loaded: true, exists: false}
	}
	var ino uint64
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		ino = sys.Ino
	}
	return hostFileStamp{loaded: true, exists: true, mod: st.ModTime(), size: st.Size(), ino: ino}
}

func (a *app) loadHostMenu() {
	owner := a.windowOwner()
	path := filepath.Join(owner.home, "hosts.json")
	stamp := statHostFile(path)
	owner.hostMenu.stampMu.Lock()
	owner.hostMenu.stamp = stamp
	owner.hostMenu.stampMu.Unlock()
	hosts, err := remote.Load(owner.home)
	owner.hostMenu.hosts = hosts
	if owner.hostMenu.names == nil {
		owner.hostMenu.names = make(map[string]string)
	}
	for _, h := range hosts {
		owner.hostMenu.names[h.ID] = h.Name
	}
	if err != nil {
		a.err = errtext.Text(err)
	}
}

// checkHostsFile 在每秒轮询中 stat 本机 hosts.json；仅当文件身份、大小或修改时间真正变化时才排队重绘。
func (a *app) checkHostsFile() {
	owner := a.windowOwner()
	if owner.home == "" {
		return
	}
	cur := statHostFile(filepath.Join(owner.home, "hosts.json"))
	owner.hostMenu.stampMu.Lock()
	same := owner.hostMenu.stamp == cur
	owner.hostMenu.stampMu.Unlock()
	if same {
		return
	}
	owner.update(func() {
		owner.loadHostMenu()
	})
}

// orderedHosts 按本机 ui.json 的 HostOrder 排好序，未登记的新主机排在末尾。
func (a *app) orderedHosts() []remote.Host {
	owner := a.windowOwner()
	hosts := owner.hostMenu.hosts
	if len(hosts) <= 1 || len(owner.state.HostOrder) == 0 {
		return slices.Clone(hosts)
	}
	byID := make(map[string]remote.Host, len(hosts))
	for _, h := range hosts {
		byID[h.ID] = h
	}
	out := make([]remote.Host, 0, len(hosts))
	seen := make(map[string]bool, len(hosts))
	for _, id := range owner.state.HostOrder {
		if h, ok := byID[id]; ok && !seen[id] {
			out = append(out, h)
			seen[id] = true
		}
	}
	for _, h := range hosts {
		if !seen[h.ID] {
			out = append(out, h)
		}
	}
	return out
}

func (a *app) findSavedHost(id string) (remote.Host, bool) {
	for _, h := range a.windowOwner().hostMenu.hosts {
		if h.ID == id {
			return h, true
		}
	}
	return remote.Host{}, false
}

func (a *app) hostName(m locales.Messages) string {
	if a.viewID == "" || a.viewID == localHostID {
		return m.HostLocal()
	}
	if name := a.windowOwner().hostMenu.names[a.viewID]; name != "" {
		return name
	}
	return a.viewID
}

// hostMonograms 为每台远端主机生成 1–2 字的磁贴缩写；撞字时依次尝试词首字母、首尾字母和数字后缀。
func hostMonograms(hosts []remote.Host) map[string]string {
	out := make(map[string]string, len(hosts))
	used := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		cands := monogramCandidates(h.Name)
		chosen := ""
		for _, cand := range cands {
			if cand != "" && !used[cand] {
				chosen = cand
				break
			}
		}
		if chosen == "" {
			base := "S"
			if len(cands) > 0 {
				base = string([]rune(cands[0])[:1])
			}
			for n := 2; n < 100; n++ {
				cand := base + strconv.Itoa(n)
				if !used[cand] {
					chosen = cand
					break
				}
			}
			if chosen == "" {
				chosen = base
			}
		}
		used[chosen] = true
		out[h.ID] = chosen
	}
	return out
}

func monogramCandidates(name string) []string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return []string{"SH"}
	}
	runes := []rune(trimmed)
	// 非 ASCII（如中文）：优先取首字，撞字再取前两字
	if runes[0] > 127 {
		cands := []string{string(runes[0])}
		if len(runes) > 1 {
			cands = append(cands, string(runes[:2]))
		}
		return cands
	}
	var cands []string
	add := func(s string) {
		s = strings.ToUpper(s)
		if s != "" && !slices.Contains(cands, s) {
			cands = append(cands, s)
		}
	}
	// 收集纯字母数字字符
	var alnum []rune
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			alnum = append(alnum, r)
		}
	}
	if len(alnum) >= 2 {
		add(string(alnum[:2]))
	} else if len(alnum) == 1 {
		add(string(alnum[0]))
	}
	// 多词首字母（如 Office Mac -> OM）
	words := strings.FieldsFunc(trimmed, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	if len(words) >= 2 {
		w0, w1 := []rune(words[0]), []rune(words[1])
		add(string([]rune{w0[0], w1[0]}))
	}
	if len(alnum) >= 3 {
		add(string([]rune{alnum[0], alnum[len(alnum)-1]}))
	}
	return cands
}

// hostActionsBuild 捕获当前帧的快照构建单台主机右键菜单，确保同一帧收集与执行两遍项号完全一致。
func (a *app) hostActionsBuild(m locales.Messages, h remote.Host, active bool) func(*popMenu) {
	var s hostSnapshot
	if active && a.host != nil {
		s = a.host.snapshot()
	}
	connecting, releasing := a.connecting, a.hostMenu.releasing != 0
	return func(menu *popMenu) {
		if h.ID == localHostID {
			if active && a.host != nil && s.Connected && !s.Controlled {
				if menu.Item(m.HostTake()).Disabled(connecting || releasing).Chosen() {
					a.takeHost(m, s)
				}
				menu.Separator()
			}
			if menu.Item(m.HostAddItem()).Icon(iconPlus).Chosen() {
				a.openHostDialog(remote.Host{})
			}
			return
		}

		if active {
			switch {
			case connecting:
				if menu.Item(m.HostCancelConnect()).Chosen() {
					a.windowOwner().hosts.disconnect(h.ID)
				}
			case !s.Connected:
				if menu.Item(m.HostReconnect()).Chosen() {
					a.selectRemoteHost(h)
				}
			default:
				if !s.Controlled {
					if menu.Item(m.HostTake()).Disabled(releasing).Chosen() {
						a.takeHost(m, s)
					}
				}
				if menu.Item(m.HostDisconnect()).Chosen() {
					a.windowOwner().hosts.disconnect(h.ID)
				}
			}
		} else {
			if menu.Item(m.HostConnect()).Chosen() {
				a.selectRemoteHost(h)
			}
		}
		menu.Separator()
		if menu.Item(m.HostEditItem()).Chosen() {
			a.openHostDialog(h)
		}
		if menu.Item(m.HostRemoveItem()).Danger().Chosen() {
			a.confirmRemoveHost(m, h)
		}
	}
}

// hostSwitchBuild 构建 ⌃⌥H 弹出的主机切换菜单（同一帧收集与执行共用快照）。
func (a *app) hostSwitchBuild(m locales.Messages) func(*popMenu) {
	var s hostSnapshot
	if a.host != nil {
		s = a.host.snapshot()
	}
	hosts := a.orderedHosts()
	connecting, releasing := a.connecting, a.hostMenu.releasing != 0
	targetID := localHostID
	if a.views != nil && a.views.target != nil {
		targetID = a.views.target.viewID
	}
	return func(menu *popMenu) {
		if menu.Item(m.HostLocal()).Checked(targetID == localHostID).Chosen() {
			a.selectLocalHost()
		}
		for _, h := range hosts {
			item := menu.Item(h.Name).Checked(targetID == h.ID)
			if a.viewID == h.ID && connecting {
				item.Spin(true)
			}
			if item.Chosen() {
				a.openHost(h)
			}
		}
		if a.host != nil {
			if a.viewID != localHostID {
				selected, ok := a.findSavedHost(a.viewID)
				if connecting {
					menu.Separator()
					if menu.Item(m.HostCancelConnect()).Chosen() {
						a.windowOwner().hosts.disconnect(a.viewID)
					}
				} else if !s.Connected && ok {
					menu.Separator()
					if menu.Item(m.HostReconnect()).Chosen() {
						a.selectRemoteHost(selected)
					}
				} else if s.Connected {
					menu.Separator()
					if !s.Controlled {
						if menu.Item(m.HostTake()).Disabled(releasing).Chosen() {
							a.takeHost(m, s)
						}
					}
					if menu.Item(m.HostDisconnect()).Chosen() {
						a.windowOwner().hosts.disconnect(a.viewID)
					}
				}
			} else if s.Connected && !s.Controlled {
				menu.Separator()
				if menu.Item(m.HostTake()).Disabled(connecting || releasing).Chosen() {
					a.takeHost(m, s)
				}
			}
		}
		menu.Separator()
		if menu.Item(m.HostAddItem()).Icon(iconPlus).Chosen() {
			a.openHostDialog(remote.Host{})
		}
	}
}

func (a *app) selectLocalHost() {
	if a.views == nil {
		return
	}
	local := a.views.local
	// 仅当显示视图和工作区目标都已是本机时才保留当前页面；
	// 处于本机设备页（设置/技能/手机/讨论）且 target 仍是远端时，必须把 target 切回本机。
	if a.views.active.Load() == local && a.views.target == local {
		return
	}
	if local.host != nil {
		local.host.prepareLocalReturn()
	}
	a.views.activate(localHostID)
	local.openWorkspaces()
}

func (a *app) isHostManuallyDisconnected(id string) bool {
	return id != "" && slices.Contains(a.windowOwner().state.DisconnectedHosts, id)
}

func (a *app) setHostManuallyDisconnected(id string, disconnected bool) {
	if id == "" || id == localHostID {
		return
	}
	owner := a.windowOwner()
	idx := slices.Index(owner.state.DisconnectedHosts, id)
	if disconnected {
		if idx >= 0 {
			return
		}
		owner.state.DisconnectedHosts = append(owner.state.DisconnectedHosts, id)
		owner.saveState()
		return
	}
	if idx < 0 {
		return
	}
	owner.state.DisconnectedHosts = slices.Delete(owner.state.DisconnectedHosts, idx, idx+1)
	owner.saveState()
}

// openHost 是点击左侧导航栏远端主机磁贴或 ⌃⌥H 列表项的动作：
// 已连接、连接中或用户主动断开（含重启后从 ui.json 恢复）时直接切回该主机视图（不重复拆建，也不擅自重连）；
// 其余未连接状态走 selectRemoteHost 发起连接。
func (a *app) openHost(h remote.Host) {
	if a.views == nil {
		return
	}
	owner := a.windowOwner()
	target := a.views.all[h.ID]
	if a.isHostManuallyDisconnected(h.ID) {
		if owner.hostMenu.names == nil {
			owner.hostMenu.names = make(map[string]string)
		}
		owner.hostMenu.names[h.ID] = h.Name
		if target == nil {
			target = a.views.forHost(newHostSession(h.ID, appName()), &client.Client{Remote: true})
		}
		if a.views.active.Load() != target {
			a.views.activate(h.ID)
		}
		if !target.workspaceModule() {
			target.openWorkspaces()
		}
		return
	}
	if target != nil && (target.connecting || target.host != nil && target.host.snapshot().Connected || owner.hosts == nil) {
		if a.views.active.Load() != target {
			a.views.activate(h.ID)
		}
		if !target.workspaceModule() {
			target.openWorkspaces()
		}
		return
	}
	a.selectRemoteHost(h)
}

func (a *app) selectRemoteHost(h remote.Host) {
	k := a.windowOwner().hosts
	if k == nil || k.closed {
		return
	}
	if a.viewID == h.ID && (a.connecting || a.host != nil && a.host.snapshot().Connected) {
		if !a.workspaceModule() {
			a.openWorkspaces()
		}
		return
	}
	if k.views.local.hostMenu.names == nil {
		k.views.local.hostMenu.names = make(map[string]string)
	}
	k.views.local.hostMenu.names[h.ID] = h.Name
	k.connectRemote(h, true)
	if active := k.views.active.Load(); active != nil && !active.workspaceModule() {
		active.openWorkspaces()
	}
}

func (a *app) takeHost(m locales.Messages, s hostSnapshot) {
	r := a.request()
	if r.host == nil || !r.host.matches(s) || s.Controlled || a.hostMenu.releasing != 0 {
		return
	}
	take := func() {
		if r.current() {
			a.hostControlJob(s.Generation, func(ctx context.Context) error { return r.host.take(ctx, s) })
		}
	}
	if s.Desktop.Owner == "" || s.Desktop.Owner == a.host.controllerID {
		take()
		return
	}
	a.confirm(m.HostTakeTitle(), m.HostTakeBody(s.Desktop.Label), m.HostTake(), func() {
		// 确认框的回调在后台调用，排回界面线程后再次核对打开时的主机和身份。
		a.update(take)
	})
}

func (a *app) releaseHost(s hostSnapshot) {
	lease := a.host.yield(s)
	if lease.client == nil {
		return
	}
	a.ensurePanes([maxColumns]string{})
	a.closeTargetPreviews()
	viewer := a.host.snapshot()
	a.hostMenu.releasing = viewer.Generation
	a.hostControlJob(viewer.Generation, func(ctx context.Context) error {
		if err := lease.release(context.Background()); err != nil {
			return err
		}
		a.host.released(lease)
		if ctx.Err() == nil && a.host.matches(viewer) {
			return a.host.refresh(ctx)
		}
		return nil
	})
}

func (a *app) hostControlJob(generation uint64, work func(context.Context) error) {
	k := a.windowOwner().hosts
	if k == nil || k.closed {
		return
	}
	viewGeneration := a.viewGeneration.Load()
	ctx := k.ctx
	k.jobs.Go(func() {
		err := work(ctx)
		a.update(func() {
			if a.hostMenu.releasing == generation {
				a.hostMenu.releasing = 0
			}
			if k.closed || a.displayed() != a || a.viewGeneration.Load() != viewGeneration || a.host.snapshot().Generation != generation {
				return
			}
			a.closeTargetPreviews()
			a.pollForce.Store(true)
			if err != nil {
				a.err = errtext.Text(err)
			}
		})
	})
}

func (a *app) hostStatus(m locales.Messages) string {
	if a.host == nil {
		return ""
	}
	if a.connecting {
		return a.connectStageText(m)
	}
	s := a.host.snapshot()
	if !s.Connected {
		return m.HostDisconnected()
	}
	if s.Controlled {
		return ""
	}
	if s.Desktop.Owner != "" && s.Desktop.Owner != a.host.controllerID {
		return m.HostViewingOwner(s.Desktop.Label)
	}
	return m.HostViewing()
}
