package remote

import (
	"context"
	"errors"
	"sync"
	"time"

	"posse/internal/rpc"
)

const (
	idleConnectionLimit = 3
	idleConnectionTTL   = 5 * time.Minute
)

// Connections 管理一个窗口的 SSH 传输：一次使用一台主机，离开的传输有界保留。
// 调用方先撤掉桌面身份和终端订阅，再 Park/Open；复用传输不恢复桌面控制权。
// 它拥有返回的 Session，调用方通过 Disconnect/Close 关闭，不自行关闭缓存中的 Session。
// Info 随每次成功握手更新，调用方应在下次 Open 前复制它用于异步工作。
type Connections struct {
	mu           sync.Mutex
	closed       bool
	active       *Session
	activeConfig string
	pending      *connectionAttempt
	idle         map[string]*idleConnection
	pins         map[*Session]int
	pendingClose map[*Session]bool
	work         sync.WaitGroup
	limit        int
	ttl          time.Duration
	dial         func(context.Context, Host, Options) (*Session, error)
	check        func(context.Context, *Session, Options) error
}

type connectionAttempt struct {
	host   Host
	cancel context.CancelFunc
}

type idleConnection struct {
	session *Session
	config  string
	at      time.Time
	timer   *time.Timer
}

// NewConnections 最多保留三台空闲主机，每台空闲五分钟后关闭。
// Session 不保留认证回调，密码和口令不会进入连接缓存。
func NewConnections() *Connections {
	return &Connections{
		idle:         make(map[string]*idleConnection),
		pins:         make(map[*Session]int),
		pendingClose: make(map[*Session]bool),
		limit:        idleConnectionLimit,
		ttl:          idleConnectionTTL,
		dial:         Connect,
		check: func(ctx context.Context, s *Session, opts Options) error {
			// 探测、发布快照与自动升级决策置于同一升级互斥内，防止复用握手把旧快照写覆盖到正在进行的手动热升级。
			if !s.tryLockUpgrade() {
				return rpc.Errorf(rpc.CodeBusy, "remote server upgrade is in progress")
			}
			defer s.unlockUpgrade()
			var info rpc.RemoteInfo
			if err := rpc.Call(ctx, s.Socket, "daemon.info", nil, &info); err != nil {
				return err
			}
			if err := validateInfo(s.Probe, info, opts.Required); err != nil {
				return err
			}
			s.setInfo(info)
			if err := s.maybeUpgradeHeadlessLocked(ctx, s.Host, opts); err != nil {
				return err
			}
			return nil
		},
	}
}

// Open 取消上次未完成的建连。返回前核对尝试身份；取消后迟到的成功连接只会被回收。
// ctx 只限制这次建连或复用握手，成功后的传输直到 Park/Disconnect/Close 才改变归属。
func (p *Connections) Open(ctx context.Context, h Host, opts Options) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.ID == "" {
		return nil, errors.New("host ID required")
	}
	if err := h.Validate(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, context.Canceled
	}
	p.cancelLocked()
	// 先取走本次目标，再停放当前连接，满缓存时不会淘汰正要使用的 master。
	entry := p.idle[h.ID]
	if entry != nil {
		delete(p.idle, h.ID)
		entry.timer.Stop()
	} else if p.active != nil && p.active.Host.ID == h.ID {
		entry = &idleConnection{session: p.active, config: p.activeConfig}
		p.active = nil
	}
	p.parkLocked()
	ctx, cancel := context.WithCancel(ctx)
	attempt := &connectionAttempt{host: h, cancel: cancel}
	p.pending = attempt
	p.work.Add(1)
	var s *Session
	if entry != nil {
		// 正在执行热升级（被 pin）的会话禁止并发复用探测或重拨第二条 SSH master，保留在原位并明确返回 busy。
		if p.pins[entry.session] > 0 {
			if p.pending == attempt {
				p.pending = nil
			}
			p.work.Done()
			cancel()
			if sessionAlive(entry.session) {
				p.idle[h.ID] = entry
				if p.ttl > 0 {
					entry.timer = time.AfterFunc(p.ttl, func() {
						p.mu.Lock()
						defer p.mu.Unlock()
						if p.idle[h.ID] == entry {
							delete(p.idle, h.ID)
							p.closeLocked(entry.session)
						}
					})
				}
			} else {
				p.closeLocked(entry.session)
			}
			p.mu.Unlock()
			return nil, rpc.Errorf(rpc.CodeBusy, "remote server upgrade is in progress")
		}
		if entry.session.Host.SameConnection(h) && entry.config == opts.config && sessionAlive(entry.session) {
			// Session.Host 作为不可变传输身份不再原地覆写，彻底避免与并发操作产生数据竞态。
			s = entry.session
		} else {
			p.closeLocked(entry.session)
		}
	}
	p.mu.Unlock()
	defer p.work.Done()
	defer cancel()

	fresh := s == nil
	var err error
	if fresh {
		s, err = p.dial(ctx, h, opts)
	} else {
		err = p.check(ctx, s, opts)
		// 复用空闲 master 时若远端 daemon 已停或版本能力不足且允许启动/自动安装，丢弃旧隧道后走一次完整建连
		if err != nil && (opts.Start || opts.AutoInstall) && ctx.Err() == nil {
			p.mu.Lock()
			p.closeLocked(s)
			p.mu.Unlock()
			s, err = p.dial(ctx, h, opts)
			fresh = true
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.pending != attempt || ctx.Err() != nil {
		if p.pending == attempt {
			p.pending = nil
		}
		if s != nil {
			p.closeLocked(s)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, context.Canceled
	}
	p.pending = nil
	if err != nil {
		if s != nil {
			p.closeLocked(s)
		}
		return nil, err
	}
	if s == nil || !sessionAlive(s) {
		if s != nil {
			p.closeLocked(s)
		}
		return nil, errors.New("SSH connection closed")
	}
	p.active = s
	// config 只供包内隔离 SSH 测试；正常连接沿用系统 SSH config。
	p.activeConfig = opts.config
	if fresh {
		p.work.Add(1)
		go p.watch(s)
	}
	return s, nil
}

func sessionAlive(s *Session) bool {
	select {
	case <-s.Done():
		return false
	default:
		return true
	}
}

func (p *Connections) cancelLocked() {
	if p.pending != nil {
		p.pending.cancel()
		p.pending = nil
	}
}

// Park 用于离开主机：取消认证和建连，仅将已经连好的传输放入空闲缓存。
func (p *Connections) Park() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelLocked()
	p.parkLocked()
}

func (p *Connections) parkLocked() {
	s := p.active
	p.active = nil
	if s == nil {
		return
	}
	if p.closed || p.limit <= 0 || p.ttl <= 0 || !sessionAlive(s) {
		p.closeLocked(s)
		return
	}
	if old := p.idle[s.Host.ID]; old != nil {
		old.timer.Stop()
		p.closeLocked(old.session)
	}
	entry := &idleConnection{session: s, config: p.activeConfig, at: time.Now()}
	p.idle[s.Host.ID] = entry
	entry.timer = time.AfterFunc(p.ttl, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.idle[s.Host.ID] == entry {
			delete(p.idle, s.Host.ID)
			p.closeLocked(s)
		}
	})
	for len(p.idle) > p.limit {
		var oldest *idleConnection
		// 优先淘汰未 pin 的空闲隧道，避免误标记正在热升级的会话。
		for _, e := range p.idle {
			if p.pins[e.session] > 0 {
				continue
			}
			if oldest == nil || e.at.Before(oldest.at) {
				oldest = e
			}
		}
		if oldest == nil {
			for _, e := range p.idle {
				if oldest == nil || e.at.Before(oldest.at) {
					oldest = e
				}
			}
		}
		delete(p.idle, oldest.session.Host.ID)
		oldest.timer.Stop()
		p.closeLocked(oldest.session)
	}
}

func (p *Connections) watch(s *Session) {
	defer p.work.Done()
	<-s.Done()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == s {
		p.active = nil
	}
	if entry := p.idle[s.Host.ID]; entry != nil && entry.session == s {
		entry.timer.Stop()
		delete(p.idle, s.Host.ID)
	}
	p.closeLocked(s)
}

// Connected 报告指定主机当前是否有存活的活动或保活 SSH 隧道。
func (p *Connections) Connected(id string) bool {
	if p == nil || id == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if p.active != nil && p.active.Host.ID == id && sessionAlive(p.active) {
		return true
	}
	if entry := p.idle[id]; entry != nil && sessionAlive(entry.session) {
		return true
	}
	return false
}

// Disconnect 只回收指定主机，不忘记配置，也不结束目标 daemon 或终端。
func (p *Connections) Disconnect(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending != nil && p.pending.host.ID == id {
		p.cancelLocked()
	}
	if p.active != nil && p.active.Host.ID == id {
		p.closeLocked(p.active)
		p.active = nil
	}
	if entry := p.idle[id]; entry != nil {
		entry.timer.Stop()
		delete(p.idle, id)
		p.closeLocked(entry.session)
	}
}

func (p *Connections) pinLocked(s *Session) {
	if p.pins == nil {
		p.pins = make(map[*Session]int)
	}
	p.pins[s]++
}

func (p *Connections) unpin(s *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins[s] > 1 {
		p.pins[s]--
		return
	}
	delete(p.pins, s)
	if p.pendingClose[s] || p.closed {
		delete(p.pendingClose, s)
		p.work.Add(1)
		go func() { defer p.work.Done(); s.Close() }()
	}
}

// UpgradeActive 对指定主机当前存活的活动或保活 SSH 隧道执行托管无头服务端热升级。
// 升级期间 pin 住 Session（Park/TTL/淘汰仅标待关闭，解钉后再关），并与自动升级检查共用 s.upgradeMu，
// 不占用窗口建连锁 dialMu，也不影响切换其他主机。
func (p *Connections) UpgradeActive(ctx context.Context, hostID string, opts Options) (rpc.RemoteInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return rpc.RemoteInfo{}, false, err
	}
	if hostID == "" {
		return rpc.RemoteInfo{}, false, errors.New("host ID required")
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return rpc.RemoteInfo{}, false, context.Canceled
	}
	var s *Session
	if p.active != nil && p.active.Host.ID == hostID && sessionAlive(p.active) {
		s = p.active
	} else if entry := p.idle[hostID]; entry != nil && sessionAlive(entry.session) {
		s = entry.session
	}
	if s == nil {
		p.mu.Unlock()
		return rpc.RemoteInfo{}, false, errors.New("remote host is not connected")
	}
	if s.Host.Executable != "" {
		p.mu.Unlock()
		return rpc.RemoteInfo{}, false, errors.New("server upgrade is disabled when custom executable path is configured")
	}
	p.pinLocked(s)
	p.work.Add(1)
	p.mu.Unlock()
	defer func() {
		p.unpin(s)
		p.work.Done()
	}()

	if !s.tryLockUpgrade() {
		return rpc.RemoteInfo{}, false, rpc.Errorf(rpc.CodeBusy, "remote server upgrade is in progress")
	}
	defer s.unlockUpgrade()
	if err := ctx.Err(); err != nil {
		return rpc.RemoteInfo{}, false, err
	}

	var live rpc.RemoteInfo
	infoCtx, infoCancel := context.WithTimeout(ctx, 5*time.Second)
	err := rpc.Call(infoCtx, s.Socket, "daemon.info", nil, &live)
	infoCancel()
	if err != nil {
		return rpc.RemoteInfo{}, false, err
	}
	if !isManagedHeadless(live) {
		return rpc.RemoteInfo{}, false, errors.New("server upgrade is only supported for managed headless nodes")
	}
	s.setInfo(live)

	opts.report(StagePackage, 0)
	localOpts := opts
	localOpts.DisableRemoteDownload = true
	pkg, err := localOpts.resolveServer(ctx, live.Platform, live.Arch)
	if err != nil {
		if ctx.Err() != nil {
			return rpc.RemoteInfo{}, false, ctx.Err()
		}
		if opts.ServerArchive != "" || opts.DisableRemoteDownload {
			return rpc.RemoteInfo{}, false, err
		}
		pkg, err = opts.resolveServer(ctx, live.Platform, live.Arch)
		if err != nil {
			return rpc.RemoteInfo{}, false, err
		}
	}
	defer pkg.Close()

	// 真正上传前对最终实际包再次核验单调向前约束。
	if !shouldAutoUpgradeHeadless(live, pkg.Version, pkg.Commit, pkg.BuiltAt) {
		return live, false, ErrServerUpToDate
	}

	newInfo, handedOff, err := s.upgradeHeadless(ctx, opts, pkg)
	if handedOff {
		s.setInfo(newInfo)
	}
	return newInfo, handedOff, err
}

func (p *Connections) closeLocked(s *Session) {
	if p.pins[s] > 0 {
		if p.pendingClose == nil {
			p.pendingClose = make(map[*Session]bool)
		}
		p.pendingClose[s] = true
		return
	}
	delete(p.pendingClose, s)
	p.work.Add(1)
	go func() { defer p.work.Done(); s.Close() }()
}

// Close 等待在途建连、认证取消和自有 SSH 进程退出，窗口应在后台调用。
func (p *Connections) Close() {
	p.mu.Lock()
	p.closed = true
	p.cancelLocked()
	if p.active != nil {
		p.closeLocked(p.active)
		p.active = nil
	}
	for id, entry := range p.idle {
		entry.timer.Stop()
		delete(p.idle, id)
		p.closeLocked(entry.session)
	}
	p.mu.Unlock()
	p.work.Wait()
}
