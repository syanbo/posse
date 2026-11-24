package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"posse/internal/tslink"
)

func resetGlobalTSForTest(t *testing.T) uint64 {
	t.Helper()
	gen := resetEngineState(false)
	t.Cleanup(func() {
		g := resetEngineState(false)
		tsStop(g)
		tsLifecycleMu.Lock()
		cancelTSIdleTimerLocked()
		tsNode = nil
		tsLastOpts = tslink.Options{}
		tsIdleStopAfterStopHook = nil
		tsIdleStopBeforeRestartHook = nil
		engineFrozen = false
		tsLifecycleMu.Unlock()
		tsEmitMu.Lock()
		tsCb.Store(nil)
		tsEmitMu.Unlock()
		_ = resetEngineState(false)
	})
	return gen
}

func TestUDPBridgeRoundTripAndPortRelearn(t *testing.T) {
	gen := resetGlobalTSForTest(t)

	// 用真实 loopback UDP socket 模拟远端 UDP 连接（比 net.Pipe 更贴近真实 UDP 报文语义）
	remoteServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP remoteServer: %v", err)
	}
	defer remoteServer.Close()

	remoteClient, err := net.DialUDP("udp4", nil, remoteServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP remoteClient: %v", err)
	}

	b, err := newUDPBridgeForGen(remoteClient, gen)
	if err != nil {
		t.Fatalf("newUDPBridgeForGen: %v", err)
	}
	defer closeUDPBridge(b.id)

	if b.localPort() <= 0 {
		t.Fatalf("invalid localPort: %d", b.localPort())
	}
	bridgeAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: b.localPort()}

	// 1) 原生侧第一个 UDP socket 发送并接收回包
	sock1, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP sock1: %v", err)
	}
	defer sock1.Close()

	if _, err := sock1.WriteToUDP([]byte("hello-1"), bridgeAddr); err != nil {
		t.Fatalf("sock1 write: %v", err)
	}
	buf := make([]byte, 64)
	_ = remoteServer.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, clientAddr, err := remoteServer.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "hello-1" {
		t.Fatalf("remoteServer read = %q, err=%v", string(buf[:n]), err)
	}

	if _, err := remoteServer.WriteToUDP([]byte("reply-1"), clientAddr); err != nil {
		t.Fatalf("remoteServer write: %v", err)
	}
	_ = sock1.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := sock1.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "reply-1" {
		t.Fatalf("sock1 read = %q, err=%v", string(buf[:n]), err)
	}
	if from.Port != b.localPort() {
		t.Fatalf("reply came from port %d, want bridge port %d", from.Port, b.localPort())
	}

	// 2) 模拟 DeskSession 暂停/恢复后重建 socket（来源端口变化），桥接器应自动重学新端口
	sock2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP sock2: %v", err)
	}
	defer sock2.Close()

	if _, err := sock2.WriteToUDP([]byte("hello-2"), bridgeAddr); err != nil {
		t.Fatalf("sock2 write: %v", err)
	}
	_ = remoteServer.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = remoteServer.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "hello-2" {
		t.Fatalf("remoteServer read 2 = %q, err=%v", string(buf[:n]), err)
	}

	if _, err := remoteServer.WriteToUDP([]byte("reply-2"), clientAddr); err != nil {
		t.Fatalf("remoteServer write 2: %v", err)
	}
	_ = sock2.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = sock2.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "reply-2" {
		t.Fatalf("sock2 read = %q, err=%v", string(buf[:n]), err)
	}

	// 3) 显式关闭桥接器应立刻回收表项
	closeUDPBridge(b.id)
	if activeBridgeCount() != 0 {
		t.Fatalf("bridge leaked after close: count=%d", activeBridgeCount())
	}
}

func TestUDPBridgeRemoteEOFAutoCleanup(t *testing.T) {
	gen := resetGlobalTSForTest(t)

	bridgeEnd, peerEnd := net.Pipe()
	if _, err := newUDPBridgeForGen(bridgeEnd, gen); err != nil {
		t.Fatalf("newUDPBridgeForGen: %v", err)
	}
	_ = peerEnd.Close()
	deadline := time.Now().Add(3 * time.Second)
	for activeBridgeCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if activeBridgeCount() != 0 {
		t.Fatalf("bridge leaked after remote EOF: count=%d", activeBridgeCount())
	}
}

// TestOldResetPreservesNewBridge 回归验证：旧引擎 reset 在锁外清理旧连接时，
// 新引擎已经 begin 并登记的 UDP 桥绝不能被旧 reset 误关。
func TestOldResetPreservesNewBridge(t *testing.T) {
	oldGen := resetGlobalTSForTest(t)

	// 在旧代登记一条连接并按住它的 entry.mu，让旧 reset 停在清理旧连接的临界区外
	oldEntry, _ := newEntry(oldGen, nil, 5000)
	if oldEntry == nil {
		t.Fatal("newEntry failed for oldGen")
	}
	oldEntry.mu.Lock()

	oldResetDone := make(chan struct{})
	go func() {
		defer close(oldResetDone)
		_ = resetEngineState(true)
	}()

	// 等旧 reset 已完成切代冻结并推进到等待 oldEntry.mu
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tsLifecycleMu.Lock()
		frozen := engineFrozen
		tsLifecycleMu.Unlock()
		if frozen {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 新引擎接管并登记新 UDP 桥
	newGen := resetEngineState(false)
	p1, p2 := net.Pipe()
	defer p2.Close()
	newBridge, err := newUDPBridgeForGen(p1, newGen)
	if err != nil {
		oldEntry.mu.Unlock()
		t.Fatalf("newUDPBridgeForGen failed for new engine: %v", err)
	}
	defer closeUDPBridge(newBridge.id)

	// 放行旧 reset 完成收尾
	oldEntry.mu.Unlock()
	<-oldResetDone

	if newBridge.closed.Load() || activeBridgeCount() != 1 {
		t.Fatalf("old reset closed a bridge registered by the new engine: closed=%v count=%d",
			newBridge.closed.Load(), activeBridgeCount())
	}
}

func TestStaleEngineGenCannotMutateNode(t *testing.T) {
	oldGen := resetGlobalTSForTest(t)
	newGen := resetEngineState(false)

	dir := t.TempDir()
	// 旧代次的所有变更/拨号/开桥入口必须全部被拒绝
	if res := tsStart(oldGen, tsStartRequest{StateDir: dir, ControlURL: "http://127.0.0.1:1"}); res.Err == "" {
		t.Fatal("stale gen tsStart succeeded")
	}
	if res := tsStop(oldGen); res.Err == "" {
		t.Fatal("stale gen tsStop succeeded")
	}
	if res := tsReset(oldGen, dir); res.Err == "" {
		t.Fatal("stale gen tsReset succeeded")
	}
	if res := tsLogin(oldGen, time.Second); res.Err == "" {
		t.Fatal("stale gen tsLogin succeeded")
	}
	if e, _ := newEntry(oldGen, nil, 1000); e != nil {
		t.Fatal("stale gen newEntry succeeded")
	}
	if res := openUDPBridge(oldGen, "100.64.0.1:6789", time.Second); res.Err != "engine is not active" {
		t.Fatalf("stale gen openUDPBridge = %+v, want engine is not active", res)
	}

	// 新代次启动成功，旧代次 stop/reset 仍不得破坏新代次节点
	res := tsStart(newGen, tsStartRequest{StateDir: dir, Hostname: "new-engine", ControlURL: "http://127.0.0.1:1"})
	if res.Err != "" || !res.Status.Enabled {
		t.Fatalf("new gen tsStart failed: %+v", res)
	}
	if stopRes := tsStop(oldGen); stopRes.Err == "" {
		t.Fatal("stale gen tsStop stopped new engine node")
	}
	if st := tsStatus(); !st.Enabled {
		t.Fatalf("new engine node was disabled by stale gen: %+v", st)
	}
}

func TestTailscaleStartValidationResetAndIdleShutdown(t *testing.T) {
	gen := resetGlobalTSForTest(t)

	// 初始状态 disabled，非 Tailscale 地址与未运行状态必须快速报错
	if st := tsStatus(); st.State != tslink.StateDisabled {
		t.Fatalf("initial state = %q, want disabled", st.State)
	}
	if _, err := dialTailscaleQUIC(context.Background(), "192.168.1.5:6789"); err == nil || !strings.Contains(err.Error(), "Tailscale IPv4") {
		t.Fatalf("expected non-Tailscale IPv4 rejected, got %v", err)
	}
	if _, err := dialTailscaleQUIC(context.Background(), "[::1]:6789"); err == nil || !strings.Contains(err.Error(), "Tailscale IPv4") {
		t.Fatalf("expected IPv6 rejected, got %v", err)
	}
	if _, err := dialTailscaleQUIC(context.Background(), "100.64.0.1:6789"); err == nil {
		t.Fatal("expected dialTailscaleQUIC error when not running")
	}
	if res := openUDPBridge(gen, "192.168.1.5:6789", time.Second); !strings.Contains(res.Err, "Tailscale IPv4") {
		t.Fatalf("expected openUDPBridge reject non-Tailscale IP, got %+v", res)
	}
	if res := openUDPBridge(gen, "100.64.0.1:6789", time.Second); res.Err == "" {
		t.Fatal("expected openUDPBridge error when not running")
	}

	// 非法 stateDir 和 controlUrl 被拒绝
	if res := tsStart(gen, tsStartRequest{StateDir: "relative/path"}); res.Err == "" {
		t.Fatal("expected invalid relative stateDir error")
	}
	if res := tsStart(gen, tsStartRequest{StateDir: t.TempDir(), ControlURL: "ftp://bad"}); res.Err == "" {
		t.Fatal("expected invalid controlUrl error")
	}

	origDelay := tsIdleDelay
	tsIdleDelay = 120 * time.Millisecond
	t.Cleanup(func() { tsIdleDelay = origDelay })

	// 用本地不可达 ControlURL 启动节点，不访问外网
	dir := t.TempDir()
	res := tsStart(gen, tsStartRequest{
		StateDir:   dir,
		Hostname:   "posse-mobile-test",
		ControlURL: "http://127.0.0.1:1",
	})
	if res.Err != "" {
		t.Fatalf("tsStart failed: %s", res.Err)
	}
	if !res.Status.Enabled {
		t.Fatalf("expected enabled=true after start, got %+v", res.Status)
	}

	// 模拟 hq_reset（引擎销毁冻结）：先关闭桥、拒绝旧代次开桥/拨号，不立刻停节点，待空闲延迟后自动关停
	p1, p2 := net.Pipe()
	defer p2.Close()
	if _, err := newUDPBridgeForGen(p1, gen); err != nil {
		t.Fatalf("newUDPBridgeForGen: %v", err)
	}
	_ = resetEngineState(true)
	if activeBridgeCount() != 0 {
		t.Fatalf("hq_reset did not close bridges: count=%d", activeBridgeCount())
	}

	// 冻结后旧代次拨号/开桥都必须被拒绝，不会留下孤立资源
	if e, _ := newEntry(gen, nil, 1000); e != nil {
		t.Fatal("newEntry succeeded while engine was frozen")
	}
	p3, p4 := net.Pipe()
	defer p4.Close()
	if _, err := newUDPBridgeForGen(p3, gen); err == nil {
		t.Fatal("newUDPBridgeForGen succeeded after reset/freeze")
	}
	if activeBridgeCount() != 0 {
		t.Fatalf("rejected bridge left count=%d", activeBridgeCount())
	}

	// 刚 reset 时节点尚未立即停止
	if st := tsStatus(); st.State == tslink.StateDisabled {
		t.Fatalf("hq_reset should not immediately stop node, got %+v", st)
	}
	// 超过空闲延迟后自动停掉节点
	deadline := time.Now().Add(3 * time.Second)
	for tsStatus().State != tslink.StateDisabled && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	if st := tsStatus(); st.State != tslink.StateDisabled {
		t.Fatalf("idle shutdown did not stop node: %+v", st)
	}

	// 场景 2：新引擎经 resetEngineState(false) 接管并持有活跃连接后，空闲定时器不会停掉节点
	newGen := resetEngineState(false)
	res = tsStart(newGen, tsStartRequest{
		StateDir:   dir,
		Hostname:   "posse-mobile-takeover",
		ControlURL: "http://127.0.0.1:1",
	})
	if res.Err != "" {
		t.Fatalf("takeover tsStart failed: %s", res.Err)
	}
	e, _ := newEntry(newGen, nil, 5000)
	if e == nil {
		t.Fatal("newEntry failed for active engine gen")
	}
	time.Sleep(tsIdleDelay * 2)
	if st := tsStatus(); st.State == tslink.StateDisabled {
		t.Fatalf("active connection did not keep node alive: %+v", st)
	}
	e.cancel()
	e.finish(evClosed, "done")

	// Reset 应清理本地目录并返回 disabled
	resetRes := tsReset(newGen, dir)
	if resetRes.Err != "" || !resetRes.LocalCleared || resetRes.Status.Enabled {
		t.Fatalf("unexpected tsReset result: %+v", resetRes)
	}
}

// TestIdleStopBeginBetweenStopAndFirstDial 回归验证：
// tsIdleStop 完成 node.Stop 后，新引擎只调用了 hq_engine_begin（尚未登记回调或首次拨号）也必须触发恢复，
// 后续首次操作绝不会看到 disabled。
func TestIdleStopBeginBetweenStopAndFirstDial(t *testing.T) {
	gen := resetGlobalTSForTest(t)
	dir := t.TempDir()

	res := tsStart(gen, tsStartRequest{
		StateDir:   dir,
		Hostname:   "posse-idle-begin",
		ControlURL: "http://127.0.0.1:1",
	})
	if res.Err != "" {
		t.Fatalf("tsStart: %s", res.Err)
	}

	origDelay := tsIdleDelay
	tsIdleDelay = time.Hour // 手动触发 tsIdleStop，精确控制屏障
	t.Cleanup(func() { tsIdleDelay = origDelay })

	_ = resetEngineState(true)
	tsLifecycleMu.Lock()
	seq := tsIdleSeq
	tsIdleStopAfterStopHook = func() {
		tsLifecycleMu.Lock()
		tsIdleStopAfterStopHook = nil
		tsLifecycleMu.Unlock()
		// 模拟 Stop 返回后、新引擎只完成了 hq_engine_begin（尚未调用 set_cb / dial）
		_ = resetEngineState(false)
	}
	tsLifecycleMu.Unlock()

	tsIdleStop(seq)

	if st := tsStatus(); !st.Enabled || st.State == tslink.StateDisabled {
		t.Fatalf("begin between Stop and first dial left node disabled: %+v", st)
	}
}

// TestIdleStopResetBeforeAutoRestart 回归验证：
// tsIdleStop 决定恢复后、调用 node.Start 之前若再次发生引擎销毁（hq_reset），
// 恢复启动完成后必须补挂空闲定时器并在延迟后停掉节点，绝不留下无主节点。
func TestIdleStopResetBeforeAutoRestart(t *testing.T) {
	gen := resetGlobalTSForTest(t)
	dir := t.TempDir()

	res := tsStart(gen, tsStartRequest{
		StateDir:   dir,
		Hostname:   "posse-idle-reset-again",
		ControlURL: "http://127.0.0.1:1",
	})
	if res.Err != "" {
		t.Fatalf("tsStart: %s", res.Err)
	}

	origDelay := tsIdleDelay
	tsIdleDelay = 80 * time.Millisecond
	t.Cleanup(func() { tsIdleDelay = origDelay })

	_ = resetEngineState(true)
	tsLifecycleMu.Lock()
	// 停止自动定时器，手动触发 tsIdleStop 精确控制交错点
	if tsIdleTimer != nil {
		tsIdleTimer.Stop()
		tsIdleTimer = nil
	}
	seq := tsIdleSeq
	tsIdleStopAfterStopHook = func() {
		tsLifecycleMu.Lock()
		tsIdleStopAfterStopHook = nil
		tsLifecycleMu.Unlock()
		// 先由新引擎接管，使 takenOver=true
		_ = resetEngineState(false)
	}
	tsIdleStopBeforeRestartHook = func() {
		tsLifecycleMu.Lock()
		tsIdleStopBeforeRestartHook = nil
		tsLifecycleMu.Unlock()
		// 在真正 node.Start(restoreOpts) 之前再次销毁引擎
		_ = resetEngineState(true)
	}
	tsLifecycleMu.Unlock()

	tsIdleStop(seq)

	// 恢复完成后应自动挂上空闲定时器，并很快再次停掉无主节点
	deadline := time.Now().Add(3 * time.Second)
	for tsStatus().State != tslink.StateDisabled && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	if st := tsStatus(); st.State != tslink.StateDisabled {
		t.Fatalf("ownerless node remained running after reset-before-restart: %+v", st)
	}
}
