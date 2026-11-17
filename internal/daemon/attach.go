package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"posse/internal/host"
	"posse/internal/rpc"
)

type terminalAttachParams struct {
	Terminal string               `json:"terminal"`
	Cols     int                  `json:"cols"`
	Rows     int                  `json:"rows"`
	Desktop  *rpc.DesktopIdentity `json:"desktop,omitempty"`
}

type terminalAttachResult struct {
	Cols     int  `json:"cols"`
	Rows     int  `json:"rows"`
	Running  bool `json:"running"`
	ReadOnly bool `json:"readOnly,omitempty"`
}

// terminalAttach 把终端接到界面：先发快照，再推送之后的输出；界面发来键入和尺寸变化。
func (d *Daemon) terminalAttach(ctx context.Context, raw json.RawMessage, conn net.Conn, r *bufio.Reader) error {
	var p terminalAttachParams
	if err := rpc.Decode(raw, &p); err != nil {
		return err
	}
	s := d.liveSession(p.Terminal)
	if s == nil {
		done, err := d.beginDesktopRestore(p.Desktop)
		if err != nil {
			return err
		}
		s, err = d.restoreTerminal(p.Terminal, p.Cols, p.Rows)
		done()
		if err != nil {
			return err
		}
	}
	c := &d.desktop
	c.mu.Lock()
	c.expireLocked()
	if p.Desktop == nil && c.owner != "" {
		c.mu.Unlock()
		return rpc.Errorf(rpc.CodeReadOnly, "desktop control is held by another client; update Posse to view this terminal")
	}
	if p.Desktop != nil && (p.Desktop.InstanceID != d.instanceID || p.Desktop.ClientID == "") {
		c.mu.Unlock()
		return rpc.Errorf(rpc.CodeConflict, "daemon instance changed; reconnect before attaching")
	}
	controlled := p.Desktop == nil || d.desktopValidLocked(p.Desktop)
	epoch, changed := c.epoch, c.changed
	var snap []byte
	var sub host.Subscription
	var cols, rows int
	if controlled {
		snap, sub = s.Attach(p.Cols, p.Rows)
		cols, rows = s.Size()
	} else {
		snap, sub, cols, rows = s.WatchSize()
	}
	var writer *desktopWriter
	if controlled {
		writer = &desktopWriter{close: func() { sub.Close(); conn.Close() }}
		if c.writers == nil {
			c.writers = map[*desktopWriter]struct{}{}
		}
		c.writers[writer] = struct{}{}
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.writers, writer); sub.Close(); c.mu.Unlock() }()
	rpc.WriteResult(conn, terminalAttachResult{Cols: cols, Rows: rows, Running: s.Running(), ReadOnly: !controlled})

	w := bufio.NewWriterSize(conn, 64<<10)
	write := func(typ byte, payload []byte) error {
		// 界面卡住时不能无限阻塞；超时后断开，输出继续进内核
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := rpc.WriteFrame(w, typ, payload); err != nil {
			return err
		}
		return w.Flush()
	}
	if err := write(rpc.FrameSnapshot, snap); err != nil {
		return nil
	}

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			typ, payload, err := rpc.ReadFrame(r)
			if err != nil {
				return
			}
			if !controlled {
				continue
			}
			var inputErr error
			writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			accepted := d.desktopInput(p.Desktop, epoch, func() {
				switch typ {
				case rpc.FrameInput:
					inputErr = s.SendContext(writeCtx, payload)
				case rpc.FrameResize:
					if cols, rows, ok := rpc.ParseResize(payload); ok && cols >= 2 && rows >= 2 {
						s.Resize(cols, rows)
					}
				}
			})
			cancel()
			if !accepted || inputErr != nil {
				return
			}
		}
	}()

	// chunk 是广播给所有接入者的同一切片，合并时不能往它后面追加，改用本连接自己的缓冲
	var merged []byte
	for {
		select {
		case chunk, ok := <-sub.Output():
			if !ok {
				// 内核重建后断开的直接关连接，界面马上重新接入拿新快照；程序结束时通知界面退出码
				if sub.Kicked() {
					return nil
				}
				select {
				case <-s.Exited():
					write(rpc.FrameExit, []byte(strconv.Itoa(s.ExitCode())))
				case <-changed:
				case <-time.After(2 * time.Second):
				}
				return nil
			}
			sub.Received(chunk)
			// 合并已经排队的输出，减少帧数
			frame, own := chunk, false
		drain:
			for len(frame) < 256<<10 {
				select {
				case more, ok := <-sub.Output():
					if !ok {
						break drain
					}
					sub.Received(more)
					if !own {
						merged, own = append(merged[:0], frame...), true
					}
					merged = append(merged, more...)
					frame = merged
				default:
					break drain
				}
			}
			if err := write(rpc.FrameOutput, frame); err != nil {
				return nil
			}
		case <-changed:
			return nil
		case <-inputDone:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
