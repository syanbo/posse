package client

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"posse/internal/rpc"
)

// Conn 把常驻进程托管的终端接到 MyGo 终端插件（作为 terminal.Options.Conn）。
// 第一次 Resize 时才发起接入，这样快照按界面终端的尺寸生成，喂进去显示一致。
type Conn struct {
	c  *Client
	id string

	ready     chan struct{}
	readyOnce sync.Once
	closed    chan struct{}
	closeOnce sync.Once

	wmu  sync.Mutex
	conn net.Conn
	r    *bufio.Reader
	err  error

	// 握手尺寸在 ready 关闭前写入，之后只读；不随界面的 Resize 改变。
	cols int
	rows int

	pending  []byte
	exited   atomic.Bool
	exitCode atomic.Int64
	readOnly atomic.Bool
}

// attachTimeout 是接入等常驻进程回第一行的期限：常驻进程收了连接却一直不回时，Read 报错，窗口照常重新接入。
// 回复前要恢复终端（拉起进程）、生成快照，回滚区大、机器忙时要几秒，所以给得宽。
var attachTimeout = 30 * time.Second

func (c *Client) Attach(id string) *Conn {
	k := &Conn{c: c, id: id, ready: make(chan struct{}), closed: make(chan struct{})}
	// 视图通常很快就会设置尺寸；万一没有，按常驻进程现有尺寸接入
	time.AfterFunc(time.Second, func() { k.open(0, 0) })
	return k
}

func (k *Conn) open(cols, rows int) bool {
	first := false
	k.readyOnce.Do(func() {
		first = true
		select {
		case <-k.closed:
			k.err = io.EOF
			close(k.ready)
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
		defer cancel()
		var result struct {
			Cols     int  `json:"cols"`
			Rows     int  `json:"rows"`
			ReadOnly bool `json:"readOnly"`
		}
		conn, r, err := k.c.Open(ctx, "terminal.attach", map[string]any{"terminal": k.id, "cols": cols, "rows": rows, "desktop": k.c.Desktop}, &result)
		k.cols, k.rows = result.Cols, result.Rows
		k.readOnly.Store(result.ReadOnly)
		k.wmu.Lock()
		k.conn, k.r, k.err = conn, r, err
		k.wmu.Unlock()
		select {
		case <-k.closed:
			if conn != nil {
				conn.Close()
			}
		default:
		}
		close(k.ready)
	})
	return first
}

func (k *Conn) wait() error {
	select {
	case <-k.ready:
	case <-k.closed:
		return io.EOF
	}
	if k.err != nil {
		return k.err
	}
	return nil
}

// TerminalState 等待接入握手，返回快照对应的实际尺寸和只读状态。
// 不主动接入，首个 Resize 仍可决定请求尺寸。
func (k *Conn) TerminalState() (cols, rows int, readOnly bool, err error) {
	if err := k.wait(); err != nil {
		return 0, 0, false, err
	}
	return k.cols, k.rows, k.readOnly.Load(), nil
}

// TerminalSnapshot 由插件的唯一读协程在 Read 前调用，把首帧作为整体回放，
// 防止窗口在大快照分片之间调整网格。未使用此接口的调用方仍可直接 Read。
func (k *Conn) TerminalSnapshot() ([]byte, error) {
	if err := k.wait(); err != nil {
		return nil, err
	}
	typ, payload, err := rpc.ReadFrame(k.r)
	if err != nil {
		return nil, err
	}
	if typ != rpc.FrameSnapshot {
		return nil, io.ErrUnexpectedEOF
	}
	return payload, nil
}

func (k *Conn) Read(p []byte) (int, error) {
	if err := k.wait(); err != nil {
		return 0, err
	}
	for len(k.pending) == 0 {
		typ, payload, err := rpc.ReadFrame(k.r)
		if err != nil {
			return 0, io.EOF
		}
		switch typ {
		case rpc.FrameSnapshot, rpc.FrameOutput:
			k.pending = payload
		case rpc.FrameExit:
			code, _ := strconv.Atoi(string(payload))
			k.exitCode.Store(int64(code))
			k.exited.Store(true)
			return 0, io.EOF
		}
	}
	n := copy(p, k.pending)
	k.pending = k.pending[n:]
	return n, nil
}

func (k *Conn) Write(p []byte) (int, error) {
	if err := k.wait(); err != nil {
		return 0, err
	}
	if k.readOnly.Load() {
		return 0, rpc.Errorf(rpc.CodeReadOnly, "take desktop control before sending terminal input")
	}
	k.wmu.Lock()
	defer k.wmu.Unlock()
	if err := rpc.WriteFrame(k.conn, rpc.FrameInput, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (k *Conn) Resize(cols, rows int) error {
	if k.open(cols, rows) {
		return nil
	}
	if err := k.wait(); err != nil {
		return err
	}
	if k.readOnly.Load() {
		return nil
	}
	k.wmu.Lock()
	defer k.wmu.Unlock()
	return rpc.WriteFrame(k.conn, rpc.FrameResize, rpc.ResizePayload(cols, rows))
}

func (k *Conn) Close() error {
	k.closeOnce.Do(func() {
		close(k.closed)
		k.wmu.Lock()
		if k.conn != nil {
			k.conn.Close()
		}
		k.wmu.Unlock()
	})
	return nil
}

// Exited 报告程序是否已结束（收到了退出帧），以及退出码；
// 连接断开但程序仍在运行时返回 false，调用方应重新接入。
func (k *Conn) Exited() (bool, int) { return k.exited.Load(), int(k.exitCode.Load()) }
