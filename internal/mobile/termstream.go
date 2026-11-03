package mobile

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"posse/internal/host"
)

// 终端输出流用 Orca 的二进制帧（src/shared/terminal-stream-protocol.ts）：16 字节头 + 负载，加密后走 WebSocket 二进制帧。
const (
	opOutput        = 1
	opSnapshotStart = 2
	opSnapshotChunk = 3
	opSnapshotEnd   = 4

	// 手机侧单帧有上限；快照按块发
	snapshotChunk = 48 << 10
	maxOutput     = 256 << 10
)

func streamFrame(op byte, sid uint32, seq uint64, payload []byte) []byte {
	b := make([]byte, 16, 16+len(payload))
	b[0], b[1], b[2] = 0x74, 1, op
	binary.LittleEndian.PutUint32(b[4:], sid)
	binary.LittleEndian.PutUint32(b[8:], uint32(seq>>32))
	binary.LittleEndian.PutUint32(b[12:], uint32(seq))
	return append(b, payload...)
}

// utf8Complete 返回 b 中完整 UTF-8 字符的前缀长度：手机按帧各自解码文本，字符不能被拆到两帧。
func utf8Complete(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if r, _ := utf8.DecodeRune(b[i:]); r == utf8.RuneError && !utf8.FullRune(b[i:]) {
			return i
		}
		break
	}
	return len(b)
}

type viewport struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (c *conn) subscribeTerminal(id string, p json.RawMessage) error {
	var a struct {
		Terminal string    `json:"terminal"`
		Viewport *viewport `json:"viewport"`
	}
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	t := c.srv.host.Terminal(a.Terminal)
	if t == nil {
		// 首页列着还没恢复的终端，点开时才拉起
		t = c.srv.host.Restore(a.Terminal)
	}
	if t == nil {
		return errorf("not_found", "terminal %s not found", a.Terminal)
	}
	// HoldSize 的 key 要能区分同时看着的几路流；之后宿主独立成进程时它会跨进程传，所以用字符串
	key := fmt.Sprintf("mobile:%p", new(byte))
	// 和 Orca 一样，手机看着的时候终端按手机屏幕排版，离开后恢复成界面的尺寸
	if v := a.Viewport; v != nil && v.Cols >= 20 && v.Rows >= 8 && t.Running() {
		t.HoldSize(key, min(v.Cols, 500), min(v.Rows, 200))
	}
	go c.terminalStream(c.startStream(id), id, t, key)
	return nil
}

// terminalStream 先发快照再推实时输出。尺寸变了（另一台手机或界面）就重发一份快照，手机据此重排。
func (c *conn) terminalStream(ctx context.Context, id string, t host.Terminal, key string) {
	defer t.ReleaseSize(key)
	defer c.srv.host.View(t.Info().ID)()
	sid := c.srv.streamID()
	var seq uint64
	kind := "scrollback"
	// colors 是程序按它配色的那套（窗口终端在用的），手机照它画终端；变了推一条 colors。
	// 只给声明了 posse.termcolors.v1 的手机：Orca App 不认这条消息
	colors, withColors := t.Colors(), c.hasCapability(termColorsCap)
	for {
		snap, sub := t.Watch()
		cols, rows := t.Size()
		if kind == "scrollback" {
			msg := map[string]any{"type": "subscribed", "streamId": sid, "cols": cols, "rows": rows, "displayMode": "auto"}
			if withColors {
				msg["colors"] = colors
			}
			if c.stream(id, msg) != nil {
				sub.Close()
				return
			}
		}
		meta, _ := json.Marshal(map[string]any{"kind": kind, "cols": cols, "rows": rows, "displayMode": "auto", "seq": seq})
		ok := c.sendBinary(streamFrame(opSnapshotStart, sid, seq, meta)) == nil
		for len(snap) > 0 && ok {
			n := utf8Complete(snap[:min(len(snap), snapshotChunk)])
			if n == 0 {
				n = min(len(snap), snapshotChunk)
			}
			ok = c.sendBinary(streamFrame(opSnapshotChunk, sid, seq, snap[:n])) == nil
			snap = snap[n:]
		}
		if !ok || c.sendBinary(streamFrame(opSnapshotEnd, sid, seq, nil)) != nil {
			sub.Close()
			return
		}
		kind = "resized"
		var watch *host.Colors
		if withColors {
			watch = &colors
		}
		switch c.pump(ctx, id, sid, &seq, t, sub, cols, rows, watch) {
		case pumpStop:
			sub.Close()
			return
		case pumpExited:
			c.stream(id, map[string]any{"type": "end"})
			c.endStream(id)
			return
		}
		sub.Close()
	}
}

const (
	pumpStop = iota
	pumpExited
	pumpResync
)

// termColorsCap 是手机能照电脑的配色画终端（订阅时的 colors、配色变了推的 colors 消息）
const termColorsCap = "posse.termcolors.v1"

// pump 转发输出直到流被取消、程序结束，或需要重发快照（尺寸变化、接入者跟不上被断开）。
func (c *conn) pump(ctx context.Context, id string, sid uint32, seq *uint64, t host.Terminal, sub host.Subscription, cols, rows int, colors *host.Colors) int {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var carry []byte
	for {
		select {
		case <-ctx.Done():
			return pumpStop
		case <-tick.C:
			if c2, r2 := t.Size(); c2 != cols || r2 != rows {
				return pumpResync
			}
			if now := t.Colors(); colors != nil && now != *colors {
				*colors = now
				if c.stream(id, map[string]any{"type": "colors", "colors": now}) != nil {
					return pumpStop
				}
			}
			continue
		case chunk, ok := <-sub.Output():
			if !ok {
				if !t.Running() {
					return pumpExited
				}
				return pumpResync
			}
			sub.Received(chunk)
			buf := append(carry, chunk...)
		drain:
			for len(buf) < maxOutput {
				select {
				case more, ok := <-sub.Output():
					if !ok {
						break drain
					}
					sub.Received(more)
					buf = append(buf, more...)
				default:
					break drain
				}
			}
			n := utf8Complete(buf)
			carry = append([]byte(nil), buf[n:]...)
			if n == 0 {
				continue
			}
			*seq += uint64(n)
			if c.sendBinary(streamFrame(opOutput, sid, *seq, buf[:n])) != nil {
				return pumpStop
			}
		}
	}
}
