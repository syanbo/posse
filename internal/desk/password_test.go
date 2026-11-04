package desk

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPasswordEventWire(t *testing.T) {
	in := []Event{{Kind: EventPassword, Seq: 9, Text: "pw!"}}
	raw, err := EncodeInput(in)
	if err != nil {
		t.Fatal(err)
	}
	// 和手机端 mobile/test/desk_wire_test.dart 的同一组字节，两边改格式要一起改
	if got := hex.EncodeToString(raw); got != "010108090000000300707721" {
		t.Fatalf("bytes %s", got)
	}
	got, err := DecodeInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	eventsEqual(t, got, in)
	for _, bad := range [][]byte{
		raw[:len(raw)-1],                                         // 字节数比实际多
		append(append([]byte{}, raw...), 0),                      // 尾部多出来
		{inputVersion, 1, EventPassword, 1, 0, 0},                // 头不齐
		{inputVersion, 1, EventPassword, 1, 0, 0, 0, 1, 0, 0xff}, // 非法 UTF-8
	} {
		if _, err := DecodeInput(bad); err == nil {
			t.Fatalf("accepted %x", bad)
		}
	}
}

// 手机没收到确认会重带同一条密码：只执行一次。
func TestPasswordEventDedup(t *testing.T) {
	var o InputOrder
	now := time.Now()
	ev := []Event{{Kind: EventPassword, Seq: 1, Text: "secret"}}
	ready, ack := o.Push(ev, now)
	if len(ready) != 1 || ready[0].Kind != EventPassword || ack != 1 {
		t.Fatalf("first: ready=%v ack=%d", ready, ack)
	}
	for range 3 {
		if ready, ack = o.Push(ev, now); len(ready) != 0 || ack != 1 {
			t.Fatalf("resent: ready=%v ack=%d", ready, ack)
		}
	}
}

type fakeLock struct {
	// states 是每次 locked() 依次给的结果，用完后一直是最后一个
	states []bool
	calls  int
	pid    int32
	noPID  bool
	// failAt 是第几个按键（从 1 数，回车也算）投递失败，0 不失败
	failAt int
	keys   int
	log    []string
}

func (f *fakeLock) locked() bool {
	i := min(f.calls, len(f.states)-1)
	f.calls++
	return f.states[i]
}

func (f *fakeLock) target() (int32, bool) { return f.pid, !f.noPID }

func (f *fakeLock) ready() { f.log = append(f.log, "ready") }

func (f *fakeLock) press(pid int32, r rune) bool {
	f.keys++
	if f.keys == f.failAt {
		return false
	}
	f.log = append(f.log, fmt.Sprintf("%d:%c", pid, r))
	return true
}

func (f *fakeLock) enter(pid int32) bool {
	f.keys++
	if f.keys == f.failAt {
		return false
	}
	f.log = append(f.log, fmt.Sprintf("%d:enter", pid))
	return true
}

func TestTypePassword(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	cases := []struct {
		name   string
		lock   *fakeLock
		want   string
		enters bool
	}{
		{"locked throughout", &fakeLock{states: []bool{true}, pid: 414}, "ready 414:a 414:B 414:1 414:enter", true},
		{"not locked", &fakeLock{states: []bool{false}, pid: 414}, "", false},
		{"no lock screen process", &fakeLock{states: []bool{true}, noPID: true}, "", false},
		// 唤醒密码框的工夫解开了：一个字也不打
		{"unlocked while waking", &fakeLock{states: []bool{true, false}, pid: 414}, "ready", false},
		// 打完两个字时解开了：剩下的字和回车都不发
		{"unlocked midway", &fakeLock{states: []bool{true, true, true, false}, pid: 414}, "ready 414:a 414:B", false},
		{"unlocked before return", &fakeLock{states: []bool{true, true, true, true, false}, pid: 414}, "ready 414:a 414:B 414:1", false},
		{"key fails", &fakeLock{states: []bool{true}, pid: 414, failAt: 2}, "ready 414:a", false},
		// 回车没投出去不算打完：手机不能因此记住密码
		{"return fails", &fakeLock{states: []bool{true}, pid: 414, failAt: 4}, "ready 414:a 414:B 414:1", false},
	}
	for _, c := range cases {
		logs = nil
		entered := typePassword(c.lock, "aB1", logf)
		if got := strings.Join(c.lock.log, " "); got != c.want || entered != c.enters {
			t.Fatalf("%s: posted %q entered %v, want %q %v", c.name, got, entered, c.want, c.enters)
		}
		for _, l := range logs {
			if strings.Contains(l, "aB1") || strings.Contains(l, "3") {
				t.Fatalf("%s: log leaks the password: %q", c.name, l)
			}
		}
		if !entered && len(logs) == 0 {
			t.Fatalf("%s: dropped without a log line", c.name)
		}
	}
	if typePassword(&fakeLock{states: []bool{true}, pid: 1}, "", logf) {
		t.Fatal("empty password pressed return")
	}
}
