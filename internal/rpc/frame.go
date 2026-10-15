package rpc

import (
	"encoding/binary"
	"fmt"
	"io"
)

// 帧格式：1 字节类型 + 4 字节大端长度 + 负载。
const (
	FrameSnapshot = 'S' // 常驻进程 → 界面：接入时的屏幕快照（VT 序列）
	FrameOutput   = 'O' // 常驻进程 → 界面：程序输出
	FrameExit     = 'X' // 常驻进程 → 界面：程序退出，负载为十进制退出码
	FrameInput    = 'I' // 界面 → 常驻进程：键入和终端查询的应答
	FrameResize   = 'R' // 界面 → 常驻进程：2 字节列数 + 2 字节行数
)

// 快照包含回滚区，上限要留足
const maxFrame = 64 << 20

func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func ReadFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func ResizePayload(cols, rows int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return b
}

func ParseResize(b []byte) (cols, rows int, ok bool) {
	if len(b) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(b)), int(binary.BigEndian.Uint16(b[2:])), true
}
