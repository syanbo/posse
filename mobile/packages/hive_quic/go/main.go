// hive_quic 是编进手机 App 的 QUIC 客户端，Dart 经 FFI 调用。拨号、收发都不阻塞调用方：
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

	"hive/internal/mquic"
)

// 回调的 kind。数据由 Go 用 malloc 分配，Dart 拷走后调 hq_free。
const (
	evOpen   = 1
	evText   = 2
	evBinary = 3
	evClosed = 4 // 数据是原因
	evFailed = 5 // 拨号失败，数据是错误
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
	e := &entry{id: nextID.Add(1), cb: cb, out: make(chan outMsg, sendQueue), done: make(chan struct{})}
	a := C.GoString(addr)
	var tok []byte
	if tokenLen > 0 {
		tok = C.GoBytes(unsafe.Pointer(token), C.int(tokenLen))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	e.cancel = cancel
	connMu.Lock()
	conns[e.id] = e
	connMu.Unlock()
	go e.run(ctx, a, tok)
	return C.int64_t(e.id)
}

func (e *entry) run(ctx context.Context, addr string, token []byte) {
	c, err := mquic.Dial(ctx, addr, token)
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
	c := e.c
	e.mu.Unlock()
	if c == nil {
		e.cancel()
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

// hq_rebind 在手机换了网络后调用，马上向中转报新地址。
//
//export hq_rebind
func hq_rebind(id C.int64_t) {
	e := lookup(id)
	if e == nil {
		return
	}
	e.mu.Lock()
	c := e.c
	e.mu.Unlock()
	if c != nil {
		c.Rebind()
	}
}

//export hq_free
func hq_free(p *C.uint8_t) {
	C.free(unsafe.Pointer(p))
}

func main() {}
