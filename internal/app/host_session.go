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

	mu             sync.Mutex
	generation     uint64
	base           *client.Client
	current        hostSnapshot
	automaticTried bool

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
	h.generation++
	h.base = nil
	h.current.Client = nil
	h.current.Connected, h.current.Controlled = false, false
	h.current.Generation = h.generation
	return lease
}

// connect 接收已经建立的本机连接或 SSH 隧道。仅窗口第一次连接本机时自动申请一次；
// 重新连接、切回主机和远端连接都从查看开始。
func (h *hostSession) connect(ctx context.Context, base *client.Client) error {
	return h.connectAt(ctx, base, h.snapshot().Generation)
}

// 发起 SSH 建连之前也可捕获 generation，排队和握手期间离开主机都会使它失效。
func (h *hostSession) connectAt(ctx context.Context, base *client.Client, expectedGeneration uint64) error {
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
	h.generation++
	h.base = &copy
	generation := h.generation
	h.current.Client = nil
	h.current.Connected, h.current.Controlled = false, false
	h.current.Generation = generation
	automatic := !copy.Remote && !h.automaticTried
	h.automaticTried = true
	h.mu.Unlock()
	_ = old.release(ctx)

	var state rpc.DesktopState
	if err := hostControlCall(ctx, &copy, "desktop.status", nil, &state); err != nil {
		return err
	}
	if !h.publish(generation, &copy, state, false) {
		return context.Canceled
	}
	if automatic {
		return h.takeLocked(ctx, generation, &copy, state, true)
	}
	return nil
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
	return nil
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
	if !h.publish(before.Generation, base, state, false) {
		return context.Canceled
	}
	return nil
}
