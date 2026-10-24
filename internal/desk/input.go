package desk

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	inputVersion = 1

	// InputPlainLimit 是一条 input 明文的上限：数据报 1200 字节减去类型、计数器和 GCM 标签。
	InputPlainLimit = PacketLimit - (1 + 8 + 16)

	EventMove    byte = 1
	EventDown    byte = 2
	EventUp      byte = 3
	EventScroll  byte = 4
	EventKeyDown byte = 5
	EventKeyUp   byte = 6
	EventText    byte = 7

	ButtonLeft  byte = 1
	ButtonRight byte = 2

	// 修饰键位与 HID boot protocol 一致。bit0–3 是左 Ctrl、Shift、Alt、GUI（⌘），bit4–7 是右侧。
	ModLCtrl  byte = 1 << 0
	ModLShift byte = 1 << 1
	ModLAlt   byte = 1 << 2
	ModLGUI   byte = 1 << 3
	ModRCtrl  byte = 1 << 4
	ModRShift byte = 1 << 5
	ModRAlt   byte = 1 << 6
	ModRGUI   byte = 1 << 7
)

// CGEventFlags。左右还要单独的设备标志，只设 Shift/Command 分不出是哪一侧。
const (
	flagShift     uint64 = 0x00020000
	flagControl   uint64 = 0x00040000
	flagAlternate uint64 = 0x00080000
	flagCommand   uint64 = 0x00100000
	flagLCtl      uint64 = 0x00000001
	flagLShift    uint64 = 0x00000002
	flagRShift    uint64 = 0x00000004
	flagLCmd      uint64 = 0x00000008
	flagRCmd      uint64 = 0x00000010
	flagLAlt      uint64 = 0x00000020
	flagRAlt      uint64 = 0x00000040
	flagRCtl      uint64 = 0x00002000
)

var errInput = errors.New("desk: bad input")

// Event 是一条已经解开的输入。各字段只在对应 Kind 下有意义。
type Event struct {
	Kind   byte
	Seq    uint32
	X, Y   uint16
	Button byte
	Clicks byte
	DX, DY int16
	HID    uint16
	Mods   byte
	Text   string
}

// EncodeInput 编码一条 UDP input 包的明文。Swift 按这里的字节写，不要写成子进程控制管道的格式。
//
// 明文是小端。外层 UDP 的计数器，以及 beat 里的时刻和确认号，都是大端，这两处不要混。
// 一条明文最长 1175 字节（InputPlainLimit）。
//
// 布局：
//
//	u8 版本，固定 1
//	u8 事件个数，0–255
//	紧接着每个事件。截断、尾部多出来的字节、不认识的类型、非法 UTF-8，整包作废。
//
// 事件：
//
//	1 移动：u16 x、u16 y。没有序号。0–65535 对应整屏 0–1，是绝对位置。
//	  一个包里只有最后一次移动会执行。丢了没关系，下一次覆盖。
//	2 按下、3 松开：u32 序号、u8 键（1 左、2 右）、u8 点击次数、u16 x、u16 y。
//	  点击次数交给 kCGMouseEventClickState，双击是 2、三击是 3；0 按 1。
//	  位置跟这次点击走，不依赖前面的移动包。
//	4 滚动：u32 序号、i16 dx、i16 dy，像素。dy 原样进滚轮 1，dx 原样进滚轮 2，
//	  正方向就是 CGEvent 的正方向。自然滚动由手机决定，Mac 不翻转。
//	5 键按下、6 键松开：u32 序号、u16 USB HID 用法（键盘页 0x07）、u8 修饰键。
//	7 文字：u32 序号、u8 修饰键、u16 字节数、这么多 UTF-8。
//	  修饰键为 0 时按 Unicode 注入；不为 0 时按当前键盘布局把每个字符反查成键码，
//	  否则 ⌘C 这类快捷键程序不认。
//
// 按下、松开、滚动、按键、文字是必达事件。序号从 1 起，一次观看内只增不重用，0 无效。
// 手机在之后每个 input 包里重复带上还没被确认的必达事件。包内顺序就是发生顺序：
// 移动留在它前后的必达事件之间，先按下再移动才会变成拖动。
//
// 确认号不在这条明文里。beat 明文固定 16 字节、大端：前 8 字节是发出时刻的 Unix 纳秒，
// 后 8 字节是已经执行或跳过的最大连续序号（uint32 零扩展）。手机丢掉序号小于等于确认号的事件。
//
// 快捷键栏的 HID 用法：Esc 0x29，Tab 0x2B，右 0x4F，左 0x50，下 0x51，上 0x52，
// F1–F12 是 0x3A–0x45，左 Ctrl/Shift/Alt/⌘ 是 0xE0–0xE3，右侧是 0xE4–0xE7。
func EncodeInput(events []Event) ([]byte, error) {
	if len(events) > 255 {
		return nil, errors.New("desk: too many input events")
	}
	n := 2
	for _, ev := range events {
		sz, err := eventSize(ev)
		if err != nil {
			return nil, err
		}
		n += sz
	}
	buf := make([]byte, 2, n)
	buf[0] = inputVersion
	buf[1] = byte(len(events))
	for _, ev := range events {
		buf = appendEvent(buf, ev)
	}
	return buf, nil
}

// DecodeInput 解开一条 input 明文。字节不齐时不分配事件。
func DecodeInput(b []byte) ([]Event, error) {
	if len(b) < 2 || b[0] != inputVersion {
		return nil, errInput
	}
	n := int(b[1])
	p := 2
	var out []Event
	if n > 0 {
		out = make([]Event, 0, n)
	}
	for range n {
		if p >= len(b) {
			return nil, errInput
		}
		kind := b[p]
		p++
		switch kind {
		case EventMove:
			if len(b)-p < 4 {
				return nil, errInput
			}
			out = append(out, Event{
				Kind: kind,
				X:    binary.LittleEndian.Uint16(b[p:]),
				Y:    binary.LittleEndian.Uint16(b[p+2:]),
			})
			p += 4
		case EventDown, EventUp:
			if len(b)-p < 10 {
				return nil, errInput
			}
			out = append(out, Event{
				Kind:   kind,
				Seq:    binary.LittleEndian.Uint32(b[p:]),
				Button: b[p+4],
				Clicks: b[p+5],
				X:      binary.LittleEndian.Uint16(b[p+6:]),
				Y:      binary.LittleEndian.Uint16(b[p+8:]),
			})
			p += 10
		case EventScroll:
			if len(b)-p < 8 {
				return nil, errInput
			}
			out = append(out, Event{
				Kind: kind,
				Seq:  binary.LittleEndian.Uint32(b[p:]),
				DX:   int16(binary.LittleEndian.Uint16(b[p+4:])),
				DY:   int16(binary.LittleEndian.Uint16(b[p+6:])),
			})
			p += 8
		case EventKeyDown, EventKeyUp:
			if len(b)-p < 7 {
				return nil, errInput
			}
			out = append(out, Event{
				Kind: kind,
				Seq:  binary.LittleEndian.Uint32(b[p:]),
				HID:  binary.LittleEndian.Uint16(b[p+4:]),
				Mods: b[p+6],
			})
			p += 7
		case EventText:
			if len(b)-p < 7 {
				return nil, errInput
			}
			seq := binary.LittleEndian.Uint32(b[p:])
			mods := b[p+4]
			nb := int(binary.LittleEndian.Uint16(b[p+5:]))
			p += 7
			if len(b)-p < nb {
				return nil, errInput
			}
			raw := b[p : p+nb]
			p += nb
			if !utf8.Valid(raw) {
				return nil, errInput
			}
			out = append(out, Event{Kind: kind, Seq: seq, Mods: mods, Text: string(raw)})
		default:
			return nil, errInput
		}
	}
	if p != len(b) {
		return nil, errInput
	}
	return out, nil
}

func eventSize(ev Event) (int, error) {
	switch ev.Kind {
	case EventMove:
		return 5, nil
	case EventDown, EventUp:
		return 11, nil
	case EventScroll:
		return 9, nil
	case EventKeyDown, EventKeyUp:
		return 8, nil
	case EventText:
		if len(ev.Text) > 65535 || !utf8.ValidString(ev.Text) {
			return 0, errInput
		}
		return 8 + len(ev.Text), nil
	default:
		return 0, errInput
	}
}

func appendEvent(buf []byte, ev Event) []byte {
	buf = append(buf, ev.Kind)
	switch ev.Kind {
	case EventMove:
		buf = putU16(buf, ev.X)
		buf = putU16(buf, ev.Y)
	case EventDown, EventUp:
		buf = putU32(buf, ev.Seq)
		buf = append(buf, ev.Button, ev.Clicks)
		buf = putU16(buf, ev.X)
		buf = putU16(buf, ev.Y)
	case EventScroll:
		buf = putU32(buf, ev.Seq)
		buf = putU16(buf, uint16(ev.DX))
		buf = putU16(buf, uint16(ev.DY))
	case EventKeyDown, EventKeyUp:
		buf = putU32(buf, ev.Seq)
		buf = putU16(buf, ev.HID)
		buf = append(buf, ev.Mods)
	case EventText:
		buf = putU32(buf, ev.Seq)
		buf = append(buf, ev.Mods)
		buf = putU16(buf, uint16(len(ev.Text)))
		buf = append(buf, ev.Text...)
	}
	return buf
}

func putU16(buf []byte, v uint16) []byte {
	var a [2]byte
	binary.LittleEndian.PutUint16(a[:], v)
	return append(buf, a[:]...)
}

func putU32(buf []byte, v uint32) []byte {
	var a [4]byte
	binary.LittleEndian.PutUint32(a[:], v)
	return append(buf, a[:]...)
}

// InputOrder 把手机重复发来的必达事件按序号排成可以执行的顺序。
// 序号从 1 起。缺号时先等；从发现缺号的那次 Push 起超过 1 秒还没有，就跳过缺的那段并记日志，
// 确认号照样往前走。时间靠之后的 Push 推进（手机会重传未确认的事件），这里不起定时器。
// 同一次 Push 里连续几个洞都已经超时的，一次跳完，不重新等。
// Push 不能并发。NewInputForwarder 自己串行。
//
// 移动没有序号。一个包只留最后一次，插回这个包里已经能执行的必达事件之间。
// 缺号挡住的必达事件不挡住移动，移动也不会在缺号补上之后再执行一次。
type InputOrder struct {
	next    uint32
	buf     map[uint32]Event
	gapOpen bool
	gapAt   time.Time
}

// Ack 是已经执行或跳过的最大连续序号。还没有时是 0。
func (o *InputOrder) Ack() uint32 {
	if o.next == 0 {
		return 0
	}
	return o.next - 1
}

// Push 收下一条明文里的事件。ready 按可以执行的顺序排，ack 是当前确认号。
func (o *InputOrder) Push(events []Event, now time.Time) (ready []Event, ack uint32) {
	if o.next == 0 {
		o.next = 1
	}
	if o.buf == nil {
		o.buf = make(map[uint32]Event)
	}
	lastMove := -1
	for i, ev := range events {
		if ev.Kind == EventMove {
			lastMove = i
		}
	}
	var before, after []uint32
	for i, ev := range events {
		// 远在前面的序号不收：缺号要等 1 秒才跳过，这期间收下的事件都留在 buf 里
		if ev.Kind == EventMove || !reliableKind(ev.Kind) || ev.Seq == 0 || ev.Seq < o.next || ev.Seq-o.next > inputWindow {
			continue
		}
		if _, ok := o.buf[ev.Seq]; !ok {
			o.buf[ev.Seq] = ev
		}
		if lastMove >= 0 {
			if i < lastMove {
				before = append(before, ev.Seq)
			} else {
				after = append(after, ev.Seq)
			}
		}
	}
	for {
		o.expire(now)
		n := len(ready)
		ready = o.drain(ready)
		if len(ready) == n {
			break
		}
	}
	if !o.hasHole() {
		o.gapOpen = false
	}
	if lastMove >= 0 {
		ready = spliceMove(ready, events[lastMove], before, after)
	}
	return ready, o.Ack()
}

// inputWindow 是确认号往后最多收多少个必达事件。
const inputWindow = 1024

func reliableKind(kind byte) bool {
	switch kind {
	case EventDown, EventUp, EventScroll, EventKeyDown, EventKeyUp, EventText:
		return true
	default:
		return false
	}
}

func (o *InputOrder) hasHole() bool {
	if _, ok := o.buf[o.next]; ok {
		return false
	}
	for seq := range o.buf {
		if seq > o.next {
			return true
		}
	}
	return false
}

func (o *InputOrder) expire(now time.Time) {
	if !o.hasHole() {
		o.gapOpen = false
		return
	}
	if !o.gapOpen {
		o.gapOpen = true
		o.gapAt = now
		return
	}
	if now.Sub(o.gapAt) < time.Second {
		return
	}
	var low uint32
	for seq := range o.buf {
		if seq > o.next && (low == 0 || seq < low) {
			low = seq
		}
	}
	if low <= o.next {
		return
	}
	// 不在这里清 gapOpen：同一次 Push 后面还有洞时沿用这次的超时。
	if low == o.next+1 {
		log.Printf("desk: input seq %d missing", o.next)
	} else {
		log.Printf("desk: input seq %d-%d missing", o.next, low-1)
	}
	o.next = low
}

func (o *InputOrder) drain(ready []Event) []Event {
	for {
		ev, ok := o.buf[o.next]
		if !ok {
			return ready
		}
		delete(o.buf, o.next)
		o.next++
		ready = append(ready, ev)
	}
}

func spliceMove(ready []Event, move Event, before, after []uint32) []Event {
	have := make(map[uint32]bool, len(ready))
	for _, ev := range ready {
		have[ev.Seq] = true
	}
	var pred uint32
	hasPred := false
	for _, seq := range before {
		if have[seq] {
			pred = seq
			hasPred = true
		}
	}
	var succ uint32
	hasSucc := false
	for _, seq := range after {
		if have[seq] {
			succ = seq
			hasSucc = true
			break
		}
	}
	if hasPred {
		for i, ev := range ready {
			if ev.Seq == pred {
				return insertEvent(ready, i+1, move)
			}
		}
	}
	if hasSucc {
		for i, ev := range ready {
			if ev.Seq == succ {
				return insertEvent(ready, i, move)
			}
		}
	}
	return append(ready, move)
}

func insertEvent(s []Event, i int, ev Event) []Event {
	if i >= len(s) {
		return append(s, ev)
	}
	s = append(s, Event{})
	copy(s[i+1:], s[i:])
	s[i] = ev
	return s
}

// NewInputForwarder 返回可以直接当作 CastConfig.OnInput 的函数。
// 明文按 InputOrder 去重排序，能执行的事件写成一条 CtrlInput；返回值是确认号，由 Serve 放进 beat。
// 明文解不开时确认号不动，管道也不写。
// control 要和 Serve 用同一个 LockWriter 包过的写端。
func NewInputForwarder(control io.Writer) func(plain []byte) uint64 {
	var (
		mu    sync.Mutex
		order InputOrder
	)
	return func(plain []byte) uint64 {
		mu.Lock()
		defer mu.Unlock()
		events, err := DecodeInput(plain)
		if err != nil {
			log.Printf("desk: bad input: %v", err)
			return uint64(order.Ack())
		}
		ready, ack := order.Push(events, time.Now())
		if len(ready) > 0 && control != nil {
			if err := WriteControl(control, Control{Kind: CtrlInput, Events: ready}); err != nil {
				log.Printf("desk: input forward: %v", err)
			}
		}
		return uint64(ack)
	}
}

// splitUTF16 按最多 limit 个 UTF-16 码元切开，不把一对代理对拆开。limit 至少为 2。
func splitUTF16(s string, limit int) [][]uint16 {
	u := utf16.Encode([]rune(s))
	var out [][]uint16
	for len(u) > 0 {
		n := limit
		if n > len(u) {
			n = len(u)
		}
		if n < len(u) && utf16.IsSurrogate(rune(u[n-1])) {
			n--
		}
		if n <= 0 {
			n = 1
		}
		out = append(out, u[:n])
		u = u[n:]
	}
	return out
}

func modifierFlags(mods byte) uint64 {
	var f uint64
	if mods&ModLCtrl != 0 {
		f |= flagControl | flagLCtl
	}
	if mods&ModRCtrl != 0 {
		f |= flagControl | flagRCtl
	}
	if mods&ModLShift != 0 {
		f |= flagShift | flagLShift
	}
	if mods&ModRShift != 0 {
		f |= flagShift | flagRShift
	}
	if mods&ModLAlt != 0 {
		f |= flagAlternate | flagLAlt
	}
	if mods&ModRAlt != 0 {
		f |= flagAlternate | flagRAlt
	}
	if mods&ModLGUI != 0 {
		f |= flagCommand | flagLCmd
	}
	if mods&ModRGUI != 0 {
		f |= flagCommand | flagRCmd
	}
	return f
}

// 指针移动每秒最多 240 次，限的是相邻两次的间隔。第一次放行。必达事件不经过这里。
const moveInterval = time.Second / 240

// MoveGate 决定这次移动发不发。
type MoveGate struct {
	on   bool
	next time.Time
}

// Allow 在间隔够了的时候放行。now 可以是零值，不拿 IsZero 当“还没开始”。
func (g *MoveGate) Allow(now time.Time) bool {
	if !g.on {
		g.on = true
		g.next = now.Add(moveInterval)
		return true
	}
	if now.Before(g.next) {
		return false
	}
	g.next = now.Add(moveInterval)
	return true
}

// NormPoint 把 0–65535 换成屏幕点坐标。65535 落在远端边上。
func NormPoint(originX, originY, width, height float64, x, y uint16) (float64, float64) {
	return originX + float64(x)/65535*width, originY + float64(y)/65535*height
}

// HIDToVirtualKey 把 USB HID 键盘用法换成 macOS 虚拟键码（ANSI 位置）。
// 键码 0 是 A，不在表里才是不认识。
func HIDToVirtualKey(usage uint16) (keycode uint16, ok bool) {
	keycode, ok = hidKey[usage]
	return
}

// hidKey 的键是 HID 用法，值是 Events.h 里的 kVK。0x32（非美式 #~）没有对应键。
var hidKey = map[uint16]uint16{
	0x04: 0x00, // A
	0x05: 0x0B, // B
	0x06: 0x08, // C
	0x07: 0x02, // D
	0x08: 0x0E, // E
	0x09: 0x03, // F
	0x0A: 0x05, // G
	0x0B: 0x04, // H
	0x0C: 0x22, // I
	0x0D: 0x26, // J
	0x0E: 0x28, // K
	0x0F: 0x25, // L
	0x10: 0x2E, // M
	0x11: 0x2D, // N
	0x12: 0x1F, // O
	0x13: 0x23, // P
	0x14: 0x0C, // Q
	0x15: 0x0F, // R
	0x16: 0x01, // S
	0x17: 0x11, // T
	0x18: 0x20, // U
	0x19: 0x09, // V
	0x1A: 0x0D, // W
	0x1B: 0x07, // X
	0x1C: 0x10, // Y
	0x1D: 0x06, // Z

	0x1E: 0x12, // 1
	0x1F: 0x13, // 2
	0x20: 0x14, // 3
	0x21: 0x15, // 4
	0x22: 0x17, // 5
	0x23: 0x16, // 6
	0x24: 0x1A, // 7
	0x25: 0x1C, // 8
	0x26: 0x19, // 9
	0x27: 0x1D, // 0

	0x28: 0x24, // Return
	0x29: 0x35, // Escape
	0x2A: 0x33, // Delete（退格）
	0x2B: 0x30, // Tab
	0x2C: 0x31, // Space
	0x2D: 0x1B, // -
	0x2E: 0x18, // =
	0x2F: 0x21, // [
	0x30: 0x1E, // ]
	0x31: 0x2A, // \
	0x33: 0x29, // ;
	0x34: 0x27, // '
	0x35: 0x32, // `
	0x36: 0x2B, // ,
	0x37: 0x2F, // .
	0x38: 0x2C, // /

	0x3A: 0x7A, // F1
	0x3B: 0x78, // F2
	0x3C: 0x63, // F3
	0x3D: 0x76, // F4
	0x3E: 0x60, // F5
	0x3F: 0x61, // F6
	0x40: 0x62, // F7
	0x41: 0x64, // F8
	0x42: 0x65, // F9
	0x43: 0x6D, // F10
	0x44: 0x67, // F11
	0x45: 0x6F, // F12

	0x39: 0x39, // Caps Lock
	0x4A: 0x73, // Home
	0x4B: 0x74, // Page Up
	0x4C: 0x75, // Forward Delete
	0x4D: 0x77, // End
	0x4E: 0x79, // Page Down

	0x4F: 0x7C, // Right
	0x50: 0x7B, // Left
	0x51: 0x7D, // Down
	0x52: 0x7E, // Up

	0xE0: 0x3B, // Left Control
	0xE1: 0x38, // Left Shift
	0xE2: 0x3A, // Left Option
	0xE3: 0x37, // Left Command
	0xE4: 0x3E, // Right Control
	0xE5: 0x3C, // Right Shift
	0xE6: 0x3D, // Right Option
	0xE7: 0x36, // Right Command
}
