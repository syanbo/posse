package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
)

// 本轮完成了还没看：桌面的标签、侧栏和手机首页都画铃铛。记在常驻进程里，桌面和手机看过一处就都不再提醒。
// 桌面窗口有焦点、选中这个会话时经 terminal.seen 报看过；手机开着它的终端页或会话页时算一直在看。

// noteStatus 在 agent 状态变化后调用：从工作中停下、没有手机看着时记成没看，离开本轮完成就清掉。
// 桌面看着的那一刻常驻进程不知道，照样记上，窗口下一帧报看过，期间它自己不画铃铛。
func (s *session) noteStatus(prev, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if next == agent.StatusWorking && prev != agent.StatusWorking {
		s.turnAt = time.Now()
	}
	switch {
	case next != agent.StatusIdle:
		s.unread = false
	case prev == agent.StatusWorking && s.viewers == 0:
		s.unread = true
	}
}

// turnStart 是这一轮开始的时间；还没见过开始工作时用终端的创建时间。
func (s *session) turnStart() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnAt.IsZero() {
		return s.CreatedAt
	}
	return s.turnAt
}

func (s *session) isUnread() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unread
}

func (s *session) markRead() {
	s.mu.Lock()
	s.unread = false
	s.mu.Unlock()
}

// view 记下手机开始看这个会话，返回的函数在不看时调用一次。开始和结束都算看过：看着时完成的也不提醒。
func (s *session) view() func() {
	s.mu.Lock()
	s.viewers++
	s.unread = false
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.viewers--
			s.unread = false
			s.mu.Unlock()
		})
	}
}

// terminalSeen 是桌面窗口报告看过了一个会话。
func (d *Daemon) terminalSeen(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Terminal string `json:"terminal"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if s := d.liveSession(p.Terminal); s != nil {
		s.markRead()
	}
	return map[string]any{}, nil
}
