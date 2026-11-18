package app

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/remote"
	"posse/locales"
)

type hostMenuState struct {
	hosts         []remote.Host
	names         map[string]string
	releasing     uint64
	openRequested bool
}

func (a *app) openHostMenu() {
	if a.views == nil {
		return
	}
	a.windowOwner().hostMenu.openRequested = true
}

func (a *app) loadHostMenu() {
	owner := a.windowOwner()
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

func (a *app) hostName(m locales.Messages) string {
	if a.viewID == "" || a.viewID == localHostID {
		return m.HostLocal()
	}
	return a.windowOwner().hostMenu.names[a.viewID]
}

// 主机名在侧栏顶栏余下的宽度内截断；收起侧栏后仍按同一宽度留位。
func (a *app) hostMenuWidth(c *ui.Context) float32 {
	if a.views == nil {
		return 0
	}
	w, _ := c.MeasureText(0, ui.Span{Text: a.hostName(L(c)), Size: fsSmall, Weight: fwBody})
	available := navigationWidth() + a.sidebarWidth() - c.TitleBar().Left - titleGap - appNameWidth(c) - 2*appNameGap - buttonBox() - iconBarPad()
	return min(w+16+4+fsSmall, max(buttonBox(), available))
}

func (a *app) hostMenuButton(c *ui.Context) {
	if a.views == nil {
		return
	}
	name := a.hostName(L(c))
	b := ui.ButtonBase(c.Key("host-menu")).Width(a.hostMenuWidth(c)).Height(compactBox()).Padding(0, 8).Radius(rSmall).Label(name).Tooltip(a.withKey(name, "switchHost"))
	if b.Hovered() {
		b.Background(colors(c.Theme()).wash)
	}
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(fsSmall).TextColor(c.Theme().TextMuted).SingleLine().Ellipsis("…").Shrink(1)
			ui.Icon(c, iconDown).Size(fsSmall, fsSmall).TextColor(c.Theme().TextMuted).Shrink(0)
		})
	})
	if owner := a.windowOwner(); owner.hostMenu.openRequested {
		owner.hostMenu.openRequested = false
		st := menuStateOf(b)
		st.open = true
		st.reset()
	}
	var build func(*popMenu)
	menuButton(c, b, func(m *popMenu) {
		if build == nil {
			if !menuStateOf(b).focused {
				a.loadHostMenu()
			}
			build = a.hostMenuBuild(L(c))
		}
		build(m)
	})
}

// 同一帧的收集和执行共用快照，后台握手完成不能改变菜单项的编号。
func (a *app) hostMenuBuild(m locales.Messages) func(*popMenu) {
	var s hostSnapshot
	if a.host != nil {
		s = a.host.snapshot()
	}
	hosts := slices.Clone(a.windowOwner().hostMenu.hosts)
	connecting, releasing := a.connecting, a.hostMenu.releasing != 0
	return func(menu *popMenu) {
		if menu.Item(m.HostLocal()).Checked(a.viewID == localHostID).Chosen() {
			a.selectLocalHost()
		}
		for _, h := range hosts {
			if menu.Item(h.Name).Checked(a.viewID == h.ID).Chosen() {
				a.selectRemoteHost(h)
			}
		}
		if a.host == nil {
			return
		}
		menu.Separator()
		if a.viewID != localHostID && !s.Connected {
			var selected remote.Host
			for _, h := range hosts {
				if h.ID == a.viewID {
					selected = h
				}
			}
			if menu.Item(m.HostConnect()).Disabled(connecting || selected.ID == "").Chosen() {
				a.selectRemoteHost(selected)
			}
		}
		if s.Controlled {
			if menu.Item(m.HostRelease()).Chosen() {
				a.releaseHost(s)
			}
		} else if menu.Item(m.HostTake()).Disabled(!s.Connected || connecting || releasing).Chosen() {
			a.takeHost(m, s)
		}
		if a.viewID != localHostID && menu.Item(m.HostDisconnect()).Disabled(!s.Connected && !connecting).Chosen() {
			a.windowOwner().hosts.disconnect(a.viewID)
		}
	}
}

func (a *app) selectLocalHost() {
	if a.views == nil {
		return
	}
	local := a.views.local
	if a.views.active.Load() == local {
		return
	}
	if local.host != nil {
		local.host.prepareLocalReturn()
	}
	a.views.activate(localHostID)
	local.openWorkspaces()
}

func (a *app) selectRemoteHost(h remote.Host) {
	k := a.windowOwner().hosts
	if k == nil || k.closed {
		return
	}
	if a.viewID == h.ID && (a.connecting || a.host != nil && a.host.snapshot().Connected) {
		return
	}
	if k.views.local.hostMenu.names == nil {
		k.views.local.hostMenu.names = make(map[string]string)
	}
	k.views.local.hostMenu.names[h.ID] = h.Name
	k.connectRemote(h, false)
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
		return m.HostConnecting()
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

func (a *app) hostStatusView(c *ui.Context) {
	if status := a.hostStatus(L(c)); status != "" {
		ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, trafficLightX, 0, max(trafficLightX, a.barInset(c))).AlignItems(ui.Center).Children(func() {
			ui.Text(c, status).FontSize(fsSmall).TextColor(c.Theme().TextMuted).SingleLine().Ellipsis("…")
		})
	}
}
