package app

import (
	"context"
	"errors"
	"slices"
	"sync"

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
	app    *app
	ctx    context.Context
	cancel context.CancelFunc
}

// 控制器的选择状态只在界面线程修改；后台只持有单次请求与固定 Client。
// dialMu 包住传输握手及 Info 的复制，取消后的旧建连不能排到新建连之后再操作缓存。
type hostTransportCleanup interface {
	Park()
	Disconnect(string)
	Close()
}

type hostConnections struct {
	views   *hostViews
	ctx     context.Context
	cancel  context.CancelFunc
	pool    hostTransportCleanup
	closing map[string]uint64
	active  *hostAttempt
	closed  bool
	jobs    sync.WaitGroup
	dialMu  sync.Mutex
	dial    func(context.Context, remote.Host, remote.Options) (hostTransport, error)
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
		return hostTransport{client: &client.Client{Home: s.Info.Home, Socket: s.Socket, Remote: true}, done: s.Done()}, nil
	}
	return k
}

func (a *app) windowContext() context.Context {
	if k := a.windowOwner().hosts; k != nil {
		return k.ctx
	}
	return context.Background()
}

// connectRemote 由主机入口在界面线程调用；连接成功只获得查看身份，不接管。
func (k *hostConnections) connectRemote(h remote.Host, start bool) {
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
	a.connecting, a.err = true, ""
	ctx, cancel := context.WithCancel(k.ctx)
	r := &hostAttempt{app: a, ctx: ctx, cancel: cancel}
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
			err = a.host.connectAt(ctx, transport.client, generation)
		}
		if err == nil {
			err = ctx.Err()
		}
		a.update(func() {
			if k.closed || k.active != r {
				return
			}
			a.connecting = false
			if ctx.Err() != nil {
				k.failed(r, ctx.Err())
				return
			}
			if err != nil {
				k.failed(r, err)
				return
			}
			a.pollForce.Store(true)
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

func (k *hostConnections) disconnect(id string) {
	if k.closed {
		return
	}
	if k.active != nil && k.active.app.viewID == id {
		k.failed(k.active, context.Canceled)
		return
	}
	if _, pending := k.closing[id]; !pending {
		k.pool.Disconnect(id)
	}
}

// heartbeat 不经 appRequest.update：连接失效后仍须发布断线状态；回到界面线程再核对代次。
func (k *hostConnections) heartbeat(a *app) bool {
	if a.host == nil {
		return true
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
			if k.closed || k.active == nil || k.active.app != a || a.host.snapshot().Generation != before.Generation {
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
