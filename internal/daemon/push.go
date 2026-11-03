package daemon

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"posse/internal/agent"
	"posse/internal/mobile"
)

// 手机推送的触发（方案见 docs/plans/push.md）：agent 进入等你处理，或从工作中停下、记成没看。
// 先等 pushDelay 再看一次：桌面窗口看着时下一帧就报看过，等待也常常一两秒就被处理掉。
const (
	pushDelay = 4 * time.Second
	// pushGap 是同一个终端两次推送的最短间隔：连着几个权限确认时只推第一次
	pushGap = 30 * time.Second
	// macActiveWithin 是多久内 Mac 有过键鼠输入算有人在用：桌面通知就够了，不推手机
	macActiveWithin = 60 * time.Second
)

func (d *Daemon) schedulePush(s *session, prev, next string) {
	if d.mobile == nil || s.agentName() == "" {
		return
	}
	if kind := pushKind(prev, next, s.isUnread()); kind != "" {
		time.AfterFunc(pushDelay, func() { d.firePush(s, kind) })
	}
}

// pushKind 是这次状态变化该推哪一类，不推时为空。规则同桌面通知（noticeTitle）。
func pushKind(prev, next string, unread bool) string {
	switch {
	case prev == next:
		return ""
	case next == agent.StatusWaiting:
		return mobile.PushWaiting
	case next == agent.StatusIdle && prev == agent.StatusWorking && unread:
		return mobile.PushDone
	}
	return ""
}

func (d *Daemon) firePush(s *session, kind string) {
	if !s.Running() {
		return
	}
	st := s.Agent().Status
	if kind == mobile.PushWaiting && st != agent.StatusWaiting ||
		kind == mobile.PushDone && (st != agent.StatusIdle || !s.isUnread()) {
		return
	}
	if idle, ok := hidIdle(); ok && idle < macActiveWithin {
		return
	}
	// 先占住间隔再发，免得同一终端两次触发一起发出去；没发出去（手机看着、没配中转）就还回去，不吞掉下一次
	s.mu.Lock()
	prev := s.pushAt
	if time.Since(prev) < pushGap {
		s.mu.Unlock()
		return
	}
	s.pushAt = time.Now()
	s.mu.Unlock()
	if !d.mobile.Notify(s.ID, kind) {
		s.mu.Lock()
		s.pushAt = prev
		s.mu.Unlock()
	}
}

var hidIdleRe = regexp.MustCompile(`"HIDIdleTime" = (\d+)`)

// hidIdle 是 Mac 上次有键鼠输入到现在多久（IOHIDSystem 的 HIDIdleTime，纳秒）。读不到时 ok 为 false。
func hidIdle() (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0, false
	}
	m := hidIdleRe.FindSubmatch(out)
	if m == nil {
		return 0, false
	}
	ns, err := strconv.ParseInt(string(bytes.TrimSpace(m[1])), 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(ns), true
}
