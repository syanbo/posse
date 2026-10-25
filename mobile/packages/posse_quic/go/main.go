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
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

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
	cb     C.hq_event_fn
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
)

func lookup(id C.int64_t) *entry {
	connMu.Lock()
	defer connMu.Unlock()
	return conns[int64(id)]
}

func (e *entry) emit(kind int, data []byte) {
	var p *C.uint8_t
	if len(data) > 0 {
		p = (*C.uint8_t)(C.CBytes(data))
	}
	C.hq_emit(e.cb, C.int64_t(e.id), C.int32_t(kind), p, C.int32_t(len(data)))
}

// finish 只发一次结束事件并摘掉这条连接。
func (e *entry) finish(kind int, msg string) {
	e.once.Do(func() {
		close(e.done)
		connMu.Lock()
		delete(conns, e.id)
		connMu.Unlock()
		e.emit(kind, []byte(msg))
	})
}

//export hq_dial
func hq_dial(addr *C.char, token *C.uint8_t, tokenLen C.int32_t, timeoutMs C.int32_t, cb C.hq_event_fn) C.int64_t {
	a := C.GoString(addr)
	var tok []byte
	if tokenLen > 0 {
		tok = C.GoBytes(unsafe.Pointer(token), C.int(tokenLen))
	}
	e, ctx := newEntry(cb, timeoutMs)
	go e.run(ctx, a, tok)
	return C.int64_t(e.id)
}

func newEntry(cb C.hq_event_fn, timeoutMs C.int32_t) (*entry, context.Context) {
	e := &entry{id: nextID.Add(1), cb: cb, out: make(chan outMsg, sendQueue), done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	e.cancel = cancel
	connMu.Lock()
	conns[e.id] = e
	connMu.Unlock()
	return e, ctx
}

// hq_prepare 是打洞的第一步：开一个 socket，经中转 relay 问出它的公网地址，用 evPrepared 报回。
// Dart 把地址交给 Mac（posse.punch），拿到 Mac 的地址后调 hq_dial_prepared；不用了调 hq_close。
//
//export hq_prepare
func hq_prepare(relay *C.char, timeoutMs C.int32_t, cb C.hq_event_fn) C.int64_t {
	e, ctx := newEntry(cb, timeoutMs)
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

//export hq_free
func hq_free(p *C.uint8_t) {
	C.free(unsafe.Pointer(p))
}

func main() {}
