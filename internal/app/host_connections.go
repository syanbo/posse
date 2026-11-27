package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"posse/internal/client"
	"posse/internal/errtext"
	"posse/internal/remote"
	"posse/internal/rpc"
)

type hostTransport struct {
	client *client.Client
	done   <-chan struct{}
}

type hostAttempt struct {
	app     *app
	ctx     context.Context
	cancel  context.CancelFunc
	onReady func(*app)
}

// 控制器的选择状态只在界面线程修改；后台只持有单次请求与固定 Client。
// dialMu 包住传输握手及 Info 的复制，取消后的旧建连不能排到新建连之后再操作缓存。
type hostTransportCleanup interface {
	Park()
	Disconnect(string)
	Close()
}

type hostConnections struct {
	views        *hostViews
	ctx          context.Context
	cancel       context.CancelFunc
	pool         hostTransportCleanup
	closing      map[string]uint64
	active       *hostAttempt
	closed       bool
	jobs         sync.WaitGroup
	dialMu       sync.Mutex
	dial         func(context.Context, remote.Host, remote.Options) (hostTransport, error)
	checkUpgrade func(context.Context, rpc.RemoteInfo, remote.Options) (remote.ServerUpgradeCheck, error)
	upgrade      func(context.Context, string, remote.Options) (rpc.RemoteInfo, bool, error)
}

func newHostConnections(v *hostViews) *hostConnections {
	ctx, cancel := context.WithCancel(context.Background())
	pool := remote.NewConnections()
	k := &hostConnections{views: v, ctx: ctx, cancel: cancel, pool: pool, closing: make(map[string]uint64)}
	k.dial = func(ctx context.Context, h remote.Host, opts remote.Options) (hostTransport, error) {
		s, err := pool.Open(ctx, h, opts)
		if err != nil {
			return hostTransport{}, err
		}
		info := s.CurrentInfo()
		return hostTransport{client: &client.Client{Home: info.Home, UserHome: info.UserHome, Platform: info.Platform, Socket: s.Socket, Remote: true}, done: s.Done()}, nil
	}
	k.checkUpgrade = remote.CheckServerUpgrade
	k.upgrade = pool.UpgradeActive
	return k
}

func (a *app) windowContext() context.Context {
	if k := a.windowOwner().hosts; k != nil {
		return k.ctx
	}
	return context.Background()
}

// connectRemote 由主机入口在界面线程调用；连上无人控制的远端自动接管，有人控制时进入查看。
func (k *hostConnections) connectRemote(h remote.Host, start bool) {
	k.connectRemoteReady(h, start, nil)
}

func (k *hostConnections) connectRemoteReady(h remote.Host, start bool, onReady func(*app)) {
	if k.closed {
		return
	}
	if h.ID == "" || h.ID == localHostID {
		k.views.active.Load().err = errtext.Text(errors.New("invalid remote host ID"))
		return
	}
	if err := h.Validate(); err != nil {
		k.views.active.Load().err = errtext.Text(err)
		return
	}
	a := k.views.forHost(newHostSession(h.ID, appName()), &client.Client{Remote: true})
	k.views.activate(h.ID)
	k.leave()
	k.release(a.host.detach())
	a.viewGeneration.Add(1)
	a.closeTargetPreviews()
	a.ensurePanes([maxColumns]string{})
	a.detachSkillRun()
	initialStage := remote.StageAuth
	if k.poolConnected(h.ID) {
		initialStage = remote.StageHandshake
	}
	k.views.local.setHostManuallyDisconnected(h.ID, false)
	a.connecting, a.connectStage, a.connectPct, a.err = true, initialStage, 0, ""
	ctx, cancel := context.WithCancel(k.ctx)
	r := &hostAttempt{app: a, ctx: ctx, cancel: cancel, onReady: onReady}
	k.active = r
	generation := a.host.snapshot().Generation
	opts := remote.Options{
		Start:       start,
		AutoInstall: start,
		LocalHome:   k.views.local.home,
		Required:    slices.Clone(rpc.RemoteCapabilities),
		Prompt: func(ctx context.Context, p remote.Prompt) (string, error) {
			return k.views.local.sshAuth.ask(ctx, h.Name, p, cancel)
		},
		Progress: func(stage remote.Stage, pct float64) {
			a.update(func() {
				if k.closed || k.active != r || ctx.Err() != nil {
					return
				}
				a.connectStage, a.connectPct = stage, pct
			})
		},
	}
	k.jobs.Go(func() {
		k.dialMu.Lock()
		var transport hostTransport
		err := ctx.Err()
		if err == nil {
			transport, err = k.dial(ctx, h, opts)
		}
		k.dialMu.Unlock()
		if err == nil {
			err = a.host.connectRemoteAt(ctx, transport.client, generation)
		}
		if err == nil {
			err = ctx.Err()
		}
		a.update(func() {
			if k.closed || k.active != r {
				return
			}
			a.connecting, a.connectStage, a.connectPct = false, "", 0
			if ctx.Err() != nil {
				k.failed(r, ctx.Err())
				return
			}
			if err != nil {
				k.failed(r, err)
				return
			}
			if transport.client != nil && transport.client.Platform != "" {
				a.platform = transport.client.Platform
			}
			a.pollForce.Store(true)
			a.wakePoll()
			if r.onReady != nil && a.displayed() == a && a.host.snapshot().Connected {
				r.onReady(a)
			}
		})
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
		case <-transport.done:
			a.update(func() { k.failed(r, errors.New("SSH connection closed")) })
		}
	})
}

// leave 在切显示主机前调用，不能等后台网络返回才使旧认证失效。
func (k *hostConnections) leave() {
	if k.active != nil {
		k.active.cancel()
		k.active.app.connecting = false
		k.active.app.connectStage = ""
		k.active.app.connectPct = 0
		k.active = nil
	}
	k.views.local.sshAuth.cancelPending(false)
	k.pool.Park()
}

func (k *hostConnections) release(l hostLease) {
	if l.client != nil {
		k.jobs.Go(func() { _ = l.release(context.Background()) })
	}
}

func (k *hostConnections) failed(r *hostAttempt, err error) {
	if k.closed || k.active != r {
		return
	}
	r.cancel()
	k.active = nil
	a := r.app
	lease := a.host.detach()
	generation := a.host.snapshot().Generation
	a.viewGeneration.Add(1)
	a.closeTargetPreviews()
	a.connecting = false
	a.ensurePanes([maxColumns]string{})
	a.detachSkillRun()
	a.closeImages()
	if lease.client == nil {
		k.pool.Disconnect(a.viewID)
	} else {
		k.closing[a.viewID] = generation
		k.jobs.Go(func() {
			_ = lease.release(context.Background())
			a.update(func() {
				if k.closing[a.viewID] != generation {
					return
				}
				delete(k.closing, a.viewID)
				// 同一主机可能已重新连接，旧清理不能关闭它的新传输。
				if !k.closed && a.host.snapshot().Generation == generation {
					k.pool.Disconnect(a.viewID)
				}
			})
		})
	}
	if !errors.Is(err, context.Canceled) {
		a.err = errtext.Text(err)
	}
}

func (k *hostConnections) poolConnected(id string) bool {
	if k == nil || k.closed {
		return false
	}
	if p, ok := k.pool.(interface{ Connected(string) bool }); ok {
		return p.Connected(id)
	}
	return false
}

func (k *hostConnections) disconnect(id string) {
	if k.closed {
		return
	}
	if v := k.views.all[id]; v != nil && v.upgrading.Load() {
		return
	}
	if k.views.suspendedRemote != nil && k.views.suspendedRemote.id == id {
		k.views.suspendedRemote = nil
	}
	if k.active != nil && k.active.app.viewID == id {
		// 主动断开已建立的会话持久化记入本机 ui.json，切走或重启后再点磁贴也不自动重连；
		// 取消尚未连上的拨号不阻断再次点击重试。
		k.views.local.setHostManuallyDisconnected(id, !k.active.app.connecting)
		k.failed(k.active, context.Canceled)
		return
	}
	k.views.local.setHostManuallyDisconnected(id, true)
	if _, pending := k.closing[id]; !pending {
		k.pool.Disconnect(id)
	}
}

func upgradeCheckIdentity(info rpc.RemoteInfo) string {
	return info.InstanceID + "|" + info.Version + "|" + info.Build + "|" + info.ServerTag
}

// checkHostUpgrade 在后台对托管无头节点执行轻量更新检查（只验本地包或签名的 R2 清单，不下载压缩包）。
func (k *hostConnections) checkHostUpgrade(hostID string, info rpc.RemoteInfo) {
	if k.closed || hostID == "" || hostID == localHostID {
		return
	}
	a := k.views.all[hostID]
	if a == nil || a.upgrading.Load() || a.upgradeChecking {
		return
	}
	checkFn := k.checkUpgrade
	if checkFn == nil {
		checkFn = remote.CheckServerUpgrade
	}
	a.upgradeCheckGen++
	gen := a.upgradeCheckGen
	targetKey := upgradeCheckIdentity(info)
	a.upgradeChecking = true
	a.upgradeCheckErr = ""
	a.upgradeErr = ""
	a.upgradeNotice = ""
	opts := remote.Options{
		LocalHome: k.views.local.home,
		Required:  slices.Clone(rpc.RemoteCapabilities),
	}
	k.jobs.Go(func() {
		ctx, cancel := context.WithTimeout(k.ctx, 20*time.Second)
		res, err := checkFn(ctx, info, opts)
		cancel()
		a.update(func() {
			if k.closed || a.upgradeCheckGen != gen {
				return
			}
			a.upgradeChecking = false
			if err != nil {
				a.upgradeCheck = nil
				a.upgradeCheckKey = ""
				a.upgradeCheckErr = errtext.Text(err)
				return
			}
			cp := res
			a.upgradeCheck = &cp
			a.upgradeCheckKey = targetKey
			a.upgradeCheckErr = ""
		})
	})
}

// cancelHostUpgrade 仅在热升级尚未进入 daemon.handoff（上传与提交阶段）时取消升级。
func (k *hostConnections) cancelHostUpgrade(hostID string) {
	if k.closed || hostID == "" || hostID == localHostID {
		return
	}
	a := k.views.all[hostID]
	if a == nil || !a.upgrading.Load() || a.upgradeHandingOff.Load() || a.upgradeCancel == nil {
		return
	}
	a.upgradeCancel()
	a.upgradeCancel = nil
}

// upgradeRemote 对当前活动且受控的远端托管无头主机执行无损热升级：
// 1. 打包与上传阶段保持心跳续租不丢桌面控制权；进入 daemon.handoff 前核对控制权未转移并切换 upgradeHandingOff 抑制瞬断；
// 2. 不占 dialMu（切换其他主机无阻塞），与在途自启写入互斥；
// 3. handoff 成功后在后台调用 connectRemoteAt 沿原隧道无感恢复桌面控制，不清空 panes 与标签；
// 4. 若升级期间切走又切回（hostSession 已 detach），无论是否交接，结束后若仍停留在当前主机视图均自动恢复连接。
func (k *hostConnections) upgradeRemote(hostID string) {
	if k.closed || hostID == "" || hostID == localHostID || k.upgrade == nil {
		return
	}
	a := k.views.all[hostID]
	if a == nil || k.views.active.Load() != a || a.host == nil || a.upgrading.Load() {
		return
	}
	snap := a.host.snapshot()
	if !snap.Connected || !snap.Controlled || snap.Client == nil {
		return
	}
	if a.autostartOp != nil && a.autostartOp.instanceID == snap.Desktop.InstanceID && a.autostartOp.busy {
		return
	}
	origHost, hasOrigHost := k.views.local.findSavedHost(hostID)
	gen := snap.Generation
	baseClient := *snap.Client
	baseClient.Desktop = nil
	ctx, cancel := context.WithCancel(k.ctx)

	a.upgrading.Store(true)
	a.upgradeHandingOff.Store(false)
	a.upgradeStage = remote.StagePackage
	a.upgradePct = 0
	a.upgradeErr = ""
	a.upgradeNotice = ""
	a.upgradeCheckErr = ""
	a.upgradeCancel = cancel

	opts := remote.Options{
		LocalHome: k.views.local.home,
		Required:  slices.Clone(rpc.RemoteCapabilities),
		BeforeHandoff: func(context.Context) error {
			cur := a.host.snapshot()
			if !cur.Connected || !cur.Controlled || cur.Generation != gen {
				return rpc.Errorf(rpc.CodeReadOnly, "desktop control was lost before handoff")
			}
			a.upgradeHandingOff.Store(true)
			return nil
		},
		Progress: func(stage remote.Stage, pct float64) {
			if stage == remote.StageUpgrade || stage == remote.StageActivate || stage == remote.StageHandshake {
				a.upgradeHandingOff.Store(true)
			}
			a.update(func() {
				if k.closed || !a.upgrading.Load() {
					return
				}
				a.upgradeStage, a.upgradePct = stage, pct
				if stage == remote.StageUpgrade || stage == remote.StageActivate || stage == remote.StageHandshake {
					a.upgradeCancel = nil
				}
			})
		},
	}

	k.jobs.Go(func() {
		newInfo, handedOff, err := k.upgrade(ctx, hostID, opts)
		cancel()

		if handedOff {
			a.upgradeHandingOff.Store(true)
			a.update(func() {
				if k.closed || !a.upgrading.Load() {
					return
				}
				a.upgradeStage, a.upgradePct = remote.StageHandshake, 0
				a.upgradeCancel = nil
			})
			newBase := baseClient
			if newInfo.Home != "" {
				newBase.Home = newInfo.Home
			}
			if newInfo.UserHome != "" {
				newBase.UserHome = newInfo.UserHome
			}
			if newInfo.Platform != "" {
				newBase.Platform = newInfo.Platform
			}
			newBase.Desktop = nil
			newBase.Remote = true
			reattachCtx, reattachCancel := context.WithTimeout(k.ctx, 15*time.Second)
			reattachErr := a.host.connectRemoteAt(reattachCtx, &newBase, gen)
			reattachCancel()
			if err == nil && reattachErr != nil && !errors.Is(reattachErr, context.Canceled) {
				err = reattachErr
			}
		}

		a.update(func() {
			a.upgrading.Store(false)
			a.upgradeHandingOff.Store(false)
			a.upgradeStage, a.upgradePct = "", 0
			a.upgradeCancel = nil
			if k.closed {
				return
			}
			restoreIfDetachedActive := func() {
				if k.views.active.Load() != a || a.host.snapshot().Connected || !hasOrigHost {
					return
				}
				if curSaved, ok := k.views.local.findSavedHost(hostID); ok && curSaved.SameConnection(origHost) {
					k.connectRemote(origHost, true)
				}
			}
			if handedOff {
				cp := newInfo
				a.remoteInfo = &cp
				a.upgradeCheck = nil
				a.upgradeCheckKey = ""
				a.upgradeCheckErr = ""
				if err != nil {
					a.upgradeErr = errtext.Text(err)
				} else {
					a.upgradeErr = ""
					a.upgradeNotice = appL().HostUpgradeDone(newInfo.Version)
				}
				a.pollForce.Store(true)
				a.wakePoll()
				if owner := k.views.local; owner.hostDlg.open && owner.hostDlg.editing.ID == hostID {
					owner.refreshHostDetails(owner, &owner.hostDlg)
				}
				restoreIfDetachedActive()
				return
			}
			if errors.Is(err, remote.ErrServerUpToDate) {
				a.upgradeNotice = appL().HostUpgradeUpToDate()
			} else if err != nil && !errors.Is(err, context.Canceled) {
				a.upgradeErr = errtext.Text(err)
			}
			// 切走又切回后取消或遇到 CodeBusy/上传失败时，统一恢复当前活动主机视图的连接。
			restoreIfDetachedActive()
		})
	})
}

// heartbeat 不经 appRequest.update：连接失效后仍须发布断线状态；回到界面线程再核对代次。
func (k *hostConnections) heartbeat(a *app) bool {
	if a.host == nil {
		return true
	}
	// 仅在真正进入 daemon.handoff 短窗口期间抑制心跳；打包/上传阶段继续续租防止 15 秒控制租约过期。
	if a.upgradeHandingOff.Load() {
		return false
	}
	before := a.host.snapshot()
	if !before.Connected {
		return false
	}
	if err := a.host.refresh(k.ctx); err != nil {
		a.update(func() {
			if a == k.views.local {
				if !k.closed && a.host.snapshot().Generation == before.Generation {
					a.err = errtext.Text(err)
				}
				return
			}
			if k.closed || a.upgradeHandingOff.Load() || k.active == nil || k.active.app != a || a.host.snapshot().Generation != before.Generation {
				return
			}
			k.failed(k.active, err)
		})
		return false
	}
	return true
}

// close 只在窗口真正结束后调用，此时不再接受界面动作；等待的后台工作不依赖界面队列。
func (k *hostConnections) close() {
	if k.closed {
		return
	}
	k.closed = true
	k.cancel()
	k.leave()
	k.views.local.sshAuth.cancelPending(true)
	for _, a := range k.views.all {
		a.closeTargetPreviews()
		if a.host != nil {
			k.release(a.host.detach())
		}
		a.ensurePanes([maxColumns]string{})
		a.detachSkillRun()
	}
	// 释放控制权还要经过隧道；先收拢这些请求，再回收 SSH。
	k.jobs.Wait()
	k.pool.Close()
}
