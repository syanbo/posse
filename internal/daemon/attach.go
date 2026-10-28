package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"posse/internal/rpc"
)

type terminalAttachParams struct {
	Terminal string `json:"terminal"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
}

type terminalAttachResult struct {
	Cols    int  `json:"cols"`
	Rows    int  `json:"rows"`
	Running bool `json:"running"`
}

// terminalAttach 把终端接到界面：先发快照，再推送之后的输出；界面发来键入和尺寸变化。
func (d *Daemon) terminalAttach(ctx context.Context, raw json.RawMessage, conn net.Conn, r *bufio.Reader) error {
	var p terminalAttachParams
	if err := rpc.Decode(raw, &p); err != nil {
		return err
	}
	s := d.liveSession(p.Terminal)
	if s == nil {
		// 还没恢复的终端按接入方的尺寸拉起，agent 不用先按默认尺寸画一遍
		var err error
		if s, err = d.restoreTerminal(p.Terminal, p.Cols, p.Rows); err != nil {
			return err
		}
	}
	snap, sub := s.Attach(p.Cols, p.Rows)
	defer sub.Close()
	cols, rows := s.Size()
	rpc.WriteResult(conn, terminalAttachResult{Cols: cols, Rows: rows, Running: s.Running()})

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
			switch typ {
			case rpc.FrameInput:
				s.Send(payload)
			case rpc.FrameResize:
				if c, rw, ok := rpc.ParseResize(payload); ok && c >= 2 && rw >= 2 {
					s.Resize(c, rw)
				}
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
		case <-inputDone:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
