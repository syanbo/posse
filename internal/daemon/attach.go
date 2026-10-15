package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"hive/internal/rpc"
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
	s, err := d.session(p.Terminal)
	if err != nil {
		return err
	}
	snap, sub := s.Attach(p.Cols, p.Rows)
	defer s.Detach(sub)
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

	for {
		select {
		case chunk, ok := <-sub.C:
			if !ok {
				// 程序结束时通知界面退出码；被判定跟不上而断开时直接关连接，界面会重新接入
				select {
				case <-s.Exited():
					write(rpc.FrameExit, []byte(strconv.Itoa(s.ExitCode())))
				case <-time.After(2 * time.Second):
				}
				return nil
			}
			// 合并已经排队的输出，减少帧数
			buf := chunk
		drain:
			for len(buf) < 256<<10 {
				select {
				case more, ok := <-sub.C:
					if !ok {
						break drain
					}
					buf = append(buf, more...)
				default:
					break drain
				}
			}
			if err := write(rpc.FrameOutput, buf); err != nil {
				return nil
			}
		case <-inputDone:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
