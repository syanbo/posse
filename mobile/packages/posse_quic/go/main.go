// posse_quic 是编进手机 App 的 QUIC 客户端，Dart 经 FFI 调用。拨号、收发都不阻塞调用方：
// 结果由 Go 的 goroutine 调 Dart 给的回调（NativeCallable.listener）送回，按发生顺序到达。
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef void (*hq_event_fn)(int64_t conn, int32_t kind, uint8_t *data, int32_t len);

static inline void hq_emit(hq_event_fn f, int64_t conn, int32_t kind, uint8_t *data, int32_t len) {
	f(conn, kind, data, len);
}
*/
import "C"

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/quic-go/quic-go"

	"posse/internal/mquic"
)

// 回调的 kind。数据由 Go 用 malloc 分配，Dart 拷走后调 hq_free。
const (
	evOpen     = 1
	evText     = 2
	evBinary   = 3
	evClosed   = 4 // 数据是原因
	evFailed   = 5 // 拨号失败，数据是错误
	evPrepared = 6 // hq_prepare 问出了公网地址，数据是 "ip:端口"
	evPeerCode = 7 // 对方发来关闭帧，数据是 u16 关闭码（大端），紧接着是 evClosed
)

// sendQueue 是还没写出去的消息上限。超过说明连接早就堵死了，断开让上层重连。
const sendQueue = 4096

type outMsg struct {
	typ  byte
	data []byte
	code uint16 // typ 为 Close 时
}

type entry struct {
	id     int64
	cb     atomic.Pointer[[0]byte] // C.hq_event_fn；hq_reset 后为 nil，不再回调
	cancel context.CancelFunc

	mu     sync.Mutex
	c      *mquic.Conn
	prep   *mquic.Prepared // hq_prepare 之后、hq_dial_prepared 之前
	closed bool

	out  chan outMsg
	done chan struct{}
	once sync.Once
}

var (
	nextID atomic.Int64
	connMu sync.Mutex
	conns  = map[int64]*entry{}

	// emit 调回调期间持读锁，hq_reset 拿写锁等它们调完：之后回调可以失效
	emitMu sync.RWMutex
)

func lookup(id C.int64_t) *entry {
	connMu.Lock()
	defer connMu.Unlock()
	return conns[int64(id)]
}

func (e *entry) emit(kind int, data []byte) {
	emitMu.RLock()
	defer emitMu.RUnlock()
	f := e.cb.Load()
	if f == nil {
		return
	}
	var p *C.uint8_t
	if len(data) > 0 {
		p = (*C.uint8_t)(C.CBytes(data))
	}
	C.hq_emit(f, C.int64_t(e.id), C.int32_t(kind), p, C.int32_t(len(data)))
}

// finish 只发一次结束事件并摘掉这条连接。先回调后摘：hq_reset 只清还在表里的连接的回调，
// 先摘的话夹在中间的这次回调它清不到、也等不到
func (e *entry) finish(kind int, msg string) {
	e.once.Do(func() {
		close(e.done)
		e.emit(kind, []byte(msg))
		connMu.Lock()
		delete(conns, e.id)
		connMu.Unlock()
		scheduleTSIdleTimerIfNeeded()
	})
}

//export hq_dial
func hq_dial(gen C.uint64_t, addr *C.char, token *C.uint8_t, tokenLen C.int32_t, timeoutMs C.int32_t, cb C.hq_event_fn) C.int64_t {
	if addr == nil {
		return -1
	}
	a := C.GoString(addr)
	var tok []byte
	if token != nil && tokenLen > 0 {
		tok = C.GoBytes(unsafe.Pointer(token), C.int(tokenLen))
	}
	e, ctx := newEntry(uint64(gen), cb, timeoutMs)
	if e == nil {
		return -1
	}
	go e.run(ctx, a, tok)
	return C.int64_t(e.id)
}

//export hq_ts_dial
func hq_ts_dial(gen C.uint64_t, addr *C.char, timeoutMs C.int32_t, cb C.hq_event_fn) C.int64_t {
	if addr == nil {
		return -1
	}
	a := C.GoString(addr)
	e, ctx := newEntry(uint64(gen), cb, timeoutMs)
	if e == nil {
		return -1
	}
	go e.serve(ctx, func(ctx context.Context) (*mquic.Conn, error) {
		return dialTailscaleQUIC(ctx, a)
	})
	return C.int64_t(e.id)
}

// newEntry 在 tsLifecycleMu -> connMu 锁序下校验引擎代次并登记连接，
// 防止引擎销毁（hq_reset）后旧 isolate 的并发拨号挂入已失效回调。
func newEntry(gen uint64, cb C.hq_event_fn, timeoutMs C.int32_t) (*entry, context.Context) {
	tsLifecycleMu.Lock()
	if engineFrozen || gen == 0 || gen != engineGen {
		tsLifecycleMu.Unlock()
		return nil, nil
	}
	e := &entry{id: nextID.Add(1), out: make(chan outMsg, sendQueue), done: make(chan struct{})}
	e.cb.Store(cb)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	e.cancel = cancel
	connMu.Lock()
	conns[e.id] = e
	connMu.Unlock()
	cancelTSIdleTimerLocked()
	tsLifecycleMu.Unlock()
	return e, ctx
}

// hq_prepare 是打洞的第一步：开一个 socket，经中转 relay 问出它的公网地址，用 evPrepared 报回。
// Dart 把地址交给 Mac（posse.punch），拿到 Mac 的地址后调 hq_dial_prepared；不用了调 hq_close。
//
//export hq_prepare
func hq_prepare(gen C.uint64_t, relay *C.char, timeoutMs C.int32_t, cb C.hq_event_fn) C.int64_t {
	if relay == nil {
		return -1
	}
	e, ctx := newEntry(uint64(gen), cb, timeoutMs)
	if e == nil {
		return -1
	}
	r := C.GoString(relay)
	go func() {
		p, err := mquic.Prepare(ctx, r)
		e.cancel()
		if err != nil {
			e.finish(evFailed, err.Error())
			return
		}
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			p.Close()
			e.finish(evClosed, "closed")
			return
		}
		e.prep = p
		e.mu.Unlock()
		e.emit(evPrepared, []byte(p.Public.String()))
	}()
	return C.int64_t(e.id)
}

// hq_dial_prepared 在 hq_prepare 开的 socket 上向 peer 打洞并拨号，之后和 hq_dial 的连接一样用。
//
//export hq_dial_prepared
func hq_dial_prepared(id C.int64_t, peer *C.char, timeoutMs C.int32_t) {
	e := lookup(id)
	if e == nil {
		return
	}
	e.mu.Lock()
	p := e.prep
	e.prep = nil
	e.mu.Unlock()
	if p == nil {
		e.finish(evFailed, "not prepared")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()
	a := C.GoString(peer)
	go e.serve(ctx, func(ctx context.Context) (*mquic.Conn, error) { return p.Dial(ctx, a) })
}

func (e *entry) run(ctx context.Context, addr string, token []byte) {
	e.serve(ctx, func(ctx context.Context) (*mquic.Conn, error) { return mquic.Dial(ctx, addr, token) })
}

// serve 拨号，连上后报 evOpen、开始收发，直到连接结束。
func (e *entry) serve(ctx context.Context, dial func(context.Context) (*mquic.Conn, error)) {
	c, err := dial(ctx)
	e.cancel()
	if err != nil {
		e.finish(evFailed, err.Error())
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		c.CloseNow()
		e.finish(evClosed, "closed")
		return
	}
	e.c = c
	e.mu.Unlock()
	e.emit(evOpen, nil)
	go e.write(c)
	for {
		typ, p, err := c.Read()
		if err != nil {
			c.CloseNow()
			// 手机按关闭码认出旧版电脑拒绝握手（4001），所以单独交出去
			if code, ok := peerCloseCode(err); ok {
				e.emit(evPeerCode, binary.BigEndian.AppendUint16(nil, code))
			}
			e.finish(evClosed, err.Error())
			return
		}
		kind := evText
		if typ == mquic.Binary {
			kind = evBinary
		}
		e.emit(kind, p)
	}
}

func (e *entry) write(c *mquic.Conn) {
	for {
		select {
		case <-e.done:
			return
		case m := <-e.out:
			if m.typ == mquic.Close {
				c.Close(m.code, string(m.data))
				return
			}
			// 和电脑那边一样：至少 15 秒，大消息按每秒 64 KB 再加
			c.SetWriteDeadline(time.Now().Add(15*time.Second + time.Duration(len(m.data)/(64<<10))*time.Second))
			if err := c.Write(m.typ, m.data); err != nil {
				c.CloseNow()
				return
			}
		}
	}
}

// hq_send 把一条消息排进发送队列，立刻返回。连接已断或队列满时返回 -1。
//
//export hq_send
func hq_send(id C.int64_t, typ C.int32_t, data *C.uint8_t, n C.int32_t) C.int32_t {
	e := lookup(id)
	if e == nil {
		return -1
	}
	m := outMsg{typ: byte(typ), data: C.GoBytes(unsafe.Pointer(data), C.int(n))}
	select {
	case e.out <- m:
		return 0
	default:
		hq_close(id, 1011, nil)
		return -1
	}
}

// hq_close 排在已经交出去的消息后面发关闭帧；还在拨号就取消。
//
//export hq_close
func hq_close(id C.int64_t, code C.int32_t, reason *C.char) {
	e := lookup(id)
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	c, p, cancel := e.c, e.prep, e.cancel
	e.prep = nil
	e.mu.Unlock()
	if p != nil {
		p.Close()
		e.finish(evClosed, "closed")
		return
	}
	if c == nil {
		cancel()
		return
	}
	msg := ""
	if reason != nil {
		msg = C.GoString(reason)
	}
	select {
	case e.out <- outMsg{typ: mquic.Close, code: uint16(code), data: []byte(msg)}:
	default:
		c.CloseNow()
	}
}

// hq_reset 不再回调、直接断开现有的所有连接，返回时已经没有正在进行的回调。安卓上 Flutter 引擎可能先于进程销毁
// （Activity 被关掉），之后旧连接的事件调到失效的回调上会崩溃：原生层在引擎销毁前调它（jni_android.go）。
// 调用后会冻结旧引擎的代次（engineFrozen=true），防止旧 isolate 在销毁前把回调或拨号重新挂回；
// 新引擎首次使用时调 hq_engine_begin() 解冻并获取新代次。
//
//export hq_reset
func hq_reset() {
	resetEngineState(true)
}

// hq_engine_begin 由新 Flutter 引擎初始化时调用一次：清理上一个引擎可能遗留的连接、回调与回环桥，
// 递增代次并解除冻结状态，返回当前引擎代次。
//
//export hq_engine_begin
func hq_engine_begin() C.uint64_t {
	return C.uint64_t(resetEngineState(false))
}

func resetEngineState(freeze bool) uint64 {
	// 严格遵循全局锁序：tsLifecycleMu -> (tsEmitMu | connMu | bridgeMu)
	// 在同一个临界区内切代、取消旧引擎上下文并摘除旧连接与旧回环 UDP 桥，确保旧 reset 绝不会误关新引擎后来登记的桥。
	tsLifecycleMu.Lock()
	engineGen++
	gen := engineGen
	engineFrozen = freeze
	oldCancel := engineCancel
	engineCtx, engineCancel = context.WithCancel(context.Background())
	tsCbGen.Add(1)
	tsEmitMu.Lock()
	tsCb.Store(nil)
	tsEmitMu.Unlock()
	if !freeze {
		cancelTSIdleTimerLocked()
	}

	connMu.Lock()
	old := make([]*entry, 0, len(conns))
	for id, e := range conns {
		old = append(old, e)
		delete(conns, id)
	}
	connMu.Unlock()

	bridgeMu.Lock()
	oldBridges := make([]*udpBridge, 0, len(udpBridges))
	for id, b := range udpBridges {
		oldBridges = append(oldBridges, b)
		delete(udpBridges, id)
	}
	bridgeMu.Unlock()
	tsLifecycleMu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}

	for _, e := range old {
		e.cb.Store(nil)
	}
	// 等正在进行的连接回调调完
	emitMu.Lock()
	emitMu.Unlock()
	for _, e := range old {
		e.mu.Lock()
		e.closed = true
		c, p, cancel := e.c, e.prep, e.cancel
		e.prep = nil
		e.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if p != nil {
			p.Close()
		}
		if c != nil {
			c.CloseNow()
		}
	}
	// 只关闭本次在临界区内摘下的旧桥；若本次为冻结（引擎销毁）且代次尚未被新引擎接管，启动空闲关停定时器。
	for _, b := range oldBridges {
		b.closeDetached()
	}
	if freeze {
		tsLifecycleMu.Lock()
		if engineFrozen && engineGen == gen {
			scheduleTSIdleTimerLocked()
		}
		tsLifecycleMu.Unlock()
	}
	return gen
}

// peerCloseCode 取对方关连接时给的码：读到了关闭帧，或者没来得及读、对方已经按同一个码关了整条连接
// （mquic.Conn.Close 发完关闭帧最多等 2 秒就关连接，App 在后台冻着时会错过那一帧）。
func peerCloseCode(err error) (uint16, bool) {
	if ce, ok := errors.AsType[*mquic.CloseError](err); ok {
		return ce.Code, true
	}
	if ae, ok := errors.AsType[*quic.ApplicationError](err); ok && ae.Remote && ae.ErrorCode != 0 && ae.ErrorCode <= 0xffff {
		return uint16(ae.ErrorCode), true
	}
	return 0, false
}

//export hq_free
func hq_free(p *C.uint8_t) {
	C.free(unsafe.Pointer(p))
}

func main() {}
