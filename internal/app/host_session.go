package app

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"posse/internal/client"
	"posse/internal/rpc"
)

// hostSession 只管理一个目标的连接和控制身份。窗口数据留在该主机自己的视图上下文中，
// 异步操作捕获 snapshot 的 Client；接管或重连只创建新 Client，不修改旧身份。
type hostSession struct {
	id           string
	viewerID     string
	controllerID string
	label        string

	mu                sync.Mutex
	generation        uint64
	base              *client.Client
	current           hostSnapshot
	automaticInstance string
	retryBusyGen      uint64
	blockedInstance   string
	returnLease       hostLease
	returnRequested   bool
	pendingRelease    hostLease

	// 网络操作串行，状态锁不跨网络；离开主机仍能立即使正在等待的结果失效。
	opMu sync.Mutex
}

type hostSnapshot struct {
	HostID     string
	Generation uint64
	Client     *client.Client
	Desktop    rpc.DesktopState
	Connected  bool
	Controlled bool
}

// hostLease 只释放它捕获的那一代控制权，迟到的释放不会影响后来接管的端。
type hostLease struct {
	client   *client.Client
	identity rpc.DesktopIdentity
}

func (l hostLease) release(ctx context.Context) error {
	if l.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return l.client.Call(ctx, "desktop.release", l.identity, nil)
}

func newHostSession(id, label string) *hostSession {
	return &hostSession{id: id, label: label, viewerID: rand.Text(), controllerID: rand.Text(), current: hostSnapshot{HostID: id}}
}

func (h *hostSession) snapshot() hostSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

// matches 供迟到的读结果、确认框和文档回调核对来源；Client 在控制代次变化时也会换掉。
func (h *hostSession) matches(s hostSnapshot) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current.Connected && s.HostID == h.id && s.Generation == h.generation && s.Client == h.current.Client
}

func (h *hostSession) leaseLocked() hostLease {
	if !h.current.Controlled || h.current.Client == nil {
		return hostLease{}
	}
	return hostLease{h.base, *h.current.Client.Desktop}
}

// detach 在界面线程立即失效，返回的租约由后台释放；不关闭 SSH，也不停止终端。
func (h *hostSession) detach() hostLease {
	h.mu.Lock()
	defer h.mu.Unlock()
	lease := h.leaseLocked()
	if lease.client == nil {
		lease = h.pendingRelease
	}
	h.pendingRelease = hostLease{}
	if lease.client != nil {
		h.returnLease = lease
	}
	h.returnRequested = false
	h.retryBusyGen = 0
	h.generation++
	h.base = nil
	h.current.Client = nil
	h.current.Connected, h.current.Controlled = false, false
	h.current.Generation = h.generation
	return lease
}

// connect 接收已经建立的本机连接或 SSH 隧道。本机每个 daemon 实例只自动申请一次；
// 用户主动发起的远端连接经 connectRemoteAt 传入 autoTakeRemote，无人控制且未主动释放时自动接管。
func (h *hostSession) connect(ctx context.Context, base *client.Client) error {
	return h.connectAt(ctx, base, h.snapshot().Generation)
}

// 发起 SSH 建连之前也可捕获 generation，排队和握手期间离开主机都会使它失效。
func (h *hostSession) connectAt(ctx context.Context, base *client.Client, expectedGeneration uint64) error {
	return h.connectAtMode(ctx, base, expectedGeneration, false)
}

// connectRemoteAt 用于用户主动发起的远端主机连接：若目标无人控制且未被本端主动释放过，则自动接管。
func (h *hostSession) connectRemoteAt(ctx context.Context, base *client.Client, expectedGeneration uint64) error {
	return h.connectAtMode(ctx, base, expectedGeneration, true)
}

func (h *hostSession) connectAtMode(ctx context.Context, base *client.Client, expectedGeneration uint64, autoTakeRemote bool) error {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	copy := *base
	copy.Desktop = nil
	h.mu.Lock()
	if h.generation != expectedGeneration {
		h.mu.Unlock()
		return context.Canceled
	}
	old := h.leaseLocked()
	returning := h.returnRequested && !copy.Remote
	h.returnRequested = false
	if returning {
		old = h.returnLease
	}
	h.returnLease = hostLease{}
	h.retryBusyGen = 0
	h.generation++
	h.base = &copy
	generation := h.generation
	h.current.Client = nil
	h.current.Connected, h.current.Controlled = false, false
	h.current.Generation = generation
	h.mu.Unlock()
	_ = old.release(ctx)

	var state rpc.DesktopState
	if err := hostControlCall(ctx, &copy, "desktop.status", nil, &state); err != nil {
		return err
	}
	return h.observe(ctx, generation, &copy, state, returning, autoTakeRemote)
}

// 只有成功读到状态才消耗该实例的自动接管机会；已有 owner 也消耗，防止稍后抢回。
func (h *hostSession) observe(ctx context.Context, generation uint64, base *client.Client, state rpc.DesktopState, returning, autoTakeRemote bool) error {
	if !h.publish(generation, base, state, false) {
		return context.Canceled
	}
	h.mu.Lock()
	if generation != h.generation || base != h.base {
		h.mu.Unlock()
		return context.Canceled
	}
	var automatic bool
	prevAutomatic := h.automaticInstance
	busyRetry := h.retryBusyGen != 0 && h.retryBusyGen == generation
	if !busyRetry || (state.Owner != "" && state.Owner != h.controllerID) {
		h.retryBusyGen = 0
		busyRetry = false
	}
	if state.InstanceID != "" && state.InstanceID != h.blockedInstance {
		if base.Remote {
			automatic = autoTakeRemote || busyRetry
		} else {
			automatic = state.InstanceID != h.automaticInstance || busyRetry || returning
		}
	}
	if automatic {
		h.automaticInstance = state.InstanceID
		h.retryBusyGen = 0
	}
	h.mu.Unlock()
	if automatic && (state.Owner == "" || state.Owner == h.controllerID) {
		err := h.takeLocked(ctx, generation, base, state, true)
		var re *rpc.Error
		if errors.As(err, &re) {
			switch re.Code {
			case rpc.CodeBusy:
				// 交接等瞬时忙碌不消耗自动接管机会，也不切断当前查看连接；交接完成后下次轮询自动接管。
				h.restoreBusyAutomatic(generation, base, state.InstanceID, prevAutomatic)
				return nil
			case rpc.CodeReadOnly:
				// 通道和查看身份已建立，目标当前只读时保留可用查看连接而不是断开主机
				return nil
			case rpc.CodeConflict:
				// 刚断开或切换时的旧 release 可能在 status 与 take 之间递增 epoch；重查一次后核对阻断状态再决定
				var fresh rpc.DesktopState
				if serr := hostControlCall(ctx, base, "desktop.status", nil, &fresh); serr == nil {
					if !h.publish(generation, base, fresh, false) {
						return context.Canceled
					}
					h.mu.Lock()
					if generation != h.generation || base != h.base {
						h.mu.Unlock()
						return context.Canceled
					}
					freshPrevAutomatic := prevAutomatic
					if fresh.InstanceID != state.InstanceID {
						freshPrevAutomatic = h.automaticInstance
					}
					blocked := fresh.InstanceID == "" || fresh.InstanceID == h.blockedInstance
					if fresh.InstanceID != "" {
						h.automaticInstance = fresh.InstanceID
					}
					h.mu.Unlock()
					if !blocked && (fresh.Owner == "" || fresh.Owner == h.controllerID) {
						rerr := h.takeLocked(ctx, generation, base, fresh, true)
						var re2 *rpc.Error
						if errors.As(rerr, &re2) {
							if re2.Code == rpc.CodeBusy {
								h.restoreBusyAutomatic(generation, base, fresh.InstanceID, freshPrevAutomatic)
								return nil
							}
							if re2.Code == rpc.CodeConflict || re2.Code == rpc.CodeReadOnly {
								return nil
							}
						}
						return rerr
					}
					return nil
				}
			}
		}
		return err
	}
	return nil
}

func (h *hostSession) restoreBusyAutomatic(generation uint64, base *client.Client, instance, prevAutomatic string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation == generation && h.base == base && h.current.Desktop.InstanceID == instance && instance != h.blockedInstance {
		h.retryBusyGen = generation
		if h.automaticInstance == instance {
			h.automaticInstance = prevAutomatic
		}
	}
}

// 切回本机单独授权一次自动申请；主动释放、心跳失效和接管结果未知的实例仍保持查看。
func (h *hostSession) prepareLocalReturn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.id == localHostID && !h.current.Connected && h.current.Desktop.InstanceID != "" && h.current.Desktop.InstanceID != h.blockedInstance {
		h.returnRequested = true
	}
}

// yield 立即换成查看身份，释放请求使用捕获的旧租约，不影响后来接管的代次。
func (h *hostSession) yield(expected hostSnapshot) hostLease {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.current.Controlled || expected.HostID != h.id || expected.Generation != h.generation || expected.Client != h.current.Client {
		return hostLease{}
	}
	lease := h.leaseLocked()
	h.pendingRelease = lease
	h.returnLease, h.returnRequested = hostLease{}, false
	h.automaticInstance = h.current.Desktop.InstanceID
	h.retryBusyGen = 0
	h.blockedInstance = h.current.Desktop.InstanceID
	h.generation++
	h.current.Generation = h.generation
	h.current.Controlled = false
	h.current.Client = h.base.WithDesktop(h.current.Desktop.Identity(h.viewerID))
	return lease
}

func (h *hostSession) released(lease hostLease) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pendingRelease == lease {
		h.pendingRelease = hostLease{}
	}
}

func hostControlCall(ctx context.Context, c *client.Client, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Call(ctx, method, params, out)
}

// publish 仅接受仍属于当前连接的结果，心跳更新不替换同一身份的 Client。
func (h *hostSession) publish(generation uint64, base *client.Client, state rpc.DesktopState, controlled bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if generation != h.generation || base != h.base {
		return false
	}
	id := state.Identity(h.viewerID)
	if controlled {
		id = state.Identity(h.controllerID)
	}
	c := h.current.Client
	if c == nil || c.Desktop == nil || *c.Desktop != id {
		c = base.WithDesktop(id)
	}
	h.current = hostSnapshot{HostID: h.id, Generation: generation, Client: c, Desktop: state, Connected: true, Controlled: controlled}
	return true
}

func (h *hostSession) take(ctx context.Context, expected hostSnapshot) error {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	h.mu.Lock()
	if !h.current.Connected || expected.HostID != h.id || expected.Generation != h.generation || expected.Client != h.current.Client {
		h.mu.Unlock()
		return rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
	}
	base := h.base
	h.mu.Unlock()
	return h.takeLocked(ctx, expected.Generation, base, expected.Desktop, false)
}

func (h *hostSession) takeLocked(ctx context.Context, generation uint64, base *client.Client, expected rpc.DesktopState, automatic bool) error {
	request := rpc.DesktopRequest{InstanceID: expected.InstanceID, ClientID: h.controllerID, Label: h.label, Epoch: expected.Epoch, Automatic: automatic}
	var state rpc.DesktopState
	if err := hostControlCall(ctx, base, "desktop.take", request, &state); err != nil {
		var response *rpc.Error
		if !errors.As(err, &response) {
			// 操作锁保证后续接管尚未开始；已经切走也要记住结果未知，不能借切回重发。
			h.mu.Lock()
			if h.current.Desktop.InstanceID == expected.InstanceID {
				h.blockedInstance = expected.InstanceID
				h.retryBusyGen = 0
			}
			h.mu.Unlock()
			h.publish(generation, base, expected, false)
			h.reconcileTake(base, expected)
		}
		return err
	}
	controlled := state.Owner == h.controllerID
	if !h.publish(generation, base, state, controlled) {
		if controlled {
			// 接管在服务端已接受，切主机不能把这份迟到的控制权遗留到租期结束。
			_ = (hostLease{base, state.Identity(h.controllerID)}).release(context.Background())
		}
		return context.Canceled
	}
	if controlled {
		h.mu.Lock()
		if h.generation == generation && h.base == base {
			h.blockedInstance = ""
			h.retryBusyGen = 0
		}
		h.mu.Unlock()
	}
	return nil
}

func (h *hostSession) blockAutomaticReturn(generation uint64, base *client.Client, instance string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation == generation && h.base == base {
		h.blockedInstance = instance
		h.retryBusyGen = 0
	}
}

// 接管回复丢失只核对状态，不重发接管。操作锁仍在，后续本端接管不会被这次清理释放；
// 他端的 owner、旧代租约或新 daemon 都不碰，网络仍不可达时只能等服务端租期结束。
func (h *hostSession) reconcileTake(base *client.Client, expected rpc.DesktopState) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var state rpc.DesktopState
	if err := hostControlCall(ctx, base, "desktop.status", nil, &state); err != nil {
		return
	}
	if state.InstanceID == expected.InstanceID && state.Owner == h.controllerID && state.Epoch > expected.Epoch {
		_ = (hostLease{base, state.Identity(h.controllerID)}).release(ctx)
	}
}

// refresh 由当前可见主机的轮询调用。控制端保活，查看端只查状态；
// 丢失控制后只降为查看，即使服务端显示 owner 是自己也不自动恢复写资格。
func (h *hostSession) refresh(ctx context.Context) error {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	h.mu.Lock()
	before, base := h.current, h.base
	h.mu.Unlock()
	if base == nil || !before.Connected {
		return context.Canceled
	}
	var state rpc.DesktopState
	if before.Controlled {
		err := hostControlCall(ctx, base, "desktop.heartbeat", before.Client.Desktop, &state)
		if err == nil {
			if !h.publish(before.Generation, base, state, state.Owner == h.controllerID && state.InstanceID == before.Desktop.InstanceID && state.Epoch == before.Desktop.Epoch) {
				return context.Canceled
			}
			return nil
		}
		// 心跳失败立即撤掉本地写资格；后续查询成功也不会恢复它。
		h.blockAutomaticReturn(before.Generation, base, before.Desktop.InstanceID)
		h.publish(before.Generation, base, before.Desktop, false)
	}
	if err := hostControlCall(ctx, base, "desktop.status", nil, &state); err != nil {
		h.mu.Lock()
		if h.generation == before.Generation && h.base == base {
			h.current.Client = nil
			h.current.Connected, h.current.Controlled = false, false
		}
		h.mu.Unlock()
		return err
	}
	return h.observe(ctx, before.Generation, base, state, false, false)
}
