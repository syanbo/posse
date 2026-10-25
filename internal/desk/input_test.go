package desk

import (
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

func TestInputRoundTrip(t *testing.T) {
	if InputPlainLimit != 1175 {
		t.Fatalf("plain limit %d", InputPlainLimit)
	}
	in := []Event{
		{Kind: EventMove, X: 100, Y: 200},
		{Kind: EventDown, Seq: 1, Button: ButtonLeft, Clicks: 2, X: 100, Y: 200},
		{Kind: EventUp, Seq: 2, Button: ButtonRight, Clicks: 1, X: 1, Y: 2},
		{Kind: EventScroll, Seq: 3, DX: -4, DY: 30},
		{Kind: EventKeyDown, Seq: 4, HID: 0x04, Mods: ModLGUI | ModLShift},
		{Kind: EventKeyUp, Seq: 5, HID: 0xE3, Mods: ModLGUI},
		{Kind: EventText, Seq: 6, Text: "你好a"},
		{Kind: EventText, Seq: 7, Mods: ModLGUI, Text: "c"},
	}
	raw, err := EncodeInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > InputPlainLimit {
		t.Fatalf("len %d", len(raw))
	}
	got, err := DecodeInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	eventsEqual(t, got, in)
	// 和手机端 mobile/test/desk_wire_test.dart 的同一组字节，两边改格式要一起改
	want := "0108016400c800020100000001026400c80003020000000201010002000403000000fcff1e00050400000004000a0605000000e300080706000000000700e4bda0e5a5bd61070700000008010063"
	if hex.EncodeToString(raw) != want {
		t.Fatalf("bytes %x", raw)
	}
	empty, err := DecodeInput([]byte{inputVersion, 0})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %v %v", empty, err)
	}
}

func TestInputReject(t *testing.T) {
	move, err := EncodeInput([]Event{{Kind: EventMove, X: 1, Y: 2}})
	if err != nil {
		t.Fatal(err)
	}
	text := []byte{inputVersion, 1, EventText, 1, 0, 0, 0, 0, 1, 0, 0xff}
	cases := [][]byte{
		{2, 0},
		{inputVersion, 1},
		{inputVersion, 1, 9},
		append([]byte{inputVersion, 0}, 0),
		text,
		append(append([]byte{}, move...), 0),
	}
	for _, c := range cases {
		if ev, err := DecodeInput(c); err == nil || ev != nil {
			t.Fatalf("accepted %x -> %v", c, ev)
		}
	}
	tooMany := make([]Event, 256)
	for i := range tooMany {
		tooMany[i] = Event{Kind: EventMove}
	}
	if _, err := EncodeInput(tooMany); err == nil {
		t.Fatal("256 events accepted")
	}
	long := Event{Kind: EventText, Seq: 1, Text: strings.Repeat("a", 65536)}
	if _, err := EncodeInput([]Event{long}); err == nil {
		t.Fatal("long text accepted")
	}
	if _, err := EncodeInput([]Event{{Kind: 9}}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestInputOrderReorderDuplicateRetransmit(t *testing.T) {
	var o InputOrder
	t0 := time.Unix(0, 0)
	down := func(seq uint32, x uint16) Event {
		return Event{Kind: EventDown, Seq: seq, Button: ButtonLeft, Clicks: 1, X: x, Y: x}
	}
	ready, ack := o.Push([]Event{down(2, 2)}, t0)
	if len(ready) != 0 || ack != 0 {
		t.Fatalf("early = %v ack %d", ready, ack)
	}
	ready, ack = o.Push([]Event{down(1, 1), down(2, 2)}, t0)
	eventsEqual(t, ready, []Event{down(1, 1), down(2, 2)})
	if ack != 2 {
		t.Fatalf("ack %d", ack)
	}
	ready, ack = o.Push([]Event{down(1, 9), down(2, 9)}, t0)
	if len(ready) != 0 || ack != 2 {
		t.Fatalf("dup = %v ack %d", ready, ack)
	}
	ready, ack = o.Push([]Event{down(1, 1), down(2, 2), down(3, 3)}, t0)
	eventsEqual(t, ready, []Event{down(3, 3)})
	if ack != 3 || o.Ack() != 3 {
		t.Fatalf("ack %d %d", ack, o.Ack())
	}
}

func TestInputOrderGapTimeout(t *testing.T) {
	var o InputOrder
	t0 := time.Unix(0, 0)
	held := Event{Kind: EventKeyDown, Seq: 2, HID: 0x04}
	o.Push([]Event{held}, t0)
	ready, ack := o.Push(nil, t0.Add(time.Second-time.Millisecond))
	if len(ready) != 0 || ack != 0 {
		t.Fatalf("early timeout %v ack %d", ready, ack)
	}
	ready, ack = o.Push(nil, t0.Add(time.Second))
	eventsEqual(t, ready, []Event{held})
	if ack != 2 {
		t.Fatalf("ack %d", ack)
	}
	ready, ack = o.Push([]Event{{Kind: EventKeyDown, Seq: 1, HID: 0x05}}, t0.Add(2*time.Second))
	if len(ready) != 0 || ack != 2 {
		t.Fatalf("late = %v ack %d", ready, ack)
	}
}

func TestInputOrderZeroTime(t *testing.T) {
	var o InputOrder
	var zero time.Time
	held := Event{Kind: EventKeyUp, Seq: 2, HID: 0x28}
	o.Push([]Event{held}, zero)
	ready, ack := o.Push(nil, zero.Add(time.Second))
	eventsEqual(t, ready, []Event{held})
	if ack != 2 {
		t.Fatalf("ack %d", ack)
	}
}

func TestInputOrderMultiHole(t *testing.T) {
	var o InputOrder
	t0 := time.Unix(0, 0)
	a := Event{Kind: EventKeyDown, Seq: 2, HID: 0x04}
	b := Event{Kind: EventKeyDown, Seq: 4, HID: 0x05}
	o.Push([]Event{a, b}, t0)
	ready, ack := o.Push(nil, t0.Add(time.Second))
	eventsEqual(t, ready, []Event{a, b})
	if ack != 4 {
		t.Fatalf("ack %d", ack)
	}
}

func TestInputOrderMove(t *testing.T) {
	t0 := time.Unix(0, 0)
	down := Event{Kind: EventDown, Seq: 1, Button: ButtonLeft, Clicks: 1, X: 5, Y: 6}
	move := Event{Kind: EventMove, X: 7, Y: 8}
	var o InputOrder
	ready, _ := o.Push([]Event{down, move}, t0)
	eventsEqual(t, ready, []Event{down, move})

	var o2 InputOrder
	ready, _ = o2.Push([]Event{move, down}, t0)
	eventsEqual(t, ready, []Event{move, down})

	var o3 InputOrder
	later := Event{Kind: EventMove, X: 9, Y: 9}
	ready, _ = o3.Push([]Event{{Kind: EventMove, X: 1, Y: 1}, down, later}, t0)
	eventsEqual(t, ready, []Event{down, later})

	var o4 InputOrder
	early := Event{Kind: EventMove, X: 4, Y: 5}
	held := Event{Kind: EventDown, Seq: 2, Button: ButtonLeft, Clicks: 1, X: 4, Y: 5}
	ready, ack := o4.Push([]Event{early, held}, t0)
	eventsEqual(t, ready, []Event{early})
	if ack != 0 {
		t.Fatalf("ack %d", ack)
	}
	fill := Event{Kind: EventDown, Seq: 1, Button: ButtonRight, Clicks: 1, X: 1, Y: 1}
	ready, ack = o4.Push([]Event{fill}, t0)
	eventsEqual(t, ready, []Event{fill, held})
	if ack != 2 {
		t.Fatalf("ack %d", ack)
	}

	var o5 InputOrder
	up := Event{Kind: EventUp, Seq: 2, Button: ButtonLeft, Clicks: 1, X: 8, Y: 8}
	o5.Push([]Event{up}, t0)
	click := Event{Kind: EventDown, Seq: 1, Button: ButtonLeft, Clicks: 1, X: 1, Y: 1}
	mid := Event{Kind: EventMove, X: 8, Y: 8}
	ready, ack = o5.Push([]Event{click, mid}, t0)
	eventsEqual(t, ready, []Event{click, mid, up})
	if ack != 2 {
		t.Fatalf("ack %d", ack)
	}
}

func TestHIDToVirtualKey(t *testing.T) {
	code, ok := HIDToVirtualKey(0x04)
	if !ok || code != 0x00 {
		t.Fatalf("A = %d %v", code, ok)
	}
	seen := map[uint16]uint16{}
	for usage := uint16(0x04); usage <= 0x1D; usage++ {
		code, ok := HIDToVirtualKey(usage)
		if !ok {
			t.Fatalf("letter %x missing", usage)
		}
		if prev, dup := seen[code]; dup {
			t.Fatalf("keycode %x from %x and %x", code, prev, usage)
		}
		seen[code] = usage
	}
	checks := []struct {
		hid, key uint16
	}{
		{0x1E, 0x12}, {0x27, 0x1D}, {0x28, 0x24}, {0x29, 0x35}, {0x2A, 0x33},
		{0x2B, 0x30}, {0x2C, 0x31}, {0x3A, 0x7A}, {0x45, 0x6F},
		{0x4F, 0x7C}, {0x50, 0x7B}, {0x51, 0x7D}, {0x52, 0x7E},
		{0xE0, 0x3B}, {0xE1, 0x38}, {0xE2, 0x3A}, {0xE3, 0x37},
		{0xE4, 0x3E}, {0xE5, 0x3C}, {0xE6, 0x3D}, {0xE7, 0x36},
	}
	for _, c := range checks {
		got, ok := HIDToVirtualKey(c.hid)
		if !ok || got != c.key {
			t.Errorf("hid %x = %x %v, want %x", c.hid, got, ok, c.key)
		}
	}
	if _, ok := HIDToVirtualKey(0x01); ok {
		t.Fatal("hid 0x01 mapped")
	}
	if _, ok := HIDToVirtualKey(0x32); ok {
		t.Fatal("non-US hash mapped")
	}
	if _, ok := HIDToVirtualKey(0xE8); ok {
		t.Fatal("hid 0xE8 mapped")
	}
}

// ⌃← 要和真键盘一样带上 Fn 和数字小键盘标志，调度中心的切空间快捷键（Control+Fn）才认
func TestArrowKeysCarryFnFlags(t *testing.T) {
	left, ok := HIDToVirtualKey(0x50)
	if !ok {
		t.Fatal("no virtual key for left arrow")
	}
	if got := (modifierFlags(ModLCtrl) | keyFlags(left)) & 0x00ff0000; got != flagControl|flagNumericPad|flagSecondaryFn {
		t.Fatalf("ctrl+left flags %#x", got)
	}
	if a, _ := HIDToVirtualKey(0x04); keyFlags(a) != 0 {
		t.Fatal("letters must not carry fn")
	}
}

func TestModifierFlags(t *testing.T) {
	got := modifierFlags(ModLGUI | ModRShift)
	want := flagCommand | flagLCmd | flagShift | flagRShift
	if got != want {
		t.Fatalf("%x want %x", got, want)
	}
	if modifierFlags(0) != 0 {
		t.Fatal("empty mods")
	}
}

func TestMoveGate(t *testing.T) {
	var g MoveGate
	t0 := time.Unix(0, 0)
	if !g.Allow(t0) {
		t.Fatal("first")
	}
	if g.Allow(t0.Add(moveInterval - time.Nanosecond)) {
		t.Fatal("too soon")
	}
	if !g.Allow(t0.Add(moveInterval)) {
		t.Fatal("at interval")
	}
	var zero MoveGate
	if !zero.Allow(time.Time{}) {
		t.Fatal("zero time")
	}
}

func TestNormPoint(t *testing.T) {
	x, y := NormPoint(10, 20, 100, 50, 0, 65535)
	if x != 10 || y != 70 {
		t.Fatalf("%v %v", x, y)
	}
	x, y = NormPoint(0, 0, 65535, 65535, 1, 1)
	if x != 1 || y != 1 {
		t.Fatalf("%v %v", x, y)
	}
}

func TestSplitUTF16(t *testing.T) {
	s := strings.Repeat("a", 19) + "😀" + "b"
	chunks := splitUTF16(s, 20)
	if len(chunks) != 2 || len(chunks[0]) != 19 {
		t.Fatalf("chunks %d first %d", len(chunks), len(chunks[0]))
	}
	if utf16.IsSurrogate(rune(chunks[0][len(chunks[0])-1])) {
		t.Fatal("split a surrogate")
	}
	var u []uint16
	for _, c := range chunks {
		u = append(u, c...)
	}
	if string(utf16.Decode(u)) != s {
		t.Fatalf("got %q", string(utf16.Decode(u)))
	}
}

func TestCtrlInputRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	ev := []Event{{Kind: EventScroll, Seq: 4, DX: -2, DY: 9}, {Kind: EventText, Seq: 5, Text: "a"}}
	if err := WriteControl(&buf, Control{Kind: CtrlInput, Events: ev}); err != nil {
		t.Fatal(err)
	}
	if err := WriteControl(&buf, Control{Kind: 9}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadControl(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != CtrlInput {
		t.Fatalf("kind %d", got.Kind)
	}
	eventsEqual(t, got.Events, ev)
	got, err = ReadControl(&buf)
	if err != nil || got.Kind != 9 || len(got.Events) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

// 几处同时写控制管道：超过 PIPE_BUF 的输入不能和改码率交错，子进程要逐条读得出来。
func TestLockWriterKeepsMessagesWhole(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lw := LockWriter(w)
	big := []Event{{Kind: EventText, Seq: 1, Text: strings.Repeat("x", 1000)}}
	const each = 200
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				c := Control{Kind: CtrlBitrate, Bitrate: 1}
				if i%2 == 0 {
					c = Control{Kind: CtrlInput, Events: big}
				}
				if err := WriteControl(lw, c); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		w.Close()
	}()
	n := 0
	for {
		c, err := ReadControl(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d messages: %v", n, err)
		}
		if c.Kind == CtrlInput && c.Events[0].Text != big[0].Text {
			t.Fatal("input message torn")
		}
		n++
	}
	if n != 4*each {
		t.Fatalf("read %d messages", n)
	}
}

func TestInputForwarder(t *testing.T) {
	var buf bytes.Buffer
	fn := NewInputForwarder(&buf)
	if ack := fn([]byte{0xff}); ack != 0 || buf.Len() != 0 {
		t.Fatalf("bad plain ack %d bytes %d", ack, buf.Len())
	}
	held, err := EncodeInput([]Event{{Kind: EventKeyDown, Seq: 2, HID: 0x05}})
	if err != nil {
		t.Fatal(err)
	}
	if ack := fn(held); ack != 0 || buf.Len() != 0 {
		t.Fatalf("held ack %d bytes %d", ack, buf.Len())
	}
	both, err := EncodeInput([]Event{
		{Kind: EventKeyDown, Seq: 1, HID: 0x04},
		{Kind: EventKeyDown, Seq: 2, HID: 0x05},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack := fn(both); ack != 2 {
		t.Fatalf("ack %d", ack)
	}
	got, err := ReadControl(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != CtrlInput || len(got.Events) != 2 || got.Events[0].Seq != 1 || got.Events[1].Seq != 2 {
		t.Fatalf("%+v", got)
	}
	if ack := fn([]byte{0xff}); ack != 2 {
		t.Fatalf("ack after bad %d", ack)
	}
}

func eventsEqual(t *testing.T, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d\n got %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// 远在确认号前面的序号不收：缺号超时之前它们会一直占着缓冲。
func TestInputOrderIgnoresFarAhead(t *testing.T) {
	var o InputOrder
	now := time.Now()
	ready, ack := o.Push([]Event{{Kind: EventKeyDown, Seq: inputWindow + 5, HID: 0x04}}, now)
	if len(ready) != 0 || ack != 0 || len(o.buf) != 0 {
		t.Fatalf("far seq kept: ready=%v ack=%d buf=%d", ready, ack, len(o.buf))
	}
	ready, ack = o.Push([]Event{{Kind: EventKeyDown, Seq: 1, HID: 0x04}}, now)
	if len(ready) != 1 || ack != 1 {
		t.Fatalf("seq 1: ready=%v ack=%d", ready, ack)
	}
}
