package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef void (*hq_ts_status_fn)(uint8_t *data, int32_t len);

static inline void hq_ts_emit(hq_ts_status_fn f, uint8_t *data, int32_t len) {
	f(data, len);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"posse/internal/mquic"
	"posse/internal/tslink"
)

const udpBridgeMaxPacket = 4096

// tsIdleDelay 是 Flutter 引擎销毁（hq_reset）后，若无新引擎接管且无活跃连接/UDP 桥，
// 自动关停进程内 Tailscale 节点的等待时间，防止 Android Activity 销毁后长期保活 DERP。
var tsIdleDelay = 90 * time.Second

// 全局锁层级（严格单向，杜绝死锁）：
//
//	tsOpMu (RWMutex: 写锁串行 start/stop/reset/idleStop，读锁保护 dial / 开桥；hq_ts_set_cb 不取此锁避免卡 UI)
//	  -> tsLifecycleMu (tsNode / tsLastOpts / tsIdleTimer / tsIdleSeq / engineGen / engineFrozen)
//	       -> tsEmitMu (tsCb)
//	       -> connMu (conns) -> emitMu (entry.cb)
//	       -> bridgeMu (udpBridges)
var (
	tsOpMu sync.RWMutex

	tsLifecycleMu sync.Mutex
	tsNode        *tslink.Node
	tsLastOpts    tslink.Options // 最近一次启动配置（不含 AuthKey），用于 idleStop 与新引擎接管竞态时无损恢复
	tsIdleTimer   *time.Timer
	tsIdleSeq     uint64
	engineGen     uint64 = 1
	engineFrozen  bool
	engineCtx     context.Context
	engineCancel  context.CancelFunc

	// 测试注入点，仅用于单测确定性复现 tsIdleStop 与 begin/reset 并发交错；生产环境恒为 nil。
	tsIdleStopAfterStopHook     func()
	tsIdleStopBeforeRestartHook func()

	// tsEmitOrderMu 保证初始状态推送与后续 OnChange 始终在锁内现读最新状态，彻底消除倒序通知。
	tsEmitOrderMu    sync.Mutex
	tsLastEmittedGen uint64
	tsLastEmitted    tslink.Status
	tsCbGen          atomic.Uint64

	tsEmitMu sync.RWMutex
	tsCb     atomic.Pointer[[0]byte] // C.hq_ts_status_fn

	bridgeMu     sync.Mutex
	nextBridgeID atomic.Int64
	udpBridges   = map[int64]*udpBridge{}
)

type tsStartRequest struct {
	StateDir             string `json:"stateDir"`
	Hostname             string `json:"hostname,omitempty"`
	ControlURL           string `json:"controlUrl,omitempty"`
	AuthKey              string `json:"authKey,omitempty"`
	DisableBindInterface bool   `json:"disableBindInterface,omitempty"`
}

type tsStatusResponse struct {
	Status tslink.Status `json:"status"`
	Err    string        `json:"err,omitempty"`
}

type tsResetResponse struct {
	Status       tslink.Status `json:"status"`
	LoggedOut    bool          `json:"loggedOut"`
	LogoutError  string        `json:"logoutError,omitempty"`
	LocalCleared bool          `json:"localCleared"`
	Err          string        `json:"err,omitempty"`
}

type tsUDPBridgeResponse struct {
	ID        int64  `json:"id,omitempty"`
	LocalPort int    `json:"localPort,omitempty"`
	Err       string `json:"err,omitempty"`
}

func init() {
	engineCtx, engineCancel = context.WithCancel(context.Background())
}

func currentTSNode() *tslink.Node {
	tsLifecycleMu.Lock()
	defer tsLifecycleMu.Unlock()
	return tsNode
}

func isEngineActiveLocked(gen uint64) bool {
	return !engineFrozen && gen != 0 && gen == engineGen
}

func ensureTSNodeLocked(stateDir, hostname string) *tslink.Node {
	if tsNode != nil {
		return tsNode
	}
	tsNode = tslink.New(tslink.Options{
		StateDir: stateDir,
		Hostname: hostname,
		OnChange: func(tslink.Status) { emitLatestTSStatus() },
	})
	return tsNode
}

// emitLatestTSStatus 在 tsEmitOrderMu 内现读最新状态并按回调代次去重发送，
// 既保证新注册的回调必收一次当前快照，又绝不会把过期快照晚发覆盖较新状态。
func emitLatestTSStatus() {
	tsEmitOrderMu.Lock()
	defer tsEmitOrderMu.Unlock()

	cbGen := tsCbGen.Load()
	st := tsStatus()
	if cbGen != 0 && tsLastEmittedGen == cbGen && tsLastEmitted.Equal(st) {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tsEmitMu.RLock()
	defer tsEmitMu.RUnlock()
	f := tsCb.Load()
	if f == nil || tsCbGen.Load() != cbGen {
		return
	}
	var p *C.uint8_t
	if len(data) > 0 {
		p = (*C.uint8_t)(C.CBytes(data))
	}
	C.hq_ts_emit(f, p, C.int32_t(len(data)))
	tsLastEmittedGen = cbGen
	tsLastEmitted = st
}

func cancelTSIdleTimerLocked() {
	tsIdleSeq++
	if tsIdleTimer != nil {
		tsIdleTimer.Stop()
		tsIdleTimer = nil
	}
}

func activeConnCountLocked() int {
	connMu.Lock()
	n := len(conns)
	connMu.Unlock()
	return n
}

func activeBridgeCountLocked() int {
	bridgeMu.Lock()
	n := len(udpBridges)
	bridgeMu.Unlock()
	return n
}

func activeBridgeCount() int {
	bridgeMu.Lock()
	n := len(udpBridges)
	bridgeMu.Unlock()
	return n
}

func scheduleTSIdleTimerIfNeeded() {
	tsLifecycleMu.Lock()
	defer tsLifecycleMu.Unlock()
	scheduleTSIdleTimerLocked()
}

func scheduleTSIdleTimerLocked() {
	if tsNode == nil || tsCb.Load() != nil {
		return
	}
	st := tsNode.Status()
	if st.State == tslink.StateDisabled || st.State == tslink.StateStopped {
		return
	}
	if activeConnCountLocked() > 0 || activeBridgeCountLocked() > 0 {
		return
	}
	cancelTSIdleTimerLocked()
	seq := tsIdleSeq
	delay := tsIdleDelay
	if delay <= 0 {
		delay = time.Millisecond
	}
	tsIdleTimer = time.AfterFunc(delay, func() {
		tsIdleStop(seq)
	})
}

// tsIdleStop 在 tsOpMu 写锁下复核并关停节点；若关停期间新引擎通过 hq_engine_begin 解除了冻结，
// 即使尚未完成首次 set_cb/dial 登记也立即用不含 AuthKey 的最近配置恢复节点；
// 若恢复期间再次发生引擎销毁（engineFrozen 重新置真），恢复结束后立刻补挂空闲关停定时器，绝不留无主节点。
func tsIdleStop(seq uint64) {
	tsOpMu.Lock()
	defer tsOpMu.Unlock()

	tsLifecycleMu.Lock()
	if tsIdleSeq != seq || !engineFrozen || tsCb.Load() != nil || activeConnCountLocked() > 0 || activeBridgeCountLocked() > 0 || tsNode == nil {
		tsLifecycleMu.Unlock()
		return
	}
	tsIdleTimer = nil
	node := tsNode
	restoreOpts := tsLastOpts
	afterStopHook := tsIdleStopAfterStopHook
	beforeRestartHook := tsIdleStopBeforeRestartHook
	tsLifecycleMu.Unlock()

	_ = node.Stop()
	if afterStopHook != nil {
		afterStopHook()
	}

	// 只要新引擎已调用 hq_engine_begin（!engineFrozen），覆盖从 begin 到首次登记的完整窗口，立即恢复热节点。
	tsLifecycleMu.Lock()
	takenOver := !engineFrozen && restoreOpts.StateDir != "" && tsNode == node
	tsLifecycleMu.Unlock()
	if !takenOver {
		return
	}
	if beforeRestartHook != nil {
		beforeRestartHook()
	}
	_ = node.Start(restoreOpts)
	// 若在恢复启动前或启动期间再次发生引擎销毁，必须按普通启动路径补挂空闲关停定时器，避免留下无主节点。
	scheduleTSIdleTimerIfNeeded()
}

func tsStart(gen uint64, req tsStartRequest) tsStatusResponse {
	tsOpMu.Lock()
	defer tsOpMu.Unlock()

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		return tsStatusResponse{Status: tsStatus(), Err: "engine is not active"}
	}
	tsLifecycleMu.Unlock()

	stateDir, err := tslink.ValidateStateDir(req.StateDir)
	if err != nil {
		return tsStatusResponse{Status: tsStatus(), Err: err.Error()}
	}
	controlURL := strings.TrimSpace(req.ControlURL)
	if err := tslink.ValidateControlURL(controlURL); err != nil {
		return tsStatusResponse{Status: tsStatus(), Err: err.Error()}
	}
	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		hostname = tslink.DefaultHostname("posse-mobile")
	}

	opts := tslink.Options{
		StateDir:             stateDir,
		Hostname:             hostname,
		ControlURL:           controlURL,
		DisableBindInterface: req.DisableBindInterface,
	}

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		return tsStatusResponse{Status: tsStatus(), Err: "engine is not active"}
	}
	cancelTSIdleTimerLocked()
	node := ensureTSNodeLocked(stateDir, hostname)
	tsLastOpts = opts // 不含 AuthKey
	tsLifecycleMu.Unlock()

	startOpts := opts
	startOpts.AuthKey = strings.TrimSpace(req.AuthKey)
	err = node.Start(startOpts)
	st := node.Status()

	// 若启动期间发生过引擎销毁（tsCb 已被清空且无活跃连接/桥），立刻补挂空闲关停定时器，避免留下无主节点。
	scheduleTSIdleTimerIfNeeded()

	if err != nil {
		return tsStatusResponse{Status: st, Err: err.Error()}
	}
	return tsStatusResponse{Status: st}
}

func tsStop(gen uint64) tsStatusResponse {
	tsOpMu.Lock()
	defer tsOpMu.Unlock()

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		return tsStatusResponse{Status: tsStatus(), Err: "engine is not active"}
	}
	cancelTSIdleTimerLocked()
	tsLastOpts = tslink.Options{}
	node := tsNode
	bridgeMu.Lock()
	oldBridges := make([]*udpBridge, 0, len(udpBridges))
	for id, b := range udpBridges {
		oldBridges = append(oldBridges, b)
		delete(udpBridges, id)
	}
	bridgeMu.Unlock()
	tsLifecycleMu.Unlock()

	for _, b := range oldBridges {
		b.closeDetached()
	}

	if node == nil {
		return tsStatusResponse{Status: tslink.Status{State: tslink.StateDisabled}}
	}
	err := node.Stop()
	st := node.Status()
	if err != nil {
		return tsStatusResponse{Status: st, Err: err.Error()}
	}
	return tsStatusResponse{Status: st}
}

func tsReset(gen uint64, stateDir string) tsResetResponse {
	tsOpMu.Lock()
	defer tsOpMu.Unlock()

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		return tsResetResponse{Status: tsStatus(), Err: "engine is not active"}
	}
	cancelTSIdleTimerLocked()
	tsLastOpts = tslink.Options{}
	node := tsNode
	bridgeMu.Lock()
	oldBridges := make([]*udpBridge, 0, len(udpBridges))
	for id, b := range udpBridges {
		oldBridges = append(oldBridges, b)
		delete(udpBridges, id)
	}
	bridgeMu.Unlock()
	tsLifecycleMu.Unlock()

	for _, b := range oldBridges {
		b.closeDetached()
	}

	if node == nil {
		cleaned, err := tslink.ValidateStateDir(stateDir)
		if err != nil {
			return tsResetResponse{
				Status: tslink.Status{State: tslink.StateDisabled},
				Err:    err.Error(),
			}
		}
		tsLifecycleMu.Lock()
		if !isEngineActiveLocked(gen) {
			tsLifecycleMu.Unlock()
			return tsResetResponse{Status: tsStatus(), Err: "engine is not active"}
		}
		node = ensureTSNodeLocked(cleaned, tslink.DefaultHostname("posse-mobile"))
		tsLifecycleMu.Unlock()
	}

	res, err := node.Reset()
	out := tsResetResponse{
		Status:       node.Status(),
		LoggedOut:    res.LoggedOut,
		LocalCleared: res.LocalCleared,
	}
	if res.LogoutErr != nil {
		out.LogoutError = res.LogoutErr.Error()
	}
	if err != nil {
		out.Err = err.Error()
	}
	return out
}

func tsLogin(gen uint64, timeout time.Duration) tsStatusResponse {
	// 持 tsOpMu 读锁防止并发重配置替换节点；使用当前代次的 engineCtx，引擎 reset 时立刻取消不拖慢写锁。
	tsOpMu.RLock()
	defer tsOpMu.RUnlock()

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		return tsStatusResponse{Status: tsStatus(), Err: "engine is not active"}
	}
	node := tsNode
	baseCtx := engineCtx
	tsLifecycleMu.Unlock()

	if node == nil {
		return tsStatusResponse{
			Status: tslink.Status{State: tslink.StateDisabled},
			Err:    tslink.ErrNotRunning.Error(),
		}
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()
	err := node.StartLoginInteractive(ctx)
	st := node.Status()
	if err != nil {
		return tsStatusResponse{Status: st, Err: err.Error()}
	}
	return tsStatusResponse{Status: st}
}

func tsStatus() tslink.Status {
	node := currentTSNode()
	if node == nil {
		return tslink.Status{State: tslink.StateDisabled}
	}
	return node.Status()
}

func tsPeerRoute(ipStr string, timeout time.Duration) tslink.PeerRoute {
	node := currentTSNode()
	if node == nil || !node.IsRunning() {
		return tslink.PeerRoute{Kind: tslink.RouteUnknown}
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil || !tslink.IsTailscaleIP(ip) {
		return tslink.PeerRoute{Kind: tslink.RouteUnknown}
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return node.PeerRoute(ctx, ip)
}

func parseTailscaleAddrPort(raw string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(strings.TrimSpace(raw))
	if err != nil || !ap.Addr().IsValid() || ap.Port() == 0 {
		return netip.AddrPort{}, errors.New("invalid target address")
	}
	if !tslink.IsTailscaleIP(ap.Addr()) {
		return netip.AddrPort{}, errors.New("target must be a Tailscale IPv4 address")
	}
	return ap, nil
}

// dialTailscaleQUIC 经内置 tsnet 的 netstack UDP 拨号 QUIC（使用 mquic.TSConfig 1200 B Initial MTU）。
func dialTailscaleQUIC(ctx context.Context, addr string) (*mquic.Conn, error) {
	ap, err := parseTailscaleAddrPort(addr)
	if err != nil {
		return nil, err
	}
	// 在 tsOpMu 读锁下获取 netstack PacketConn，避免与 tsIdleStop 竞态
	tsOpMu.RLock()
	node := currentTSNode()
	if node == nil || !node.IsRunning() {
		tsOpMu.RUnlock()
		return nil, tslink.ErrNotRunning
	}
	pc, err := node.ListenPacket(0)
	tsOpMu.RUnlock()
	if err != nil {
		return nil, err
	}
	// DialOver 失败时自动关闭 pc，成功时由 Conn.Close 释放 pc
	return mquic.DialOver(ctx, pc, net.UDPAddrFromAddrPort(ap), mquic.TSConfig())
}

// udpBridge 把本机回环 UDP（127.0.0.1:localPort）双向桥接到 tsnet 用户态 UDP，
// 供原生 iOS/Android DeskSession 收发看桌面 HEVC RTP/UDP 帧。
// 回环侧非连接、逐包重学来源端口，兼容暂停/恢复时原生 socket 重建。
type udpBridge struct {
	id        int64
	local     *net.UDPConn
	remote    net.Conn
	peer      atomic.Pointer[net.UDPAddr]
	closed    atomic.Bool
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// newUDPBridgeForGen 创建并登记回环 UDP 桥；若登记前引擎已冻结或代次变化，立即关闭资源并报错。
func newUDPBridgeForGen(remote net.Conn, gen uint64) (*udpBridge, error) {
	local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = remote.Close()
		return nil, err
	}
	b := &udpBridge{
		id:     nextBridgeID.Add(1),
		local:  local,
		remote: remote,
	}

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		_ = local.Close()
		_ = remote.Close()
		return nil, errors.New("engine reset while opening UDP bridge")
	}
	bridgeMu.Lock()
	udpBridges[b.id] = b
	bridgeMu.Unlock()
	cancelTSIdleTimerLocked()
	tsLifecycleMu.Unlock()

	b.wg.Add(2)
	go b.pumpLocalToRemote()
	go b.pumpRemoteToLocal()
	return b, nil
}

func (b *udpBridge) localPort() int {
	if addr, ok := b.local.LocalAddr().(*net.UDPAddr); ok {
		return addr.Port
	}
	return 0
}

func (b *udpBridge) pumpLocalToRemote() {
	defer b.wg.Done()
	buf := make([]byte, udpBridgeMaxPacket)
	for {
		n, src, err := b.local.ReadFromUDP(buf)
		if err != nil {
			b.closeAndRemove()
			return
		}
		if src == nil || !src.IP.IsLoopback() {
			continue
		}
		// 每次都拷贝一份地址并更新，适配原生 DeskSession 暂停恢复后换端口
		cp := &net.UDPAddr{IP: append(net.IP(nil), src.IP...), Port: src.Port, Zone: src.Zone}
		b.peer.Store(cp)
		if _, err := b.remote.Write(buf[:n]); err != nil {
			if b.closed.Load() || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				b.closeAndRemove()
				return
			}
		}
	}
}

func (b *udpBridge) pumpRemoteToLocal() {
	defer b.wg.Done()
	buf := make([]byte, udpBridgeMaxPacket)
	for {
		n, err := b.remote.Read(buf)
		if err != nil {
			// gVisor gonet.UDPConn 关闭时返回 io.EOF 或 net.ErrClosed
			b.closeAndRemove()
			return
		}
		peer := b.peer.Load()
		if peer == nil {
			continue
		}
		if _, err := b.local.WriteToUDP(buf[:n], peer); err != nil {
			if b.closed.Load() || errors.Is(err, net.ErrClosed) {
				b.closeAndRemove()
				return
			}
		}
	}
}

func (b *udpBridge) closeAndRemove() {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		bridgeMu.Lock()
		delete(udpBridges, b.id)
		bridgeMu.Unlock()
		_ = b.local.Close()
		_ = b.remote.Close()
		scheduleTSIdleTimerIfNeeded()
	})
}

// closeDetached 关闭已被调用方从 udpBridges 摘下的桥（不触碰全局表，避免干扰新引擎）。
func (b *udpBridge) closeDetached() {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		_ = b.local.Close()
		_ = b.remote.Close()
	})
}

func openUDPBridge(gen uint64, remoteAddr string, timeout time.Duration) tsUDPBridgeResponse {
	ap, err := parseTailscaleAddrPort(remoteAddr)
	if err != nil {
		return tsUDPBridgeResponse{Err: err.Error()}
	}
	// 整个开桥（DialUDP + 登记）在 tsOpMu 读锁下执行，防止空闲定时器中途关停节点
	tsOpMu.RLock()
	defer tsOpMu.RUnlock()

	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(gen) {
		tsLifecycleMu.Unlock()
		return tsUDPBridgeResponse{Err: "engine is not active"}
	}
	node := tsNode
	tsLifecycleMu.Unlock()

	if node == nil || !node.IsRunning() {
		return tsUDPBridgeResponse{Err: tslink.ErrNotRunning.Error()}
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tsConn, err := node.DialUDP(ctx, ap.String())
	if err != nil {
		return tsUDPBridgeResponse{Err: err.Error()}
	}
	b, err := newUDPBridgeForGen(tsConn, gen)
	if err != nil {
		return tsUDPBridgeResponse{Err: err.Error()}
	}
	return tsUDPBridgeResponse{ID: b.id, LocalPort: b.localPort()}
}

func closeUDPBridge(id int64) {
	bridgeMu.Lock()
	b := udpBridges[id]
	bridgeMu.Unlock()
	if b != nil {
		b.closeAndRemove()
	}
}

func marshalJSONCString(v any) *C.char {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"err":"marshal failed"}`)
	}
	return C.CString(string(b))
}

// hq_ts_set_cb 只持极短的 tsLifecycleMu -> tsEmitMu 锁，绝不等待 tsOpMu 上的慢 I/O，避免卡住 Dart 主线程。
// 无论 cb 是否为空，都必须匹配当前活跃引擎代次，防止旧代传空回调清掉新代已注册回调。
//
//export hq_ts_set_cb
func hq_ts_set_cb(gen C.uint64_t, cb C.hq_ts_status_fn) {
	tsLifecycleMu.Lock()
	if !isEngineActiveLocked(uint64(gen)) {
		tsLifecycleMu.Unlock()
		return
	}
	tsCbGen.Add(1)
	tsEmitMu.Lock()
	tsCb.Store(cb)
	tsEmitMu.Unlock()
	if cb != nil {
		cancelTSIdleTimerLocked()
	} else {
		scheduleTSIdleTimerLocked()
	}
	tsLifecycleMu.Unlock()

	if cb != nil {
		go emitLatestTSStatus()
	}
}

//export hq_ts_start
func hq_ts_start(gen C.uint64_t, req *C.char, reqLen C.int32_t) *C.char {
	if req == nil || reqLen <= 0 {
		return marshalJSONCString(tsStatusResponse{Status: tsStatus(), Err: "empty request"})
	}
	raw := C.GoBytes(unsafe.Pointer(req), C.int(reqLen))
	var r tsStartRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return marshalJSONCString(tsStatusResponse{Status: tsStatus(), Err: err.Error()})
	}
	return marshalJSONCString(tsStart(uint64(gen), r))
}

//export hq_ts_stop
func hq_ts_stop(gen C.uint64_t) *C.char {
	return marshalJSONCString(tsStop(uint64(gen)))
}

//export hq_ts_reset
func hq_ts_reset(gen C.uint64_t, stateDir *C.char, stateDirLen C.int32_t) *C.char {
	dir := ""
	if stateDir != nil && stateDirLen > 0 {
		dir = string(C.GoBytes(unsafe.Pointer(stateDir), C.int(stateDirLen)))
	}
	return marshalJSONCString(tsReset(uint64(gen), dir))
}

//export hq_ts_login
func hq_ts_login(gen C.uint64_t, timeoutMs C.int32_t) *C.char {
	return marshalJSONCString(tsLogin(uint64(gen), time.Duration(timeoutMs)*time.Millisecond))
}

//export hq_ts_status
func hq_ts_status() *C.char {
	return marshalJSONCString(tsStatus())
}

//export hq_ts_wake
func hq_ts_wake() {
	if node := currentTSNode(); node != nil {
		node.InjectLinkChange()
	}
}

//export hq_ts_peer_route
func hq_ts_peer_route(ip *C.char, timeoutMs C.int32_t) *C.char {
	s := ""
	if ip != nil {
		s = C.GoString(ip)
	}
	return marshalJSONCString(tsPeerRoute(s, time.Duration(timeoutMs)*time.Millisecond))
}

//export hq_ts_udp_bridge_open
func hq_ts_udp_bridge_open(gen C.uint64_t, remoteAddr *C.char, timeoutMs C.int32_t) *C.char {
	a := ""
	if remoteAddr != nil {
		a = C.GoString(remoteAddr)
	}
	return marshalJSONCString(openUDPBridge(uint64(gen), a, time.Duration(timeoutMs)*time.Millisecond))
}

//export hq_ts_udp_bridge_close
func hq_ts_udp_bridge_close(id C.int64_t) {
	closeUDPBridge(int64(id))
}
