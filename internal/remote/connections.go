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
		idle: make(map[string]*idleConnection), limit: idleConnectionLimit, ttl: idleConnectionTTL,
		dial: Connect,
		check: func(ctx context.Context, s *Session, opts Options) error {
			var info rpc.RemoteInfo
			if err := rpc.Call(ctx, s.Socket, "daemon.info", nil, &info); err != nil {
				return err
			}
			if err := validateInfo(s.Probe, info, opts.Required); err != nil {
				return err
			}
			s.Info = info
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
		if entry.session.Host == h && entry.config == opts.config && sessionAlive(entry.session) {
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
		for _, e := range p.idle {
			if oldest == nil || e.at.Before(oldest.at) {
				oldest = e
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

func (p *Connections) closeLocked(s *Session) {
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
